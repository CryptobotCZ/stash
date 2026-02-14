package api

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os/exec"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/internal/static"
	"github.com/stashapp/stash/pkg/file"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/image"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/utils"
)

type ImageFinder interface {
	models.ImageGetter
	FindByChecksum(ctx context.Context, checksum string) ([]*models.Image, error)
}

type imageRoutes struct {
	routes
	imageFinder ImageFinder
	fileGetter  models.FileGetter
}

func (rs imageRoutes) Routes() chi.Router {
	r := chi.NewRouter()

	r.Route("/{imageId}", func(r chi.Router) {
		r.Use(rs.ImageCtx)

		r.Get("/image", rs.Image)
		r.Get("/thumbnail", rs.Thumbnail)
		r.Get("/preview", rs.Preview)
	})

	return r
}

func (rs imageRoutes) Thumbnail(w http.ResponseWriter, r *http.Request) {
	img := r.Context().Value(imageKey).(*models.Image)
	rs.serveThumbnail(w, r, img, nil)
}

func (rs imageRoutes) serveThumbnail(w http.ResponseWriter, r *http.Request, img *models.Image, modTime *time.Time) {
	mgr := manager.GetInstance()
	storageType := mgr.Config.GetImageThumbnailsStorage()

	// Handle DATABASE storage modes
	if storageType == config.ImageThumbnailsStorageDatabase {
		rs.serveThumbnailFromDB(w, r, img, modTime)
		return
	}

	if storageType == config.ImageThumbnailsStoragePrefixed {
		rs.serveThumbnailFromPrefixedDB(w, r, img, modTime)
		return
	}

	// FILESYSTEM storage mode (original behavior)
	filepath := mgr.Paths.Generated.GetThumbnailPath(img.Checksum, models.DefaultGthumbWidth)

	// if the thumbnail doesn't exist, encode on the fly
	exists, _ := fsutil.FileExists(filepath)
	if exists {
		if modTime == nil {
			utils.ServeStaticFile(w, r, filepath)
		} else {
			utils.ServeStaticFileModTime(w, r, filepath, *modTime)
		}
	} else {
		rs.generateAndServeThumbnail(w, r, img, filepath, modTime)
	}
}

func (rs imageRoutes) serveThumbnailFromDB(w http.ResponseWriter, r *http.Request, img *models.Image, modTime *time.Time) {
	mgr := manager.GetInstance()
	thumbnailDB := mgr.ThumbnailDB
	if thumbnailDB == nil {
		// Fallback to filesystem if thumbnail DB is not initialized
		rs.serveThumbnailFromFilesystem(w, r, img, modTime)
		return
	}

	// Try to read from database first
	data, err := thumbnailDB.Read(img.Checksum)
	if err == nil {
		// Found in database, serve it
		if modTime != nil {
			w.Header().Set("Last-Modified", modTime.Format(http.TimeFormat))
		}
		utils.ServeStaticContent(w, r, data)
		return
	}

	// Not found in database, generate and save
	rs.generateAndServeThumbnailToDB(w, r, img)
}

func (rs imageRoutes) serveThumbnailFromPrefixedDB(w http.ResponseWriter, r *http.Request, img *models.Image, modTime *time.Time) {
	mgr := manager.GetInstance()
	prefixedDB := mgr.PrefixedThumbnailDB
	if prefixedDB == nil {
		// Fallback to filesystem if prefixed thumbnail DB is not initialized
		rs.serveThumbnailFromFilesystem(w, r, img, modTime)
		return
	}

	// Try to read from prefixed database first
	data, err := prefixedDB.Read(img.Checksum)
	if err == nil {
		// Found in database, serve it
		if modTime != nil {
			w.Header().Set("Last-Modified", modTime.Format(http.TimeFormat))
		}
		utils.ServeStaticContent(w, r, data)
		return
	}

	// Not found in database, generate and save
	rs.generateAndServeThumbnailToPrefixedDB(w, r, img)
}

func (rs imageRoutes) serveThumbnailFromFilesystem(w http.ResponseWriter, r *http.Request, img *models.Image, modTime *time.Time) {
	mgr := manager.GetInstance()
	filepath := mgr.Paths.Generated.GetThumbnailPath(img.Checksum, models.DefaultGthumbWidth)

	exists, _ := fsutil.FileExists(filepath)
	if exists {
		if modTime == nil {
			utils.ServeStaticFile(w, r, filepath)
		} else {
			utils.ServeStaticFileModTime(w, r, filepath, *modTime)
		}
	} else {
		rs.generateAndServeThumbnail(w, r, img, filepath, modTime)
	}
}

func (rs imageRoutes) generateAndServeThumbnailToDB(w http.ResponseWriter, r *http.Request, img *models.Image) {
	mgr := manager.GetInstance()
	thumbnailDB := mgr.ThumbnailDB
	if thumbnailDB == nil {
		http.Error(w, "thumbnail database not available", http.StatusInternalServerError)
		return
	}

	f := img.Files.Primary()
	if f == nil {
		const useDefault = true
		rs.serveImage(w, r, img, useDefault)
		return
	}

	// use the image thumbnail generate wait group to limit the number of concurrent thumbnail generation tasks
	wg := &mgr.ImageThumbnailGenerateWaitGroup
	wg.Add()
	defer wg.Done()

	clipPreviewOptions := image.ClipPreviewOptions{
		InputArgs:  mgr.Config.GetTranscodeInputArgs(),
		OutputArgs: mgr.Config.GetTranscodeOutputArgs(),
		Preset:     mgr.Config.GetPreviewPreset().String(),
	}

	encoder := image.NewThumbnailEncoder(mgr.FFMpeg, mgr.FFProbe, clipPreviewOptions)
	data, err := encoder.GetThumbnail(f, models.DefaultGthumbWidth)
	if err != nil {
		if !errors.Is(err, image.ErrNotSupportedForThumbnail) && !errors.Is(err, fs.ErrNotExist) {
			logger.Errorf("error generating thumbnail for %s: %v", f.Base().Path, err)
		}
		const useDefault = true
		rs.serveImage(w, r, img, useDefault)
		return
	}

	// Write to database
	if err := thumbnailDB.Write(img.Checksum, data); err != nil {
		logger.Errorf("error writing thumbnail to database for image %s: %v", img.Path, err)
	}

	// Serve the thumbnail
	utils.ServeStaticContent(w, r, data)
}

func (rs imageRoutes) generateAndServeThumbnailToPrefixedDB(w http.ResponseWriter, r *http.Request, img *models.Image) {
	mgr := manager.GetInstance()
	prefixedDB := mgr.PrefixedThumbnailDB
	if prefixedDB == nil {
		http.Error(w, "prefixed thumbnail database not available", http.StatusInternalServerError)
		return
	}

	f := img.Files.Primary()
	if f == nil {
		const useDefault = true
		rs.serveImage(w, r, img, useDefault)
		return
	}

	// use the image thumbnail generate wait group to limit the number of concurrent thumbnail generation tasks
	wg := &mgr.ImageThumbnailGenerateWaitGroup
	wg.Add()
	defer wg.Done()

	clipPreviewOptions := image.ClipPreviewOptions{
		InputArgs:  mgr.Config.GetTranscodeInputArgs(),
		OutputArgs: mgr.Config.GetTranscodeOutputArgs(),
		Preset:     mgr.Config.GetPreviewPreset().String(),
	}

	encoder := image.NewThumbnailEncoder(mgr.FFMpeg, mgr.FFProbe, clipPreviewOptions)
	data, err := encoder.GetThumbnail(f, models.DefaultGthumbWidth)
	if err != nil {
		if !errors.Is(err, image.ErrNotSupportedForThumbnail) && !errors.Is(err, fs.ErrNotExist) {
			logger.Errorf("error generating thumbnail for %s: %v", f.Base().Path, err)
		}
		const useDefault = true
		rs.serveImage(w, r, img, useDefault)
		return
	}

	// Write to prefixed database
	if err := prefixedDB.Write(img.Checksum, data); err != nil {
		logger.Errorf("error writing thumbnail to prefixed database for image %s: %v", img.Path, err)
	}

	// Serve the thumbnail
	utils.ServeStaticContent(w, r, data)
}

func (rs imageRoutes) generateAndServeThumbnail(w http.ResponseWriter, r *http.Request, img *models.Image, filepath string, modTime *time.Time) {
	mgr := manager.GetInstance()
	const useDefault = true

	f := img.Files.Primary()
	if f == nil {
		rs.serveImage(w, r, img, useDefault)
		return
	}

	// use the image thumbnail generate wait group to limit the number of concurrent thumbnail generation tasks
	wg := &mgr.ImageThumbnailGenerateWaitGroup
	wg.Add()
	defer wg.Done()

	clipPreviewOptions := image.ClipPreviewOptions{
		InputArgs:  mgr.Config.GetTranscodeInputArgs(),
		OutputArgs: mgr.Config.GetTranscodeOutputArgs(),
		Preset:     mgr.Config.GetPreviewPreset().String(),
	}

	encoder := image.NewThumbnailEncoder(mgr.FFMpeg, mgr.FFProbe, clipPreviewOptions)
	data, err := encoder.GetThumbnail(f, models.DefaultGthumbWidth)
	if err != nil {
		// don't log for unsupported image format
		// don't log for file not found - can optionally be logged in serveImage
		if !errors.Is(err, image.ErrNotSupportedForThumbnail) && !errors.Is(err, fs.ErrNotExist) {
			logger.Errorf("error generating thumbnail for %s: %v", f.Base().Path, err)

			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				logger.Errorf("stderr: %s", string(exitErr.Stderr))
			}
		}

		// backwards compatibility - fallback to original image instead
		rs.serveImage(w, r, img, useDefault)
		return
	}

	// write the generated thumbnail to disk if enabled
	if mgr.Config.IsWriteImageThumbnails() {
		logger.Debugf("writing thumbnail to disk: %s", img.Path)
		if err := fsutil.WriteFile(filepath, data); err == nil {
			utils.ServeStaticFile(w, r, filepath)
			return
		}
		logger.Errorf("error writing thumbnail for image %s: %v", img.Path, err)
	}
	utils.ServeStaticContent(w, r, data)
}

func (rs imageRoutes) Preview(w http.ResponseWriter, r *http.Request) {
	img := r.Context().Value(imageKey).(*models.Image)
	filepath := manager.GetInstance().Paths.Generated.GetClipPreviewPath(img.Checksum, models.DefaultGthumbWidth)

	// don't check if the preview exists - we'll just return a 404 if it doesn't
	utils.ServeStaticFile(w, r, filepath)
}

func (rs imageRoutes) Image(w http.ResponseWriter, r *http.Request) {
	i := r.Context().Value(imageKey).(*models.Image)

	const useDefault = false
	rs.serveImage(w, r, i, useDefault)
}

func (rs imageRoutes) serveImage(w http.ResponseWriter, r *http.Request, i *models.Image, useDefault bool) {
	if i.Files.Primary() != nil {
		err := i.Files.Primary().Base().Serve(&file.OsFS{}, w, r)
		if err == nil {
			return
		}

		if !useDefault {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// only log in debug since it can get noisy
		logger.Debugf("Error serving %s: %v", i.DisplayName(), err)
	}

	if !useDefault {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}

	// fallback to default image
	image := static.ReadAll(static.DefaultImageImage)
	utils.ServeImage(w, r, image)
}

func (rs imageRoutes) ImageCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageIdentifierQueryParam := chi.URLParam(r, "imageId")
		imageID, _ := strconv.Atoi(imageIdentifierQueryParam)

		var image *models.Image
		_ = rs.withReadTxn(r, func(ctx context.Context) error {
			qb := rs.imageFinder
			if imageID == 0 {
				images, _ := qb.FindByChecksum(ctx, imageIdentifierQueryParam)
				if len(images) > 0 {
					image = images[0]
				}
			} else {
				image, _ = qb.Find(ctx, imageID)
			}

			if image != nil {
				if err := image.LoadPrimaryFile(ctx, rs.fileGetter); err != nil {
					if !errors.Is(err, context.Canceled) {
						logger.Errorf("error loading primary file for image %d: %v", imageID, err)
					}
					// set image to nil so that it doesn't try to use the primary file
					image = nil
				}
			}

			return nil
		})
		if image == nil {
			http.Error(w, http.StatusText(404), 404)
			return
		}

		ctx := context.WithValue(r.Context(), imageKey, image)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
