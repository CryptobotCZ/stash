# Image Thumbnail Storage - Summary & Implementation Plan

## Problem Statement

Current filesystem-based thumbnail storage has ~1.3 million files in `generated/thumbnails/` directory. This creates issues:
- Nightmares for backups (millions of small files)
- Filesystem metadata overhead
- Slow incremental operations

## Options Considered

### Option 1: Filesystem (Current Default)
- **How it works:** JPEG files in `generated/thumbnails/XX/XX/<checksum>_<width>.jpg`
- **Pros:**
  - Native OS caching
  - No extra code needed
  - Already implemented
- **Cons:**
  - 1.3M files = backup nightmare
  - Filesystem metadata overhead
  - Slow incremental operations

### Option 2: Single SQLite Database
- **How it works:** All thumbnails in one `generated/thumbnails.db`
- **Pros:**
  - Single file to backup
  - Already partially implemented
- **Cons:**
  - 44GB+ = too large (SQLite recommends <1GB)
  - Slow queries at scale
  - Corruption risk, slow vacuum/backup

### Option 3: SQLite Per Gallery
- **How it works:** One DB per gallery at `generated/thumbnails/gallery_<id>.db`
- **Pros:**
  - Natural grouping (easy to delete when gallery removed)
  - Bounded size (~3MB per gallery)
  - Easy selective backup
- **Cons:**
  - Many DB files if many galleries
  - Connection management overhead

### Option 4: Prefixed Databases (Recommended)
- **How it works:** 256 DBs based on checksum prefix: `00.db`, `01.db`, ... `FF.db`
- **Pros:**
  - Bounded worst case (256 files max)
  - ~150MB per DB (manageable)
  - Fast backup (large sequential writes)
  - Same locality as filesystem
- **Cons:**
  - More complex implementation
  - Slightly more complex lookup

---

## Chosen Approach

**Give users a choice** with three options:

| Config Value | Behavior | Best For |
|--------------|----------|----------|
| `FILESYSTEM` | Current behavior | Users with small libraries |
| `DATABASE` | Single SQLite file | Small libraries, testing |
| `DATABASE_PREFIXED` | 256 DBs based on checksum prefix | **Recommended** - large libraries |

**Default:** `FILESYSTEM` (backward compatible)

---

## Implementation Steps

### 1. Update Config
- **File:** `internal/manager/config/enums.go`
- Add `DATABASE_PREFIXED` to `ImageThumbnailsStorageType` enum
- Values: `FILESYSTEM`, `DATABASE`, `DATABASE_PREFIXED`

### 2. Update GraphQL Schema
- **File:** `graphql/schema/types/config.graphql`
- Add `DATABASE_PREFIXED` to enum
- Update descriptions for all options

### 3. Create Prefixed Database Manager
- **File:** `internal/manager/thumbnail_db.go`
- Add functions to map checksum → DB file (first 2 chars = hex prefix)
- Create/open DB on demand: `00.db`, `01.db`, ... `FF.db`
- Structure: `generated/thumbnails/prefixed/00.db` to `FF.db`

### 4. Update Routes
- **File:** `internal/api/routes_image.go`
- Check `GetImageThumbnailsStorage()`
- Handle all three modes:
  - `FILESYSTEM`: existing file-based code
  - `DATABASE`: existing single-DB code (already implemented)
  - `DATABASE_PREFIXED`: new prefixed DB logic

### 5. Update Tasks
- **File:** `internal/manager/task_generate_image_thumbnail.go`
  - Write to correct storage based on config
- **File:** `internal/manager/task/clean_generated.go`
  - Handle cleanup for all three modes

---

## Key Files to Modify

| File | Changes |
|------|---------|
| `internal/manager/config/enums.go` | Add `DATABASE_PREFIXED` enum value |
| `graphql/schema/types/config.graphql` | Add enum value + descriptions |
| `internal/manager/thumbnail_db.go` | Add prefixed DB management functions |
| `internal/api/routes_image.go` | Handle all 3 modes |
| `internal/manager/task_generate_image_thumbnail.go` | Support all modes |
| `internal/manager/task/clean_generated.go` | Support all modes |

---

## Data Locality Analysis

**Filesystem worst case:**
- 1.3M files across potentially thousands of nested directories
- Each file = separate filesystem entry (inode, metadata)
- Backup = millions of small file operations ❌

**Hash-prefix DB worst case:**
- 256 DB files maximum (00-FF)
- Each DB could be ~150MB (if evenly distributed)
- Backup = 256 large file operations ✅

**Both approaches have the same locality problem** - there's no guarantee images from one gallery share the same prefix. But **hash-prefix is definitively better** because:
- Bounded worst case (256 files max)
- Much faster backup (large sequential writes)
- Less filesystem overhead
- Same locality as filesystem (neither is worse)

---

## Migration Path

- Allow existing filesystem thumbnails to remain
- New thumbnails go to selected storage type
- Users can manually migrate via "Clean Generated" task after switching
