# Image Thumbnails SQLite Storage

This document describes the implementation of storing image thumbnails in a separate SQLite database instead of the filesystem.

## Overview

- **Motivation:** Disks perform better with larger single files vs thousands of small files
- **Scale:** ~100,000 thumbnails (thousands of galleries, ~100 thumbs per gallery)
- **Design:** Separate SQLite database (ephemeral, can be rebuilt from source)

## Key Design Decisions

1. **Separate SQLite database** for thumbnails (not the main database)
2. Located in `generated/thumbnails.db`
3. Ephemeral - can be deleted and rebuilt from source images
4. Keep FILESYSTEM as default for backward compatibility
5. No migration of existing thumbnails

## Implementation

### 1. Configuration

**File:** `internal/manager/config/enums.go`

Add new enum:
```go
type ImageThumbnailsStorageType string

const (
    ImageThumbnailsStorageFilesystem ImageThumbnailsStorageType = "FILESYSTEM"
    ImageThumbnailsStorageDatabase   ImageThumbnailsStorageType = "DATABASE"
)
```

**File:** `internal/manager/config/config.go`

Add config key and getter:
```go
ImageThumbnailsStorage = "image_thumbnails_storage"

func (i *Config) GetImageThumbnailsStorage() ImageThumbnailsStorageType
```

Default: FILESYSTEM (for backward compatibility)

### 2. Thumbnail Database Module

**New file:** `pkg/sqlite/thumbnail_db.go`

Simple SQLite database with single table:
```sql
CREATE TABLE IF NOT EXISTS thumbnails (
    checksum TEXT PRIMARY KEY,
    data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_thumbnails_checksum ON thumbnails(checksum);
```

Operations:
- `Read(checksum string) ([]byte, error)` - Get thumbnail by checksum
- `Write(checksum string, data []byte) error` - Store thumbnail
- `Delete(checksum string) error` - Remove thumbnail
- `GetAllChecksums() ([]string, error)` - Get all stored checksums (for cleanup)

### 3. Manager Integration

**File:** `internal/manager/manager.go`

- Initialize `ThumbnailDB` in manager initialization
- Path: `filepath.Join(generatedPath, "thumbnails.db")`
- Store in manager struct for access by routes and tasks

### 4. Thumbnail Serving

**File:** `internal/api/routes_image.go`

Modify `serveThumbnail()` to check config:
- **DATABASE mode:**
  - Try reading from `ThumbnailDB` first
  - If not found, generate and write to `ThumbnailDB`
- **FILESYSTEM mode:**
  - Use existing filesystem logic (current behavior)

### 5. Generation Task

**File:** `internal/manager/task_generate_image_thumbnail.go`

When `GetImageThumbnailsStorage() == DATABASE`:
- Write to `ThumbnailDB` instead of filesystem
- Use existing checksum-based lookup

### 6. Cleanup Task

**File:** `internal/manager/task/clean_thumbnails.go`

New cleanup task to remove orphaned thumbnails:
- Get all checksums from `ThumbnailDB`
- Compare with checksums of existing images in database
- Delete thumbnails that no longer have corresponding images

Triggered via existing cleanup job system.

## Storage Comparison

| Aspect | Generated (FILESYSTEM) | New (DATABASE) |
|--------|----------------------|----------------|
| Location | `generated/thumbnails/` | `generated/thumbnails.db` |
| Format | JPEG files | SQLite blob |
| Backup | Manual | Single file backup |
| Performance | Many file operations | Single DB query |

## Future Improvements

- Consider WAL mode for better concurrent read performance
- Add compression for thumbnail data if needed
- Consider cache size configuration for SQLite
