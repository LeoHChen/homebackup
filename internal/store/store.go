// Package store manages the on-disk layout of the backup root.
//
// Current files are kept as plain files so a backup disk can be browsed on any
// computer without this software:
//
//	<root>/data/<source>/<path>                         current version
//	<root>/versions/<source>/<path>/<UTC time>_<name>   previous versions
//	<root>/trash/<source>/<UTC date>/<path>             deleted files
//	<root>/state/                                       catalog database
//	<root>/tmp/                                         in-progress writes
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	dirData     = "data"
	dirVersions = "versions"
	dirTrash    = "trash"
	dirState    = "state"
	dirTmp      = "tmp"
)

// ErrInvalidPath is returned for paths that are absolute or escape the source root.
var ErrInvalidPath = errors.New("invalid path")

// Store is a backup root on a local disk.
type Store struct {
	root string
	now  func() time.Time
}

// Open creates the directory layout under root if needed and returns a Store.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("store root is empty")
	}
	for _, d := range []string{dirData, dirVersions, dirTrash, dirState, dirTmp} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			return nil, err
		}
	}
	return &Store{root: root, now: time.Now}, nil
}

// Root returns the store root directory.
func (s *Store) Root() string { return s.root }

// StateDir returns the directory for the catalog database.
func (s *Store) StateDir() string { return filepath.Join(s.root, dirState) }

// cleanRel validates a source ID and a slash-separated relative path.
func cleanRel(sourceID, rel string) (string, error) {
	if sourceID == "" || strings.ContainsAny(sourceID, `/\`) || sourceID == "." || sourceID == ".." {
		return "", fmt.Errorf("%w: source id %q", ErrInvalidPath, sourceID)
	}
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, rel)
	}
	c := path.Clean(rel)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, rel)
	}
	return filepath.Join(sourceID, filepath.FromSlash(c)), nil
}

// DataPath returns the location of the current version of a file.
func (s *Store) DataPath(sourceID, rel string) (string, error) {
	r, err := cleanRel(sourceID, rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, dirData, r), nil
}

// Put writes r as the current version of rel. If a current version exists it is
// moved to the versions directory first. Put returns the SHA-256 of the content.
// The write is atomic: readers see either the old or the new file.
func (s *Store) Put(sourceID, rel string, r io.Reader) (string, error) {
	dst, err := s.DataPath(sourceID, rel)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.root, dirTmp), "put-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	if _, err := os.Stat(dst); err == nil {
		if err := s.moveToVersions(sourceID, rel, dst); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) moveToVersions(sourceID, rel, current string) error {
	r, _ := cleanRel(sourceID, rel) // already validated by caller
	dir := filepath.Join(s.root, dirVersions, r)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	name := s.now().UTC().Format("20060102T150405.000000000Z") + "_" + filepath.Base(current)
	return os.Rename(current, filepath.Join(dir, name))
}

// Delete moves the current version of rel into the trash and returns its new location.
func (s *Store) Delete(sourceID, rel string) (string, error) {
	src, err := s.DataPath(sourceID, rel)
	if err != nil {
		return "", err
	}
	r, _ := cleanRel(sourceID, rel)
	parts := strings.SplitN(r, string(filepath.Separator), 2)
	dst := filepath.Join(s.root, dirTrash, parts[0], s.now().UTC().Format("2006-01-02"), parts[1])
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", err
	}
	if _, err := os.Stat(dst); err == nil {
		// Same file deleted twice on one day: keep both.
		dst += "." + s.now().UTC().Format("150405.000000000")
	}
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}
