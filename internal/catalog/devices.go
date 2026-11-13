package catalog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"
)

const devicesSchema = `
CREATE TABLE IF NOT EXISTS devices (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	platform   TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	last_seen  INTEGER
);
CREATE TABLE IF NOT EXISTS pair_codes (
	code_hash  TEXT PRIMARY KEY,
	expires_at INTEGER NOT NULL,
	attempts   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS uploads (
	id         TEXT PRIMARY KEY,
	device_id  TEXT NOT NULL,
	source     TEXT NOT NULL,
	item_id    TEXT NOT NULL,
	path       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	sha256     TEXT NOT NULL,
	modified   INTEGER NOT NULL,
	tmp_path   TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	UNIQUE (source, item_id)
);
`

// ErrBadCode is returned for a wrong, expired, or exhausted pairing code.
var ErrBadCode = errors.New("invalid or expired pairing code")

// maxPairAttempts is how many wrong guesses invalidate all pending codes.
const maxPairAttempts = 5

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewPairingCode creates a 6-digit pairing code valid for ttl. Only the hash
// is stored. Any previous codes are replaced.
func (c *Catalog) NewPairingCode(ctx context.Context, now time.Time, ttl time.Duration) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM pair_codes`); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pair_codes (code_hash, expires_at) VALUES (?, ?)`,
		hashSecret(code), ns(now.Add(ttl))); err != nil {
		return "", err
	}
	return code, tx.Commit()
}

// Device is a paired client.
type Device struct {
	ID        string
	Name      string
	Platform  string
	CreatedAt time.Time
	LastSeen  *time.Time
}

// Pair redeems a pairing code and registers a device. It returns the device
// and its bearer token (shown once; only the hash is stored). Wrong codes
// count as attempts; after maxPairAttempts all pending codes are revoked.
func (c *Catalog) Pair(ctx context.Context, code, name, platform string, now time.Time) (Device, string, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM pair_codes WHERE expires_at <= ? OR attempts >= ?`,
		ns(now), maxPairAttempts); err != nil {
		return Device{}, "", err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM pair_codes WHERE code_hash = ?`, hashSecret(code))
	if err != nil {
		return Device{}, "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE pair_codes SET attempts = attempts + 1`); err != nil {
			return Device{}, "", err
		}
		if err := tx.Commit(); err != nil {
			return Device{}, "", err
		}
		return Device{}, "", ErrBadCode
	}
	id, err := randomHex(8)
	if err != nil {
		return Device{}, "", err
	}
	token, err := randomHex(32)
	if err != nil {
		return Device{}, "", err
	}
	d := Device{ID: id, Name: name, Platform: platform, CreatedAt: now.UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id, name, platform, token_hash, created_at)
		VALUES (?, ?, ?, ?, ?)`, d.ID, d.Name, d.Platform, hashSecret(token), ns(now)); err != nil {
		return Device{}, "", err
	}
	return d, token, tx.Commit()
}

// DeviceByToken returns the device a bearer token belongs to and records
// that it was seen.
func (c *Catalog) DeviceByToken(ctx context.Context, token string, now time.Time) (Device, error) {
	var d Device
	var created int64
	var seen sql.NullInt64
	err := c.db.QueryRowContext(ctx, `SELECT id, name, platform, created_at, last_seen FROM devices WHERE token_hash = ?`,
		hashSecret(token)).Scan(&d.ID, &d.Name, &d.Platform, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, err
	}
	d.CreatedAt, d.LastSeen = fromNS(created), fromNullNS(seen)
	// Throttle writes: update last_seen at most once a minute.
	if d.LastSeen == nil || now.Sub(*d.LastSeen) > time.Minute {
		if _, err := c.db.ExecContext(ctx, `UPDATE devices SET last_seen = ? WHERE id = ?`, ns(now), d.ID); err != nil {
			return Device{}, err
		}
	}
	return d, nil
}

// ListDevices returns paired devices, oldest first.
func (c *Catalog) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id, name, platform, created_at, last_seen FROM devices ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var created int64
		var seen sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Name, &d.Platform, &created, &seen); err != nil {
			return nil, err
		}
		d.CreatedAt, d.LastSeen = fromNS(created), fromNullNS(seen)
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDevice revokes a device and its upload sessions.
func (c *Catalog) DeleteDevice(ctx context.Context, id string) error {
	res, err := c.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Upload is a resumable upload session.
type Upload struct {
	ID        string
	DeviceID  string
	Source    string
	ItemID    string
	Path      string
	Size      int64
	SHA256    string
	Modified  time.Time
	TmpPath   string
	CreatedAt time.Time
}

const uploadCols = `id, device_id, source, item_id, path, size, sha256, modified, tmp_path, created_at`

func scanUpload(r scanner) (Upload, error) {
	var u Upload
	var mod, created int64
	err := r.Scan(&u.ID, &u.DeviceID, &u.Source, &u.ItemID, &u.Path, &u.Size, &u.SHA256, &mod, &u.TmpPath, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	u.Modified, u.CreatedAt = fromNS(mod), fromNS(created)
	return u, err
}

// CreateUpload stores a new upload session. If a session for the same item
// already exists it is returned instead (resume), with created=false.
func (c *Catalog) CreateUpload(ctx context.Context, u Upload) (Upload, bool, error) {
	existing, err := scanUpload(c.db.QueryRowContext(ctx,
		`SELECT `+uploadCols+` FROM uploads WHERE source = ? AND item_id = ?`, u.Source, u.ItemID))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Upload{}, false, err
	}
	if u.ID, err = randomHex(12); err != nil {
		return Upload{}, false, err
	}
	_, err = c.db.ExecContext(ctx, `INSERT INTO uploads (`+uploadCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.DeviceID, u.Source, u.ItemID, u.Path, u.Size, u.SHA256, ns(u.Modified), u.TmpPath, ns(u.CreatedAt))
	return u, true, err
}

// SetUploadTmpPath records the temporary file of a session.
func (c *Catalog) SetUploadTmpPath(ctx context.Context, id, tmp string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE uploads SET tmp_path = ? WHERE id = ?`, tmp, id)
	return err
}

// GetUpload returns an upload session.
func (c *Catalog) GetUpload(ctx context.Context, id string) (Upload, error) {
	return scanUpload(c.db.QueryRowContext(ctx, `SELECT `+uploadCols+` FROM uploads WHERE id = ?`, id))
}

// DeleteUpload removes an upload session row.
func (c *Catalog) DeleteUpload(ctx context.Context, id string) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, id)
	return err
}

// StaleUploads returns sessions created before t, and sessions of devices
// that were revoked.
func (c *Catalog) StaleUploads(ctx context.Context, t time.Time) ([]Upload, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT `+uploadCols+` FROM uploads
		WHERE created_at < ? OR device_id NOT IN (SELECT id FROM devices)`, ns(t))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
