package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// changesSchema numbers every change to the files table with a global,
// increasing sequence, so clients can ask "what changed since N".
// Rows removed for good leave a tombstone.
const changesSchema = `
CREATE TABLE IF NOT EXISTS seq_counter (id INTEGER PRIMARY KEY CHECK (id = 1), value INTEGER NOT NULL);
INSERT OR IGNORE INTO seq_counter (id, value) VALUES (1, (SELECT COALESCE(MAX(seq), 0) FROM files));
CREATE TABLE IF NOT EXISTS tombstones (
	seq     INTEGER PRIMARY KEY,
	source  TEXT NOT NULL,
	file_id INTEGER NOT NULL,
	path    TEXT NOT NULL,
	is_dir  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS files_seq ON files (source, seq);
CREATE INDEX IF NOT EXISTS tombstones_source ON tombstones (source, seq);
CREATE TRIGGER IF NOT EXISTS files_seq_insert AFTER INSERT ON files BEGIN
	UPDATE seq_counter SET value = value + 1 WHERE id = 1;
	UPDATE files SET seq = (SELECT value FROM seq_counter WHERE id = 1) WHERE id = NEW.id;
END;
CREATE TRIGGER IF NOT EXISTS files_seq_update AFTER UPDATE OF path, size, modified, sha256, deleted_at, is_dir ON files BEGIN
	UPDATE seq_counter SET value = value + 1 WHERE id = 1;
	UPDATE files SET seq = (SELECT value FROM seq_counter WHERE id = 1) WHERE id = NEW.id;
END;
CREATE TRIGGER IF NOT EXISTS files_seq_delete AFTER DELETE ON files BEGIN
	UPDATE seq_counter SET value = value + 1 WHERE id = 1;
	INSERT INTO tombstones (seq, source, file_id, path, is_dir)
		VALUES ((SELECT value FROM seq_counter WHERE id = 1), OLD.source, OLD.id, OLD.path, OLD.is_dir);
END;
`

// Change is one entry of the change feed: the current state of a file or
// folder, or (Removed) a file that no longer exists at all.
type Change struct {
	Seq     int64
	File    File // for Removed, only ID, Source, Path, and IsDir are set
	Removed bool
}

// Changes returns up to limit changes in a source with Seq > cursor, oldest
// first, and whether more remain. Cursor 0 lists everything that exists.
// A file changed several times appears once, at its latest Seq.
func (c *Catalog) Changes(ctx context.Context, source string, cursor int64, limit int) ([]Change, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("limit must be positive")
	}
	files, err := c.queryFiles(ctx, `SELECT `+fileCols+` FROM files
		WHERE source = ? AND seq > ? ORDER BY seq LIMIT ?`, source, cursor, limit+1)
	if err != nil {
		return nil, false, err
	}
	var out []Change
	for _, f := range files {
		out = append(out, Change{Seq: f.Seq, File: f})
	}
	if cursor > 0 {
		rows, err := c.db.QueryContext(ctx, `SELECT seq, file_id, path, is_dir FROM tombstones
			WHERE source = ? AND seq > ? ORDER BY seq LIMIT ?`, source, cursor, limit+1)
		if err != nil {
			return nil, false, err
		}
		for rows.Next() {
			ch := Change{Removed: true, File: File{Source: source}}
			if err := rows.Scan(&ch.Seq, &ch.File.ID, &ch.File.Path, &ch.File.IsDir); err != nil {
				rows.Close()
				return nil, false, err
			}
			out = append(out, ch)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// LatestSeq returns the current change sequence number.
func (c *Catalog) LatestSeq(ctx context.Context) (int64, error) {
	var v int64
	err := c.db.QueryRowContext(ctx, `SELECT value FROM seq_counter WHERE id = 1`).Scan(&v)
	return v, err
}

// ErrExists is returned when a path is already used by a live file or folder.
var ErrExists = errors.New("path already exists")

// PutFolder creates a folder row (and rows for missing parent folders).
// It returns the folder; if a live folder already exists at p it is returned.
func (c *Catalog) PutFolder(ctx context.Context, source, p string, newItemID func() string, now time.Time) (File, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback()
	var f File
	for _, dir := range ancestorsAndSelf(p) {
		existing, err := scanFile(tx.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files
			WHERE source = ? AND path = ? AND deleted_at IS NULL`, source, dir))
		if err == nil {
			if !existing.IsDir {
				return File{}, fmt.Errorf("%w: %s is a file", ErrExists, dir)
			}
			f = existing
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			return File{}, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO files
			(source, source_item_id, path, dir, size, modified, sha256, is_dir) VALUES (?, ?, ?, ?, 0, ?, '', 1)`,
			source, newItemID(), dir, dirOf(dir), ns(now))
		if err != nil {
			return File{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return File{}, err
		}
		if f, err = scanFile(tx.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE id = ?`, id)); err != nil {
			return File{}, err
		}
	}
	return f, tx.Commit()
}

// ancestorsAndSelf returns "a", "a/b", "a/b/c" for "a/b/c".
func ancestorsAndSelf(p string) []string {
	var out []string
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return append(out, p)
}

// MoveTree changes the path prefix of a live folder row and everything below it.
func (c *Catalog) MoveTree(ctx context.Context, source, from, to string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, path FROM files
		WHERE source = ? AND deleted_at IS NULL AND (path = ? OR (path >= ? AND path < ?))`,
		source, from, from+"/", upperBound(from+"/"))
	if err != nil {
		return err
	}
	type mv struct {
		id int64
		p  string
	}
	var mvs []mv
	for rows.Next() {
		var m mv
		if err := rows.Scan(&m.id, &m.p); err != nil {
			rows.Close()
			return err
		}
		mvs = append(mvs, m)
	}
	rows.Close()
	for _, m := range mvs {
		np := to + m.p[len(from):]
		if _, err := tx.ExecContext(ctx, `UPDATE files SET path = ?, dir = ? WHERE id = ?`, np, dirOf(np), m.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
