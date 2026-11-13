// Package catalog is the SQLite index of everything in the store: files,
// their previous versions, deleted files, and per-source sync state.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, keeps builds cgo-free
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS sources (
	id         TEXT PRIMARY KEY,
	cursor     TEXT NOT NULL DEFAULT '',
	last_sync  INTEGER,
	last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS files (
	id              INTEGER PRIMARY KEY,
	source          TEXT NOT NULL,
	source_item_id  TEXT NOT NULL,
	path            TEXT NOT NULL,
	dir             TEXT NOT NULL,
	size            INTEGER NOT NULL,
	modified        INTEGER NOT NULL,
	sha256          TEXT NOT NULL,
	source_checksum TEXT NOT NULL DEFAULT '',
	deleted_at      INTEGER,
	trash_path      TEXT NOT NULL DEFAULT '',
	UNIQUE (source, source_item_id)
);
CREATE INDEX IF NOT EXISTS files_dir ON files (source, dir) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS files_path ON files (source, path);
CREATE INDEX IF NOT EXISTS files_deleted ON files (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS versions (
	id          INTEGER PRIMARY KEY,
	file_id     INTEGER NOT NULL REFERENCES files (id) ON DELETE CASCADE,
	size        INTEGER NOT NULL,
	sha256      TEXT NOT NULL,
	modified    INTEGER NOT NULL,
	stored_path TEXT NOT NULL,
	created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS versions_file ON versions (file_id, created_at);
`

// Catalog wraps the SQLite database.
type Catalog struct {
	db *sql.DB
}

// Open opens (or creates) the catalog database at path.
func Open(dbPath string) (*Catalog, error) {
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Catalog{db: db}, nil
}

// Close closes the database.
func (c *Catalog) Close() error { return c.db.Close() }

// File is a file known to the catalog.
type File struct {
	ID             int64
	Source         string
	SourceItemID   string
	Path           string
	Size           int64
	Modified       time.Time
	SHA256         string
	SourceChecksum string
	DeletedAt      *time.Time
	TrashPath      string
}

// Name returns the file's base name.
func (f File) Name() string { return path.Base(f.Path) }

// Version is a previous version of a file.
type Version struct {
	ID         int64
	FileID     int64
	Size       int64
	SHA256     string
	Modified   time.Time
	StoredPath string
	CreatedAt  time.Time
}

// SourceState is the sync state of a source.
type SourceState struct {
	ID        string
	Cursor    string
	LastSync  *time.Time
	LastError string
}

func ns(t time.Time) int64 { return t.UnixNano() }

func fromNS(v int64) time.Time { return time.Unix(0, v).UTC() }

func fromNullNS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromNS(v.Int64)
	return &t
}

func dirOf(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

const fileCols = `id, source, source_item_id, path, size, modified, sha256, source_checksum, deleted_at, trash_path`

type scanner interface{ Scan(...any) error }

func scanFile(r scanner) (File, error) {
	var f File
	var mod int64
	var del sql.NullInt64
	err := r.Scan(&f.ID, &f.Source, &f.SourceItemID, &f.Path, &f.Size, &mod, &f.SHA256, &f.SourceChecksum, &del, &f.TrashPath)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	f.Modified = fromNS(mod)
	f.DeletedAt = fromNullNS(del)
	return f, err
}

// GetFile returns a file by catalog ID.
func (c *Catalog) GetFile(ctx context.Context, id int64) (File, error) {
	return scanFile(c.db.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE id = ?`, id))
}

// FileBySourceItem returns a file by its source and source item ID.
func (c *Catalog) FileBySourceItem(ctx context.Context, source, itemID string) (File, error) {
	return scanFile(c.db.QueryRowContext(ctx,
		`SELECT `+fileCols+` FROM files WHERE source = ? AND source_item_id = ?`, source, itemID))
}

// LiveFileByPath returns the non-deleted file at path in a source.
func (c *Catalog) LiveFileByPath(ctx context.Context, source, p string) (File, error) {
	return scanFile(c.db.QueryRowContext(ctx,
		`SELECT `+fileCols+` FROM files WHERE source = ? AND path = ? AND deleted_at IS NULL`, source, p))
}

// PutFile inserts or updates the current state of a file and, when
// previousVersionPath is set, records the replaced content as a version.
// It returns the stored file.
func (c *Catalog) PutFile(ctx context.Context, f File, previousVersionPath string, now time.Time) (File, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback()

	old, err := scanFile(tx.QueryRowContext(ctx,
		`SELECT `+fileCols+` FROM files WHERE source = ? AND source_item_id = ?`, f.Source, f.SourceItemID))
	switch {
	case errors.Is(err, ErrNotFound):
		res, err := tx.ExecContext(ctx, `INSERT INTO files
			(source, source_item_id, path, dir, size, modified, sha256, source_checksum)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			f.Source, f.SourceItemID, f.Path, dirOf(f.Path), f.Size, ns(f.Modified), f.SHA256, f.SourceChecksum)
		if err != nil {
			return File{}, err
		}
		if f.ID, err = res.LastInsertId(); err != nil {
			return File{}, err
		}
	case err != nil:
		return File{}, err
	default:
		f.ID = old.ID
		if previousVersionPath != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO versions
				(file_id, size, sha256, modified, stored_path, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				old.ID, old.Size, old.SHA256, ns(old.Modified), previousVersionPath, ns(now)); err != nil {
				return File{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET path = ?, dir = ?, size = ?, modified = ?,
			sha256 = ?, source_checksum = ?, deleted_at = NULL, trash_path = '' WHERE id = ?`,
			f.Path, dirOf(f.Path), f.Size, ns(f.Modified), f.SHA256, f.SourceChecksum, f.ID); err != nil {
			return File{}, err
		}
	}
	f.DeletedAt, f.TrashPath = nil, ""
	return f, tx.Commit()
}

// MoveFile records a rename without a content change.
func (c *Catalog) MoveFile(ctx context.Context, id int64, newPath string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE files SET path = ?, dir = ? WHERE id = ?`, newPath, dirOf(newPath), id)
	return err
}

// MarkDeleted records that a file was moved to the trash.
func (c *Catalog) MarkDeleted(ctx context.Context, id int64, trashPath string, at time.Time) error {
	_, err := c.db.ExecContext(ctx, `UPDATE files SET deleted_at = ?, trash_path = ? WHERE id = ?`, ns(at), trashPath, id)
	return err
}

// Undelete records that a trashed file was restored, possibly at a new path.
func (c *Catalog) Undelete(ctx context.Context, id int64, p string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE files SET deleted_at = NULL, trash_path = '', path = ?, dir = ? WHERE id = ?`,
		p, dirOf(p), id)
	return err
}

// Entry is one item in a directory listing: either a file or a sub-directory.
type Entry struct {
	Name  string
	IsDir bool
	File  *File // nil for directories
}

// ListDir lists the live files and sub-directories directly under dir
// ("" is the source root). Directories come first, then files, by name.
func (c *Catalog) ListDir(ctx context.Context, source, dir string) ([]Entry, error) {
	dir = strings.Trim(dir, "/")
	// Sub-directories: distinct next path segment of live files below dir.
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	// SQLite substr counts characters, not bytes.
	start := utf8.RuneCountInString(prefix) + 1
	// '/' is 0x2F and '0' is 0x30, so [prefix, prefix-with-'/'→'0') bounds all descendants.
	rows, err := c.db.QueryContext(ctx, `
		SELECT DISTINCT substr(substr(path, ?), 1, instr(substr(path, ?), '/') - 1) AS name
		FROM files
		WHERE source = ? AND deleted_at IS NULL AND path >= ? AND path < ?
		  AND instr(substr(path, ?), '/') > 0
		ORDER BY name`,
		start, start, source, prefix, upperBound(prefix), start)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, Entry{Name: name, IsDir: true})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = c.db.QueryContext(ctx, `SELECT `+fileCols+` FROM files
		WHERE source = ? AND dir = ? AND deleted_at IS NULL ORDER BY path`, source, dir)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, Entry{Name: f.Name(), File: &f})
	}
	return out, rows.Err()
}

// upperBound returns the smallest string greater than every string with the
// given prefix, for prefixes ending in '/' (or empty).
func upperBound(prefix string) string {
	if prefix == "" {
		return "\U0010FFFF"
	}
	return prefix[:len(prefix)-1] + "0"
}

// ListTrash returns deleted files, most recently deleted first.
func (c *Catalog) ListTrash(ctx context.Context, source string) ([]File, error) {
	q := `SELECT ` + fileCols + ` FROM files WHERE deleted_at IS NOT NULL`
	args := []any{}
	if source != "" {
		q += ` AND source = ?`
		args = append(args, source)
	}
	return c.queryFiles(ctx, q+` ORDER BY deleted_at DESC`, args...)
}

// TrashedBefore returns files deleted before t.
func (c *Catalog) TrashedBefore(ctx context.Context, t time.Time) ([]File, error) {
	return c.queryFiles(ctx, `SELECT `+fileCols+` FROM files WHERE deleted_at IS NOT NULL AND deleted_at < ?`, ns(t))
}

func (c *Catalog) queryFiles(ctx context.Context, q string, args ...any) ([]File, error) {
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFile removes a file and its versions from the catalog.
func (c *Catalog) DeleteFile(ctx context.Context, id int64) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id)
	return err
}

const versionCols = `id, file_id, size, sha256, modified, stored_path, created_at`

func scanVersion(r scanner) (Version, error) {
	var v Version
	var mod, created int64
	err := r.Scan(&v.ID, &v.FileID, &v.Size, &v.SHA256, &mod, &v.StoredPath, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	v.Modified, v.CreatedAt = fromNS(mod), fromNS(created)
	return v, err
}

// ListVersions returns the previous versions of a file, newest first.
func (c *Catalog) ListVersions(ctx context.Context, fileID int64) ([]Version, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT `+versionCols+` FROM versions
		WHERE file_id = ? ORDER BY created_at DESC, id DESC`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVersion returns one version.
func (c *Catalog) GetVersion(ctx context.Context, id int64) (Version, error) {
	return scanVersion(c.db.QueryRowContext(ctx, `SELECT `+versionCols+` FROM versions WHERE id = ?`, id))
}

// ExpiredVersions returns versions that are both older than before and not
// among the newest keep versions of their file. ("Whichever keeps more.")
func (c *Catalog) ExpiredVersions(ctx context.Context, before time.Time, keep int) ([]Version, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT `+versionCols+` FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY file_id ORDER BY created_at DESC, id DESC) AS rn
			FROM versions
		) WHERE created_at < ? AND rn > ?`, ns(before), keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteVersion removes a version row.
func (c *Catalog) DeleteVersion(ctx context.Context, id int64) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM versions WHERE id = ?`, id)
	return err
}

// SourceState returns the sync state of a source (zero state if never synced).
func (c *Catalog) SourceState(ctx context.Context, id string) (SourceState, error) {
	s := SourceState{ID: id}
	var last sql.NullInt64
	err := c.db.QueryRowContext(ctx, `SELECT cursor, last_sync, last_error FROM sources WHERE id = ?`, id).
		Scan(&s.Cursor, &last, &s.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	s.LastSync = fromNullNS(last)
	return s, err
}

// SetCursor stores the resume cursor of a source.
func (c *Catalog) SetCursor(ctx context.Context, id, cursor string) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO sources (id, cursor) VALUES (?, ?)
		ON CONFLICT (id) DO UPDATE SET cursor = excluded.cursor`, id, cursor)
	return err
}

// RecordSync stores the outcome of a sync run.
func (c *Catalog) RecordSync(ctx context.Context, id string, at time.Time, syncErr error) error {
	msg := ""
	if syncErr != nil {
		msg = syncErr.Error()
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO sources (id, last_sync, last_error) VALUES (?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET last_sync = excluded.last_sync, last_error = excluded.last_error`,
		id, ns(at), msg)
	return err
}
