package manager

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/image"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

type GenerateImageThumbnailTask struct {
	Image     models.Image
	Overwrite bool
}

func (t *GenerateImageThumbnailTask) GetDescription() string {
	return fmt.Sprintf("Generating Thumbnail for image %s", t.Image.Path)
}

func (t *GenerateImageThumbnailTask) logStderr(err error) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		logger.Debugf("[generator] error output: %s", exitErr.Stderr)
	}
}

func (t *GenerateImageThumbnailTask) findGalleryID(ctx context.Context) int {
	mgr := GetInstance()
	galleries, err := mgr.Repository.Gallery.FindByImageID(ctx, t.Image.ID)
	if err == nil && len(galleries) > 0 {
		return galleries[0].ID
	}
	return 0
}

func (t *GenerateImageThumbnailTask) Start(ctx context.Context) {
	if !t.required() {
		return
	}

	mgr := GetInstance()
	storageType := mgr.Config.GetImageThumbnailsStorage()

	f := t.Image.Files.Primary()
	path := f.Base().Path

	logger.Debugf("Generating thumbnail for %s", path)

	c := mgr.Config

	clipPreviewOptions := image.ClipPreviewOptions{
		InputArgs:  c.GetTranscodeInputArgs(),
		OutputArgs: c.GetTranscodeOutputArgs(),
		Preset:     c.GetPreviewPreset().String(),
	}

	encoder := image.NewThumbnailEncoder(mgr.FFMpeg, mgr.FFProbe, clipPreviewOptions)
	data, err := encoder.GetThumbnail(f, models.DefaultGthumbWidth)

	if err != nil {
		// don't log for animated images
		if !errors.Is(err, image.ErrNotSupportedForThumbnail) {
			logger.Errorf("[generator] getting thumbnail for image %s: %s", path, err.Error())
			t.logStderr(err)
		}
		return
	}

	// Save based on storage type
	if storageType == config.ImageThumbnailsStorageDatabase {
		thumbnailDB := mgr.ThumbnailDB
		if thumbnailDB != nil {
			if err := thumbnailDB.Write(t.Image.Checksum, data); err != nil {
				logger.Errorf("[generator] writing thumbnail to database for image %s: %s", path, err.Error())
			}
		}
	} else if storageType == config.ImageThumbnailsStoragePrefixed {
		prefixedDB := mgr.PrefixedThumbnailDB
		if prefixedDB != nil {
			if err := prefixedDB.Write(t.Image.Checksum, data); err != nil {
				logger.Errorf("[generator] writing thumbnail to prefixed database for image %s: %s", path, err.Error())
			}
		}
	} else if storageType == config.ImageThumbnailsStoragePerGallery {
		perGalleryDB := mgr.PerGalleryThumbnailDB
		if perGalleryDB != nil {
			// Find the gallery this image belongs to
			galleryID := t.findGalleryID(ctx)
			if galleryID > 0 {
				if err := perGalleryDB.Write(galleryID, t.Image.Checksum, data); err != nil {
					logger.Errorf("[generator] writing thumbnail to per-gallery database for image %s: %s", path, err.Error())
				}
			} else {
				// Fallback to prefixed mode if no gallery found
				if prefixedDB := mgr.PrefixedThumbnailDB; prefixedDB != nil {
					if err := prefixedDB.Write(t.Image.Checksum, data); err != nil {
						logger.Errorf("[generator] writing thumbnail to prefixed database for image %s: %s", path, err.Error())
					}
				}
			}
		}
	} else if storageType == config.ImageThumbnailsStorageHybrid {
		hybridDB := mgr.HybridThumbnailDB
		if hybridDB != nil {
			// Find the gallery this image belongs to
			galleryID := t.findGalleryID(ctx)
			index := hybridDB.GetDBIndex(&galleryID)
			if err := hybridDB.Write(index, t.Image.Checksum, data); err != nil {
				logger.Errorf("[generator] writing thumbnail to hybrid database for image %s: %s", path, err.Error())
			}
		}
	} else {
		// FILESYSTEM mode
		thumbPath := mgr.Paths.Generated.GetThumbnailPath(t.Image.Checksum, models.DefaultGthumbWidth)
		err = fsutil.WriteFile(thumbPath, data)
		if err != nil {
			logger.Errorf("[generator] writing thumbnail for image %s: %s", path, err.Error())
		}
	}
}

func (t *GenerateImageThumbnailTask) required() bool {
	vf, ok := t.Image.Files.Primary().(models.VisualFile)
	if !ok {
		return false
	}

	if vf.GetHeight() <= models.DefaultGthumbWidth && vf.GetWidth() <= models.DefaultGthumbWidth {
		return false
	}

	if t.Overwrite {
		return true
	}

	mgr := GetInstance()
	storageType := mgr.Config.GetImageThumbnailsStorage()

	if storageType == config.ImageThumbnailsStorageDatabase {
		thumbnailDB := mgr.ThumbnailDB
		if thumbnailDB != nil {
			exists, err := thumbnailDB.Exists(t.Image.Checksum)
			if err == nil && exists {
				return false
			}
		}
		return true
	}

	if storageType == config.ImageThumbnailsStoragePrefixed {
		prefixedDB := mgr.PrefixedThumbnailDB
		if prefixedDB != nil {
			exists, err := prefixedDB.Exists(t.Image.Checksum)
			if err == nil && exists {
				return false
			}
		}
		return true
	}

	if storageType == config.ImageThumbnailsStoragePerGallery {
		perGalleryDB := mgr.PerGalleryThumbnailDB
		if perGalleryDB != nil {
			galleryID := t.findGalleryID(context.Background())
			if galleryID > 0 {
				exists, err := perGalleryDB.Exists(galleryID, t.Image.Checksum)
				if err == nil && exists {
					return false
				}
			} else {
				// Fallback to prefixed mode
				if prefixedDB := mgr.PrefixedThumbnailDB; prefixedDB != nil {
					exists, err := prefixedDB.Exists(t.Image.Checksum)
					if err == nil && exists {
						return false
					}
				}
			}
		}
		return true
	}

	// FILESYSTEM mode
	thumbPath := mgr.Paths.Generated.GetThumbnailPath(t.Image.Checksum, models.DefaultGthumbWidth)
	exists, _ := fsutil.FileExists(thumbPath)

	return !exists
}
