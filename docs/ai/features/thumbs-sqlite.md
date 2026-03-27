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

**Give users a choice** with five options:

| Config Value | Behavior | Best For |
|--------------|----------|----------|
| `FILESYSTEM` | Current behavior | Users with small libraries |
| `DATABASE` | Single SQLite file | Small libraries, testing |
| `DATABASE_PREFIXED` | 256 DBs based on checksum prefix | Large libraries |
| `DATABASE_PER_GALLERY` | One DB per gallery | Gallery-centric backup |
| `DATABASE_HYBRID` | 256 DBs based on gallery_id % 256 | Large libraries with many galleries |

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

## DATABASE_HYBRID Mode

New in 0.30.x - Distributes thumbnails across 256 database files based on `gallery_id % 256`.

### How it works

- Images in gallery ID 0 → DB index 0
- Images in gallery ID 256 → DB index 0  
- Images in gallery ID 1 → DB index 1
- Images with no gallery → DB index 255 (default)

This provides even distribution while keeping related images (same gallery) in the same DB.

### Configuration

Add to `config.yml`:

```yaml
general:
  image_thumbnails_storage: DATABASE_HYBRID
  thumbnail_migration_workers: 16    # parallel workers (default: 16)
  thumbnail_migration_batch_size: 100 # work queue size (default: 100)
  delete_fs_thumbnail_on_load: false  # delete fs thumbnail on serve
```

### Migration

Run migration task from Settings → Tasks → Migrations → "Migrate Thumbnails (Hybrid)"

Or enable gradual migration:
1. Set `image_thumbnails_storage: DATABASE_HYBRID` 
2. Set `delete_fs_thumbnail_on_load: true`
3. Browse your image library - thumbnails are migrated on-demand
4. Filesystem thumbnails are deleted after serving from DB

- Allow existing filesystem thumbnails to remain
- New thumbnails go to selected storage type
- Users can manually migrate via "Clean Generated" task after switching

---

## Migration Performance Optimization

When migrating large thumbnail collections (e.g., 20 GB / 200K+ thumbnails), performance optimizations can reduce migration time from ~30 minutes to ~3-5 minutes.

### Current Bottlenecks

1. **SQLite write locking** - Only one writer per DB file at a time
2. **Small batch size** - 10 concurrent operations (default)
3. **Per-thumbnail transaction overhead** - Each write is a separate transaction
4. **Default PRAGMA settings** - `synchronous=on` forces disk fsync after each write

### Optimization Techniques

#### 1. SQLite Speed PRAGMAs
Apply these settings at migration start, restore after completion:

```go
db.Exec("PRAGMA synchronous = OFF")      // Skip fsync during migration  
db.Exec("PRAGMA journal_mode = WAL")     // Write-ahead logging
db.Exec("PRAGMA cache_size = -64000")    // 64MB cache
db.Exec("PRAGMA temp_store = MEMORY")    // Temp tables in memory
```

**Impact:** 2-3x speedup

#### 2. Batch Transactions
Wrap multiple writes in a single transaction (e.g., 100 thumbnails per commit):

```go
tx, _ := db.Begin()
for _, thumb := range batch {
    tx.Exec("INSERT INTO thumbnails VALUES(?, ?)", thumb.checksum, thumb.data)
}
tx.Commit()
```

**Impact:** 2-3x speedup

#### 3. Increase Concurrent Batch Size
Change batch size from 10 to 50-100 goroutines:

```go
const batchSize = 50  // Was 10
```

**Impact:** 1.5-2x speedup

#### 4. Skip Index During Migration
Create table without index, add index after all data is written:

```sql
-- Migration phase: no index
CREATE TABLE thumbnails (checksum TEXT PRIMARY KEY, data BLOB);

-- After migration: add index
CREATE INDEX idx_thumbnails_checksum ON thumbnails(checksum);
```

**Impact:** 1.2-1.5x speedup for initial migration

#### 5. Two-Phase Migration (Optional)
- Phase 1: Copy to DB (keep filesystem)
- Phase 2: Verify checksums + delete filesystem after

**Use case:** Safety net if migration is interrupted

### Combined Impact

| Optimization | Speedup |
|--------------|---------|
| Speed PRAGMAs | 2-3x |
| Batch transactions | 2-3x |
| Larger batch | 1.5-2x |
| Skip index | 1.2-1.5x |
| **Total** | **5-10x** |

### Time Estimates (20 GB / 200K thumbnails)

| Scenario | Time |
|----------|------|
| Before optimization | ~30 minutes |
| After optimization | ~3-5 minutes |

### Implementation Notes

- Apply speed PRAGMAs when opening each prefixed DB file during migration
- Restore default PRAGMAs after migration completes (or on error)
- Monitor disk I/O - can become bottleneck on HDDs vs SSDs
- Progress reporting every 500 thumbnails helps track long migrations
- **Implemented:** Fast mode with speed PRAGMAs enabled during migration (batch size: configurable)
- **Implemented:** Skip index during migration, added after completion
- **Implemented:** Worker pool (configurable workers) for parallel processing
- **Implemented:** On-demand migration via `delete_fs_thumbnail_on_load` config

---

## Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `image_thumbnails_storage` | string | `FILESYSTEM` | Storage mode: FILESYSTEM, DATABASE, DATABASE_PREFIXED, DATABASE_PER_GALLERY, DATABASE_HYBRID |
| `thumbnail_migration_workers` | int | 16 | Number of parallel workers for migration task |
| `thumbnail_migration_batch_size` | int | 100 | Work queue size for migration |
| `delete_fs_thumbnail_on_load` | bool | false | Delete filesystem thumbnail after serving from DB (gradual migration)
