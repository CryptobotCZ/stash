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

type MigrateThumbnailsToHybridTask struct {
	Overwrite bool
}

func (t *MigrateThumbnailsToHybridTask) GetDescription() string {
	return "Migrating image thumbnails to DATABASE_HYBRID storage"
}

type thumbnailWork struct {
	image     *models.Image
	checksum  string
	galleryID *int
}

func (s *Manager) MigrateThumbnailsToHybrid(ctx context.Context, overwrite bool) int {
	migrationTask := &MigrateThumbnailsToHybridTask{
		Overwrite: overwrite,
	}

	return s.JobManager.Add(ctx, "Migrating thumbnails to DATABASE_HYBRID...", job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
		mgr := GetInstance()
		hybridDB := mgr.HybridThumbnailDB
		if hybridDB == nil {
			return fmt.Errorf("HybridThumbnailDB not initialized")
		}

		logger.Infof("Enabling fast mode for migration...")
		hybridDB.SetFastMode(true)

		var images []*models.Image
		if err := mgr.Repository.WithTxn(ctx, func(ctx context.Context) error {
			var err error
			images, err = mgr.Repository.Image.All(ctx)
			if err != nil {
				return err
			}

			logger.Infof("Loading gallery relationships for %d images...", len(images))
			for _, img := range images {
				if err := img.LoadGalleryIDs(ctx, mgr.Repository.Image); err != nil {
					logger.Warnf("Error loading gallery IDs for image %s: %v", img.Checksum, err)
				}
			}

			return nil
		}); err != nil {
			hybridDB.SetFastMode(false)
			return fmt.Errorf("error fetching images: %w", err)
		}

		total := len(images)
		progress.SetTotal(total)

		// Get config values
		numWorkers := mgr.Config.GetThumbnailMigrationWorkers()
		batchSize := mgr.Config.GetThumbnailMigrationBatchSize()
		if numWorkers <= 0 {
			numWorkers = 16
		}
		if batchSize <= 0 {
			batchSize = 100
		}

		logger.Infof("Migrating %d image thumbnails to DATABASE_HYBRID storage (workers: %d, batch: %d)", total, numWorkers, batchSize)

		// Create work channel
		workChan := make(chan thumbnailWork, batchSize)
		var wg sync.WaitGroup

		// Start worker pool
		migrated := 0
		skipped := 0
		var mu sync.Mutex

		for i := 0; i < numWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for work := range workChan {
					result := migrationTask.processThumbnail(work, mgr, hybridDB)
					mu.Lock()
					if result {
						migrated++
					} else {
						skipped++
					}
					mu.Unlock()
				}
			}()
		}

		// Send work to channel
		go func() {
			defer close(workChan)
			for i, image := range images {
				progress.Increment()

				if job.IsCancelled(ctx) {
					break
				}

				if image == nil || image.Checksum == "" {
					mu.Lock()
					skipped++
					mu.Unlock()
					continue
				}

				var galleryID *int
				if image.GalleryIDs.Loaded() && len(image.GalleryIDs.List()) > 0 {
					id := image.GalleryIDs.List()[0]
					galleryID = &id
				}

				workChan <- thumbnailWork{
					image:     image,
					checksum:  image.Checksum,
					galleryID: galleryID,
				}

				// Log progress periodically
				if (i+1)%500 == 0 {
					logger.Infof("Migration progress: %d/%d (migrated: %d, skipped: %d)", i+1, total, migrated, skipped)
				}
			}
		}()

		// Wait for all workers to complete
		wg.Wait()

		logger.Infof("Restoring default settings...")
		hybridDB.SetFastMode(false)

		logger.Infof("Finished migrating thumbnails: %d migrated, %d skipped", migrated, skipped)
		return nil
	}))
}

func (t *MigrateThumbnailsToHybridTask) processThumbnail(work thumbnailWork, mgr *Manager, hybridDB *HybridThumbnailDB) bool {
	checksum := work.checksum
	if checksum == "" {
		return false
	}

	index := hybridDB.GetDBIndex(work.galleryID)

	if !t.Overwrite {
		exists, err := hybridDB.Exists(index, checksum)
		if err == nil && exists {
			logger.Debugf("Thumbnail already exists in hybrid DB for checksum %s", checksum)
			return false
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
		return false
	}

	if err := hybridDB.Write(index, checksum, data); err != nil {
		logger.Errorf("Error writing thumbnail to hybrid DB for checksum %s: %v", checksum, err)
		return false
	}

	logger.Debugf("Migrated thumbnail for checksum %s to hybrid DB index %d", checksum, index)

	if err := os.Remove(thumbPath); err != nil {
		logger.Warnf("Error deleting filesystem thumbnail %s: %v", thumbPath, err)
	}

	return true
}