// Package takeout imports Google Photos from Google Takeout exports.
//
// Since March 2025 the Google Photos Library API only returns media uploaded
// by the calling app, so a full library can only be exported with Google
// Takeout. Drop the Takeout .zip files (all parts) or extracted folders into
// the inbox directory; each sync imports archives that have not been
// imported yet. Photos are stored as <year>/<month>/<file name> using the
// capture time from Takeout's JSON sidecar files.
//
// Takeout repeats a photo in every album that contains it, so items are
// identified by (file name, size, capture time): the same photo from
// several albums or from a newer Takeout is stored once.
package takeout

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LeoHChen/homebackup/internal/source"
)

// photosFolders are the localized names of the Google Photos folder in Takeout.
var photosFolders = map[string]bool{
	"Google Photos": true, "Google Fotos": true, "Google Foto": true,
	"Google 相册": true, "Google 相簿": true, "Google フォト": true, "Google 포토": true,
}

// Source imports Takeout archives from an inbox directory.
type Source struct {
	id    string
	inbox string
	log   *slog.Logger

	mu      sync.Mutex
	open    []io.Closer
	entries map[string]entry // item ID -> where to read it
}

type entry struct {
	fsys fs.FS
	name string
}

// New returns a Takeout source reading from inbox.
func New(id, inbox string, log *slog.Logger) (*Source, error) {
	if inbox == "" {
		return nil, errors.New("takeout: options.inbox is required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Source{id: id, inbox: inbox, log: log, entries: map[string]entry{}}, nil
}

// ID implements source.Source.
func (s *Source) ID() string { return s.id }

// archive is a zip file or an extracted directory in the inbox.
type archive struct {
	key  string // stable fingerprint: name and size (or "dir")
	fsys fs.FS
}

// Changes implements source.Source. The cursor is the newline-separated list
// of archive keys already imported. Each call imports one archive.
func (s *Source) Changes(ctx context.Context, cursor string) (source.ChangeSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeArchives()

	done := map[string]bool{}
	for _, k := range strings.Split(cursor, "\n") {
		if k != "" {
			done[k] = true
		}
	}
	archives, err := s.openArchives()
	if err != nil {
		return source.ChangeSet{}, err
	}
	var next *archive
	for i := range archives {
		if !done[archives[i].key] {
			next = &archives[i]
			break
		}
	}
	if next == nil {
		return source.ChangeSet{NextCursor: cursor}, nil
	}

	// Sidecars may be in a different part of a split export, so index all.
	sidecars := map[sidecarKey]sidecar{}
	for _, a := range archives {
		if err := indexSidecars(a.fsys, sidecars); err != nil {
			return source.ChangeSet{}, fmt.Errorf("read metadata in %s: %w", a.key, err)
		}
	}
	items, err := s.mediaItems(*next, sidecars)
	if err != nil {
		return source.ChangeSet{}, fmt.Errorf("read %s: %w", next.key, err)
	}
	s.log.Info("takeout archive", "source", s.id, "archive", next.key, "items", len(items))

	cs := source.ChangeSet{More: true}
	for _, it := range items {
		cs.Changes = append(cs.Changes, source.Change{Kind: source.Upserted, Item: it})
	}
	if cursor != "" {
		cursor += "\n"
	}
	cs.NextCursor = cursor + next.key
	return cs, nil
}

// Open implements source.Source.
func (s *Source) Open(_ context.Context, it source.Item) (io.ReadCloser, error) {
	s.mu.Lock()
	e, ok := s.entries[it.SourceID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("takeout: unknown item %s (archive removed?)", it.Path)
	}
	return e.fsys.Open(e.name)
}

func (s *Source) closeArchives() {
	for _, c := range s.open {
		c.Close()
	}
	s.open = nil
	s.entries = map[string]entry{}
}

func (s *Source) openArchives() ([]archive, error) {
	des, err := os.ReadDir(s.inbox)
	if err != nil {
		return nil, fmt.Errorf("takeout inbox: %w", err)
	}
	var out []archive
	for _, de := range des {
		name := de.Name()
		full := filepath.Join(s.inbox, name)
		switch {
		case strings.HasPrefix(name, "."):
		case de.IsDir():
			out = append(out, archive{key: name + "/", fsys: os.DirFS(full)})
		case strings.EqualFold(filepath.Ext(name), ".zip"):
			fi, err := de.Info()
			if err != nil {
				return nil, err
			}
			zr, err := zip.OpenReader(full)
			if err != nil {
				// Possibly still downloading/copying; try again next sync.
				s.log.Warn("skipping unreadable zip", "file", name, "err", err)
				continue
			}
			s.open = append(s.open, zr)
			out = append(out, archive{key: name + ":" + strconv.FormatInt(fi.Size(), 10), fsys: zr})
		case strings.HasSuffix(name, ".tgz") || strings.HasSuffix(name, ".tar.gz"):
			s.log.Warn("tgz Takeout exports are not supported; extract it into the inbox or export as .zip", "file", name)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out, nil
}

// photosPath reports whether name is inside Takeout's Google Photos folder and
// returns the path relative to it.
func photosPath(name string) (string, bool) {
	parts := strings.SplitN(name, "/", 3)
	if len(parts) == 3 && parts[0] == "Takeout" && photosFolders[parts[1]] {
		return parts[2], true
	}
	return "", false
}

type sidecarKey struct {
	dir, title string
	dup        int
}

type sidecar struct {
	Title          string `json:"title"`
	PhotoTakenTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"photoTakenTime"`
	CreationTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"creationTime"`
}

func (sc sidecar) taken() time.Time {
	for _, ts := range []string{sc.PhotoTakenTime.Timestamp, sc.CreationTime.Timestamp} {
		if v, err := strconv.ParseInt(ts, 10, 64); err == nil && v > 0 {
			return time.Unix(v, 0).UTC()
		}
	}
	return time.Time{}
}

// jsonDup matches the "(1)" Takeout puts before ".json" for duplicate names:
// "IMG.JPG(1).json" is the sidecar of "IMG(1).JPG".
var jsonDup = regexp.MustCompile(`\((\d+)\)\.json$`)

func indexSidecars(fsys fs.FS, idx map[sidecarKey]sidecar) error {
	return fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".json") {
			return err
		}
		rel, ok := photosPath(name)
		if !ok {
			return nil
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		var sc sidecar
		if json.Unmarshal(b, &sc) != nil || sc.Title == "" {
			return nil // album metadata.json and similar
		}
		dup := 0
		if m := jsonDup.FindStringSubmatch(name); m != nil {
			dup, _ = strconv.Atoi(m[1])
		}
		idx[sidecarKey{path.Dir(rel), sc.Title, dup}] = sc
		return nil
	})
}

// mediaDup matches "name(1).ext".
var mediaDup = regexp.MustCompile(`^(.*)\((\d+)\)(\.[^.]*)?$`)

func lookupSidecar(idx map[sidecarKey]sidecar, dir, base string) (sidecar, bool) {
	title, dup := base, 0
	if m := mediaDup.FindStringSubmatch(base); m != nil {
		title = m[1] + m[3]
		dup, _ = strconv.Atoi(m[2])
	}
	if sc, ok := idx[sidecarKey{dir, title, dup}]; ok {
		return sc, true
	}
	if sc, ok := idx[sidecarKey{dir, base, 0}]; ok {
		return sc, true
	}
	// Edited copies ("IMG-edited.jpg") share the original's sidecar.
	ext := path.Ext(title)
	if stem := strings.TrimSuffix(title, ext); strings.HasSuffix(stem, "-edited") {
		if sc, ok := idx[sidecarKey{dir, strings.TrimSuffix(stem, "-edited") + ext, dup}]; ok {
			return sc, true
		}
	}
	return sidecar{}, false
}

func (s *Source) mediaItems(a archive, sidecars map[sidecarKey]sidecar) ([]source.Item, error) {
	var items []source.Item
	seen := map[string]bool{}
	err := fs.WalkDir(a.fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, ok := photosPath(name)
		if !ok {
			return nil
		}
		base := path.Base(rel)
		switch strings.ToLower(path.Ext(base)) {
		case ".json", ".html":
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		taken := fi.ModTime().UTC()
		if sc, ok := lookupSidecar(sidecars, path.Dir(rel), base); ok && !sc.taken().IsZero() {
			taken = sc.taken()
		}
		id := itemID(base, fi.Size(), taken)
		if seen[id] {
			return nil // same photo in another album folder
		}
		seen[id] = true
		s.entries[id] = entry{fsys: a.fsys, name: name}
		items = append(items, source.Item{
			SourceID: id,
			Path:     taken.Format("2006/01") + "/" + base,
			Size:     fi.Size(),
			Modified: taken,
			Checksum: "size:" + strconv.FormatInt(fi.Size(), 10),
		})
		return nil
	})
	return items, err
}

func itemID(base string, size int64, taken time.Time) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", base, size, taken.Unix())))
	return hex.EncodeToString(h[:16])
}
