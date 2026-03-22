package manager

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

type MigrateThumbnailsTask struct {
	Overwrite bool
}

func (t *MigrateThumbnailsTask) GetDescription() string {
	return "Migrating image thumbnails to DATABASE_PREFIXED storage"
}

func (t *MigrateThumbnailsTask) migrateThumbnail(image *models.Image) {
	checksum := image.Checksum
	if checksum == "" {
		return
	}

	mgr := GetInstance()
	prefixedDB := mgr.PrefixedThumbnailDB
	if prefixedDB == nil {
		return
	}

	if !t.Overwrite {
		exists, err := prefixedDB.Exists(checksum)
		if err == nil && exists {
			logger.Debugf("Thumbnail already exists in prefixed DB for checksum %s", checksum)
			return
		}
	}

	thumbPath := mgr.Paths.Generated.GetThumbnailPath(checksum, models.DefaultGthumbWidth)
	data, err := os.ReadFile(thumbPath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Debugf("Filesystem thumbnail not found for checksum %s", checksum)
		} else {
			logger.Errorf("Error reading thumbnail file %s: %v", thumbPath, err)
		}
		return
	}

	if err := prefixedDB.Write(checksum, data); err != nil {
		logger.Errorf("Error writing thumbnail to prefixed DB for checksum %s: %v", checksum, err)
		return
	}

	logger.Debugf("Migrated thumbnail for checksum %s", checksum)

	if err := os.Remove(thumbPath); err != nil {
		logger.Warnf("Error deleting filesystem thumbnail %s: %v", thumbPath, err)
	}
}

func (s *Manager) MigrateThumbnails(ctx context.Context, overwrite bool) int {
	task := &MigrateThumbnailsTask{
		Overwrite: overwrite,
	}

	return s.JobManager.Add(ctx, "Migrating thumbnails to DATABASE_PREFIXED...", job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
		mgr := GetInstance()
		prefixedDB := mgr.PrefixedThumbnailDB
		if prefixedDB == nil {
			return fmt.Errorf("PrefixedThumbnailDB not initialized")
		}

		var images []*models.Image
		if err := mgr.Repository.WithTxn(ctx, func(ctx context.Context) error {
			var err error
			images, err = mgr.Repository.Image.All(ctx)
			return err
		}); err != nil {
			return fmt.Errorf("error fetching images: %w", err)
		}

		total := len(images)
		progress.SetTotal(total)
		logger.Infof("Migrating %d image thumbnails to DATABASE_PREFIXED storage", total)

		var wg sync.WaitGroup

		for i, image := range images {
			progress.Increment()

			if job.IsCancelled(ctx) {
				logger.Info("Stopping migration due to user request")
				return nil
			}

			if image == nil || image.Checksum == "" {
				continue
			}

			wg.Add(1)
			go func(img *models.Image) {
				defer wg.Done()
				task.migrateThumbnail(img)
			}(image)

			if (i+1)%10 == 0 {
				wg.Wait()
			}
		}

		wg.Wait()
		logger.Info("Finished migrating thumbnails")
		return nil
	}))
}
