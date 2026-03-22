package manager

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
)

const thumbnailTable = "thumbnails"
const thumbnailChecksumColumn = "checksum"

// ThumbnailDBOptions contains options for creating a thumbnail database
type ThumbnailDBOptions struct {
	// FastMode enables performance optimizations for bulk writes (e.g., migration)
	FastMode bool
	// SkipIndex creates table without checksum index (for faster initial migration)
	SkipIndex bool
}

// ThumbnailDB represents a single thumbnail database
type ThumbnailDB struct {
	db     *sql.DB
	dbPath string
}

// PrefixedThumbnailDB manages multiple thumbnail databases based on checksum prefix
type PrefixedThumbnailDB struct {
	basePath string
	dbs      map[string]*ThumbnailDB
	mu       sync.RWMutex
	fastMode bool
}

// SetFastMode enables performance optimizations for bulk writes
func (p *PrefixedThumbnailDB) SetFastMode(enabled bool) {
	p.fastMode = enabled
}

// IsFastMode returns whether fast mode is enabled
func (p *PrefixedThumbnailDB) IsFastMode() bool {
	return p.fastMode
}

// PerGalleryThumbnailDB manages thumbnail databases per gallery
type PerGalleryThumbnailDB struct {
	basePath string
	dbs      map[int]*ThumbnailDB
	mu       sync.RWMutex
	fastMode bool
}

// SetFastMode enables performance optimizations for bulk writes
func (p *PerGalleryThumbnailDB) SetFastMode(enabled bool) {
	p.fastMode = enabled
}

// IsFastMode returns whether fast mode is enabled
func (p *PerGalleryThumbnailDB) IsFastMode() bool {
	return p.fastMode
}

// HybridThumbnailDB manages thumbnail databases using gallery ID modulo 256
// - Images with gallery: gallery_id % 256 → database file
// - Images without gallery: 255 → default database file
type HybridThumbnailDB struct {
	basePath string
	dbs      map[int]*ThumbnailDB
	mu       sync.RWMutex
	fastMode bool
}

// SetFastMode enables performance optimizations for bulk writes
func (p *HybridThumbnailDB) SetFastMode(enabled bool) {
	p.fastMode = enabled
}

// IsFastMode returns whether fast mode is enabled
func (p *HybridThumbnailDB) IsFastMode() bool {
	return p.fastMode
}

// NewThumbnailDB creates a new single thumbnail database
func NewThumbnailDB(dbPath string) (*ThumbnailDB, error) {
	return NewThumbnailDBWithOptions(dbPath, ThumbnailDBOptions{})
}

// NewThumbnailDBWithOptions creates a new single thumbnail database with custom options
func NewThumbnailDBWithOptions(dbPath string, opts ThumbnailDBOptions) (*ThumbnailDB, error) {
	if err := fsutil.EnsureDirAll(filepath.Dir(dbPath)); err != nil {
		return nil, fmt.Errorf("creating thumbnail db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening thumbnail db: %w", err)
	}

	// Apply speed pragmas if in fast mode
	if opts.FastMode {
		if err := applySpeedPragmas(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("applying speed pragmas: %w", err)
		}
	}

	if err := createThumbnailTable(db, opts.SkipIndex); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating thumbnail table: %w", err)
	}

	logger.Infof("Initialized thumbnail database at %s", dbPath)

	return &ThumbnailDB{
		db:     db,
		dbPath: dbPath,
	}, nil
}

// applySpeedPragmas sets SQLite to fast mode for bulk writes
func applySpeedPragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA synchronous = OFF",
		"PRAGMA journal_mode = WAL",
		"PRAGMA cache_size = -64000",
		"PRAGMA temp_store = MEMORY",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("failed to apply pragma %s: %w", p, err)
		}
	}
	return nil
}

// RestoreDefaultPragmas restores SQLite to default settings after fast mode
func RestoreDefaultPragmas(db *sql.DB) {
	defaultPragmas := []string{
		"PRAGMA synchronous = NORMAL",
		"PRAGMA journal_mode = DELETE",
		"PRAGMA cache_size = -2000",
		"PRAGMA temp_store = DEFAULT",
	}
	for _, p := range defaultPragmas {
		if _, err := db.Exec(p); err != nil {
			logger.Warnf("failed to restore pragma %s: %v", p, err)
		}
	}
}

// NewPrefixedThumbnailDB creates a manager for prefixed thumbnail databases
func NewPrefixedThumbnailDB(basePath string) (*PrefixedThumbnailDB, error) {
	prefixedPath := filepath.Join(basePath, "prefixed")
	if err := fsutil.EnsureDirAll(prefixedPath); err != nil {
		return nil, fmt.Errorf("creating prefixed thumbnail db directory: %w", err)
	}

	logger.Infof("Initialized prefixed thumbnail database at %s", prefixedPath)

	return &PrefixedThumbnailDB{
		basePath: prefixedPath,
		dbs:      make(map[string]*ThumbnailDB),
	}, nil
}

// NewPerGalleryThumbnailDB creates a manager for per-gallery thumbnail databases
func NewPerGalleryThumbnailDB(basePath string) (*PerGalleryThumbnailDB, error) {
	perGalleryPath := filepath.Join(basePath, "per_gallery")
	if err := fsutil.EnsureDirAll(perGalleryPath); err != nil {
		return nil, fmt.Errorf("creating per-gallery thumbnail db directory: %w", err)
	}

	logger.Infof("Initialized per-gallery thumbnail database at %s", perGalleryPath)

	return &PerGalleryThumbnailDB{
		basePath: perGalleryPath,
		dbs:      make(map[int]*ThumbnailDB),
	}, nil
}

// getPrefix returns the first 2 characters of the checksum (hex prefix)
func getPrefix(checksum string) string {
	if len(checksum) >= 2 {
		return strings.ToUpper(checksum[:2])
	}
	return "00"
}

// getDBPath returns the database path for a given checksum prefix
func (p *PrefixedThumbnailDB) getDBPath(prefix string) string {
	return filepath.Join(p.basePath, prefix+".db")
}

// getDB returns or creates the database for the given checksum
func (p *PrefixedThumbnailDB) getDB(checksum string) (*ThumbnailDB, error) {
	prefix := getPrefix(checksum)

	p.mu.RLock()
	db, exists := p.dbs[prefix]
	p.mu.RUnlock()

	if exists {
		return db, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if db, exists := p.dbs[prefix]; exists {
		return db, nil
	}

	dbPath := p.getDBPath(prefix)
	opts := ThumbnailDBOptions{
		FastMode:  p.fastMode,
		SkipIndex: p.fastMode, // Skip index during fast mode migration
	}
	newDB, err := NewThumbnailDBWithOptions(dbPath, opts)
	if err != nil {
		return nil, fmt.Errorf("creating prefixed thumbnail db for %s: %w", prefix, err)
	}

	p.dbs[prefix] = newDB
	return newDB, nil
}

// Read reads a thumbnail from the prefixed databases
func (p *PrefixedThumbnailDB) Read(checksum string) ([]byte, error) {
	db, err := p.getDB(checksum)
	if err != nil {
		return nil, err
	}
	return db.Read(checksum)
}

// Write writes a thumbnail to the prefixed databases
func (p *PrefixedThumbnailDB) Write(checksum string, data []byte) error {
	db, err := p.getDB(checksum)
	if err != nil {
		return err
	}
	return db.Write(checksum, data)
}

// Delete deletes a thumbnail from the prefixed databases
func (p *PrefixedThumbnailDB) Delete(checksum string) error {
	db, err := p.getDB(checksum)
	if err != nil {
		return err
	}
	return db.Delete(checksum)
}

// Exists checks if a thumbnail exists in the prefixed databases
func (p *PrefixedThumbnailDB) Exists(checksum string) (bool, error) {
	db, err := p.getDB(checksum)
	if err != nil {
		return false, err
	}
	return db.Exists(checksum)
}

// GetAllChecksums returns all checksums from all prefixed databases
func (p *PrefixedThumbnailDB) GetAllChecksums(ctx context.Context) ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var allChecksums []string

	for prefix, db := range p.dbs {
		checksums, err := db.GetAllChecksums(ctx)
		if err != nil {
			logger.Warnf("Error getting checksums from prefix %s: %v", prefix, err)
			continue
		}
		allChecksums = append(allChecksums, checksums...)
	}

	return allChecksums, nil
}

// GetAllChecksumsAllDBs returns checksums from all database files (including unopened ones)
func (p *PrefixedThumbnailDB) GetAllChecksumsAllDBs(ctx context.Context) ([]string, error) {
	var allChecksums []string

	// Read all .db files in the prefixed directory
	entries, err := os.ReadDir(p.basePath)
	if err != nil {
		return nil, fmt.Errorf("reading prefixed directory: %w", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}

		dbPath := filepath.Join(p.basePath, entry.Name())
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			logger.Warnf("Error opening database %s: %v", dbPath, err)
			continue
		}

		query := fmt.Sprintf("SELECT %s FROM %s", thumbnailChecksumColumn, thumbnailTable)
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			db.Close()
			logger.Warnf("Error querying database %s: %v", dbPath, err)
			continue
		}

		for rows.Next() {
			var checksum string
			if err := rows.Scan(&checksum); err != nil {
				rows.Close()
				db.Close()
				logger.Warnf("Error scanning checksum: %v", err)
				continue
			}
			allChecksums = append(allChecksums, checksum)
		}
		rows.Close()
		db.Close()
	}

	return allChecksums, nil
}

// Close closes all prefixed databases
func (p *PrefixedThumbnailDB) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errors []string
	for prefix, db := range p.dbs {
		if err := db.Close(); err != nil {
			errors = append(errors, fmt.Sprintf("error closing db %s: %w", prefix, err))
		}
	}
	p.dbs = make(map[string]*ThumbnailDB)

	if len(errors) > 0 {
		return fmt.Errorf("errors closing databases: %s", strings.Join(errors, ", "))
	}
	return nil
}

// PerGalleryThumbnailDB methods

// getDBPath returns the database path for a given gallery ID
func (p *PerGalleryThumbnailDB) getDBPath(galleryID int) string {
	return filepath.Join(p.basePath, fmt.Sprintf("gallery_%d.db", galleryID))
}

// getDB returns or creates the database for the given gallery ID
func (p *PerGalleryThumbnailDB) getDB(galleryID int) (*ThumbnailDB, error) {
	p.mu.RLock()
	db, exists := p.dbs[galleryID]
	p.mu.RUnlock()

	if exists {
		return db, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if db, exists := p.dbs[galleryID]; exists {
		return db, nil
	}

	dbPath := p.getDBPath(galleryID)
	newDB, err := NewThumbnailDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("creating per-gallery thumbnail db for gallery %d: %w", galleryID, err)
	}

	p.dbs[galleryID] = newDB
	return newDB, nil
}

// Read reads a thumbnail from the per-gallery databases
func (p *PerGalleryThumbnailDB) Read(galleryID int, checksum string) ([]byte, error) {
	db, err := p.getDB(galleryID)
	if err != nil {
		return nil, err
	}
	return db.Read(checksum)
}

// Write writes a thumbnail to the per-gallery databases
func (p *PerGalleryThumbnailDB) Write(galleryID int, checksum string, data []byte) error {
	db, err := p.getDB(galleryID)
	if err != nil {
		return err
	}
	return db.Write(checksum, data)
}

// Delete deletes a thumbnail from the per-gallery databases
func (p *PerGalleryThumbnailDB) Delete(galleryID int, checksum string) error {
	db, err := p.getDB(galleryID)
	if err != nil {
		return err
	}
	return db.Delete(checksum)
}

// Exists checks if a thumbnail exists in the per-gallery databases
func (p *PerGalleryThumbnailDB) Exists(galleryID int, checksum string) (bool, error) {
	db, err := p.getDB(galleryID)
	if err != nil {
		return false, err
	}
	return db.Exists(checksum)
}

// GetAllChecksums returns all checksums from all per-gallery databases
func (p *PerGalleryThumbnailDB) GetAllChecksums(ctx context.Context) ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var allChecksums []string

	for galleryID, db := range p.dbs {
		checksums, err := db.GetAllChecksums(ctx)
		if err != nil {
			logger.Warnf("Error getting checksums from gallery %d: %v", galleryID, err)
			continue
		}
		allChecksums = append(allChecksums, checksums...)
	}

	return allChecksums, nil
}

// GetAllChecksumsAllDBs returns checksums from all database files (including unopened ones)
func (p *PerGalleryThumbnailDB) GetAllChecksumsAllDBs(ctx context.Context) ([]string, error) {
	var allChecksums []string

	// Read all .db files in the per-gallery directory
	entries, err := os.ReadDir(p.basePath)
	if err != nil {
		return nil, fmt.Errorf("reading per-gallery directory: %w", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}

		// Extract gallery ID from filename (gallery_<id>.db)
		name := strings.TrimSuffix(entry.Name(), ".db")
		if !strings.HasPrefix(name, "gallery_") {
			continue
		}
		galleryIDStr := strings.TrimPrefix(name, "gallery_")
		_, err := strconv.Atoi(galleryIDStr)
		if err != nil {
			continue // Not a gallery database
		}

		dbPath := filepath.Join(p.basePath, entry.Name())
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			logger.Warnf("Error opening database %s: %v", dbPath, err)
			continue
		}

		query := fmt.Sprintf("SELECT %s FROM %s", thumbnailChecksumColumn, thumbnailTable)
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			db.Close()
			logger.Warnf("Error querying database %s: %v", dbPath, err)
			continue
		}

		for rows.Next() {
			var checksum string
			if err := rows.Scan(&checksum); err != nil {
				rows.Close()
				db.Close()
				logger.Warnf("Error scanning checksum: %v", err)
				continue
			}
			allChecksums = append(allChecksums, checksum)
		}
		rows.Close()
		db.Close()
	}

	return allChecksums, nil
}

// Close closes all per-gallery databases
func (p *PerGalleryThumbnailDB) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errors []string
	for galleryID, db := range p.dbs {
		if err := db.Close(); err != nil {
			errors = append(errors, fmt.Sprintf("error closing db for gallery %d: %w", galleryID, err))
		}
	}
	p.dbs = make(map[int]*ThumbnailDB)

	if len(errors) > 0 {
		return fmt.Errorf("errors closing databases: %s", strings.Join(errors, ", "))
	}
	return nil
}

// HybridThumbnailDB methods

// NewHybridThumbnailDB creates a manager for hybrid thumbnail databases
func NewHybridThumbnailDB(basePath string) (*HybridThumbnailDB, error) {
	hybridPath := filepath.Join(basePath, "hybrid")
	if err := fsutil.EnsureDirAll(hybridPath); err != nil {
		return nil, fmt.Errorf("creating hybrid thumbnail db directory: %w", err)
	}

	logger.Infof("Initialized hybrid thumbnail database at %s", hybridPath)

	return &HybridThumbnailDB{
		basePath: hybridPath,
		dbs:      make(map[int]*ThumbnailDB),
	}, nil
}

// getDBPath returns the database path for a given index (gallery_id % 256 or 255 for no gallery)
func (p *HybridThumbnailDB) getDBPath(index int) string {
	return filepath.Join(p.basePath, fmt.Sprintf("%02d.db", index))
}

// getDB returns or creates the database for the given index
func (p *HybridThumbnailDB) getDB(index int) (*ThumbnailDB, error) {
	p.mu.RLock()
	db, exists := p.dbs[index]
	p.mu.RUnlock()

	if exists {
		return db, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if db, exists := p.dbs[index]; exists {
		return db, nil
	}

	dbPath := p.getDBPath(index)
	opts := ThumbnailDBOptions{
		FastMode:  p.fastMode,
		SkipIndex: p.fastMode,
	}
	newDB, err := NewThumbnailDBWithOptions(dbPath, opts)
	if err != nil {
		return nil, fmt.Errorf("creating hybrid thumbnail db for index %d: %w", index, err)
	}

	p.dbs[index] = newDB
	return newDB, nil
}

// GetDBIndex returns the database index for an image based on its gallery
// Returns gallery_id % 256 if image has gallery, otherwise 255 (default DB)
func (p *HybridThumbnailDB) GetDBIndex(galleryID *int) int {
	if galleryID == nil || *galleryID == 0 {
		return 255 // Default DB for images without gallery
	}
	return *galleryID % 256
}

// Read reads a thumbnail from the hybrid databases
func (p *HybridThumbnailDB) Read(index int, checksum string) ([]byte, error) {
	db, err := p.getDB(index)
	if err != nil {
		return nil, err
	}
	return db.Read(checksum)
}

// Write writes a thumbnail to the hybrid databases
func (p *HybridThumbnailDB) Write(index int, checksum string, data []byte) error {
	db, err := p.getDB(index)
	if err != nil {
		return err
	}
	return db.Write(checksum, data)
}

// Delete deletes a thumbnail from the hybrid databases
func (p *HybridThumbnailDB) Delete(index int, checksum string) error {
	db, err := p.getDB(index)
	if err != nil {
		return err
	}
	return db.Delete(checksum)
}

// Exists checks if a thumbnail exists in the hybrid databases
func (p *HybridThumbnailDB) Exists(index int, checksum string) (bool, error) {
	db, err := p.getDB(index)
	if err != nil {
		return false, err
	}
	return db.Exists(checksum)
}

// GetAllChecksums returns checksums from all opened databases
func (p *HybridThumbnailDB) GetAllChecksums(ctx context.Context) ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var allChecksums []string

	for index, db := range p.dbs {
		checksums, err := db.GetAllChecksums(ctx)
		if err != nil {
			logger.Warnf("Error getting checksums from index %d: %v", index, err)
			continue
		}
		allChecksums = append(allChecksums, checksums...)
	}

	return allChecksums, nil
}

// GetAllChecksumsAllDBs returns checksums from all database files (including unopened ones)
func (p *HybridThumbnailDB) GetAllChecksumsAllDBs(ctx context.Context) ([]string, error) {
	var allChecksums []string

	entries, err := os.ReadDir(p.basePath)
	if err != nil {
		return nil, fmt.Errorf("reading hybrid directory: %w", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".db")
		if _, err := strconv.Atoi(name); err != nil {
			continue
		}

		dbPath := filepath.Join(p.basePath, entry.Name())
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			logger.Warnf("Error opening database %s: %v", dbPath, err)
			continue
		}

		query := fmt.Sprintf("SELECT %s FROM %s", thumbnailChecksumColumn, thumbnailTable)
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			db.Close()
			logger.Warnf("Error querying database %s: %v", dbPath, err)
			continue
		}

		for rows.Next() {
			var checksum string
			if err := rows.Scan(&checksum); err != nil {
				rows.Close()
				db.Close()
				logger.Warnf("Error scanning checksum: %v", err)
				continue
			}
			allChecksums = append(allChecksums, checksum)
		}
		rows.Close()
		db.Close()
	}

	return allChecksums, nil
}

// Close closes all hybrid databases
func (p *HybridThumbnailDB) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errors []string
	for index, db := range p.dbs {
		if err := db.Close(); err != nil {
			errors = append(errors, fmt.Sprintf("error closing db for index %d: %w", index, err))
		}
	}
	p.dbs = make(map[int]*ThumbnailDB)

	if len(errors) > 0 {
		return fmt.Errorf("errors closing databases: %s", strings.Join(errors, ", "))
	}
	return nil
}

func createThumbnailTable(db *sql.DB, skipIndex bool) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			%s TEXT PRIMARY KEY,
			data BLOB NOT NULL
		);
	`, thumbnailTable, thumbnailChecksumColumn)

	_, err := db.Exec(query)
	if err != nil {
		return err
	}

	// Add index after table creation if not skipping
	if !skipIndex {
		indexQuery := fmt.Sprintf(`
			CREATE INDEX IF NOT EXISTS idx_thumbnails_checksum ON %s(%s);
		`, thumbnailTable, thumbnailChecksumColumn)
		_, err = db.Exec(indexQuery)
	}

	return err
}

func (t *ThumbnailDB) Read(checksum string) ([]byte, error) {
	query := fmt.Sprintf("SELECT data FROM %s WHERE %s = ?", thumbnailTable, thumbnailChecksumColumn)

	var data []byte
	err := t.db.QueryRow(query, checksum).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("thumbnail not found for checksum: %s", checksum)
	}
	if err != nil {
		return nil, fmt.Errorf("reading thumbnail: %w", err)
	}

	return data, nil
}

func (t *ThumbnailDB) Write(checksum string, data []byte) error {
	query := fmt.Sprintf(`
		INSERT OR REPLACE INTO %s (%s, data) VALUES (?, ?)
	`, thumbnailTable, thumbnailChecksumColumn)

	_, err := t.db.Exec(query, checksum, data)
	if err != nil {
		return fmt.Errorf("writing thumbnail: %w", err)
	}

	return nil
}

func (t *ThumbnailDB) Delete(checksum string) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE %s = ?", thumbnailTable, thumbnailChecksumColumn)

	_, err := t.db.Exec(query, checksum)
	if err != nil {
		return fmt.Errorf("deleting thumbnail: %w", err)
	}

	return nil
}

func (t *ThumbnailDB) Exists(checksum string) (bool, error) {
	query := fmt.Sprintf("SELECT 1 FROM %s WHERE %s = ? LIMIT 1", thumbnailTable, thumbnailChecksumColumn)

	var exists int
	err := t.db.QueryRow(query, checksum).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking thumbnail existence: %w", err)
	}

	return true, nil
}

func (t *ThumbnailDB) GetAllChecksums(ctx context.Context) ([]string, error) {
	query := fmt.Sprintf("SELECT %s FROM %s", thumbnailChecksumColumn, thumbnailTable)

	rows, err := t.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("getting all checksums: %w", err)
	}
	defer rows.Close()

	var checksums []string
	for rows.Next() {
		var checksum string
		if err := rows.Scan(&checksum); err != nil {
			return nil, fmt.Errorf("scanning checksum: %w", err)
		}
		checksums = append(checksums, checksum)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating checksums: %w", err)
	}

	return checksums, nil
}

func (t *ThumbnailDB) Close() error {
	return t.db.Close()
}
