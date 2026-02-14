package manager

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
)

const thumbnailTable = "thumbnails"
const thumbnailChecksumColumn = "checksum"

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
}

// NewThumbnailDB creates a new single thumbnail database
func NewThumbnailDB(dbPath string) (*ThumbnailDB, error) {
	if err := fsutil.EnsureDirAll(filepath.Dir(dbPath)); err != nil {
		return nil, fmt.Errorf("creating thumbnail db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening thumbnail db: %w", err)
	}

	if err := createThumbnailTable(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating thumbnail table: %w", err)
	}

	logger.Infof("Initialized thumbnail database at %s", dbPath)

	return &ThumbnailDB{
		db:     db,
		dbPath: dbPath,
	}, nil
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
	newDB, err := NewThumbnailDB(dbPath)
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

func createThumbnailTable(db *sql.DB) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			%s TEXT PRIMARY KEY,
			data BLOB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_thumbnails_checksum ON %s(%s);
	`, thumbnailTable, thumbnailChecksumColumn, thumbnailTable, thumbnailChecksumColumn)

	_, err := db.Exec(query)
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
