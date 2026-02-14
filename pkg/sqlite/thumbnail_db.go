package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
)

const thumbnailTable = "thumbnails"
const thumbnailChecksumColumn = "checksum"

type ThumbnailDB struct {
	db     *sql.DB
	dbPath string
}

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
