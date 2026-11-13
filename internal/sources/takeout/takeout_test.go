package takeout

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/store"
)

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func meta(title string, ts int64) string {
	return fmt.Sprintf(`{"title":%q,"photoTakenTime":{"timestamp":"%d"}}`, title, ts)
}

var (
	may2023 = time.Date(2023, 5, 6, 7, 8, 9, 0, time.UTC).Unix()
	dec2024 = time.Date(2024, 12, 25, 10, 0, 0, 0, time.UTC).Unix()
	jun2022 = time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC).Unix()
)

type harness struct {
	inbox string
	eng   *engine.Engine
	st    *store.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Open(filepath.Join(st.StateDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	inbox := t.TempDir()
	src, err := New("gphotos", inbox, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, cat, config.Default().Retention, nil)
	eng.Register(src, "gphotos-takeout", 0)
	return &harness{inbox: inbox, eng: eng, st: st}
}

func (h *harness) sync(t *testing.T) {
	t.Helper()
	if err := h.eng.Sync(context.Background(), "gphotos"); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) read(t *testing.T, rel string) (string, time.Time) {
	t.Helper()
	p, _ := h.st.DataPath("gphotos", rel)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	fi, _ := os.Stat(p)
	return string(b), fi.ModTime().UTC()
}

func (h *harness) count(t *testing.T) int {
	t.Helper()
	n := 0
	filepath.WalkDir(filepath.Join(h.st.Root(), "data", "gphotos"), func(_ string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestImportMultiPartTakeout(t *testing.T) {
	h := newHarness(t)
	gp := "Takeout/Google Photos/"
	writeZip(t, filepath.Join(h.inbox, "takeout-001.zip"), map[string]string{
		gp + "Photos from 2023/IMG_1.JPG":                            "photo1",
		gp + "Photos from 2023/IMG_1.JPG.supplemental-metadata.json": meta("IMG_1.JPG", may2023),
		gp + "Trip/IMG_1.JPG":                                        "photo1", // same photo in an album
		gp + "Trip/IMG_1.JPG.json":                                   meta("IMG_1.JPG", may2023),
		gp + "Trip/metadata.json":                                    `{"title":""}`,
		gp + "Photos from 2024/IMG_1(1).JPG":                         "another photo, same name",
		gp + "Photos from 2024/IMG_2.jpg":                            "orig",
		gp + "Photos from 2024/IMG_2-edited.jpg":                     "edited",
		gp + "Photos from 2024/IMG_2.jpg.json":                       meta("IMG_2.jpg", dec2024),
		gp + "Photos from 2022/VID_9.MP4":                            "video",
		"Takeout/Google Drive/notes.txt":                             "not photos",
		"Takeout/archive_browser.html":                               "<html>",
	})
	// Second part holds sidecars for media in the first part.
	writeZip(t, filepath.Join(h.inbox, "takeout-002.zip"), map[string]string{
		gp + "Photos from 2024/IMG_1.JPG(1).json":                    meta("IMG_1.JPG", dec2024),
		gp + "Photos from 2022/VID_9.MP4.supplemental-metadata.json": meta("VID_9.MP4", jun2022),
		gp + "Photos from 2022/no_sidecar.png":                       "png",
	})

	h.sync(t)

	cases := []struct {
		rel, body string
		when      int64
	}{
		{"2023/05/IMG_1.JPG", "photo1", may2023},
		{"2024/12/IMG_1(1).JPG", "another photo, same name", dec2024},
		{"2024/12/IMG_2.jpg", "orig", dec2024},
		{"2024/12/IMG_2-edited.jpg", "edited", dec2024},
		{"2022/06/VID_9.MP4", "video", jun2022},
		{"2025/01/no_sidecar.png", "png", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix()},
	}
	for _, c := range cases {
		body, mod := h.read(t, c.rel)
		if body != c.body {
			t.Errorf("%s = %q, want %q", c.rel, body, c.body)
		}
		if mod.Unix() != c.when {
			t.Errorf("%s mtime = %v, want %v", c.rel, mod, time.Unix(c.when, 0).UTC())
		}
	}
	if n := h.count(t); n != len(cases) {
		t.Errorf("stored %d files, want %d (album duplicates and non-photos excluded)", n, len(cases))
	}

	// A newer Takeout with an overlapping photo adds only what is new.
	writeZip(t, filepath.Join(h.inbox, "takeout-2026-001.zip"), map[string]string{
		gp + "Photos from 2023/IMG_1.JPG":      "photo1",
		gp + "Photos from 2023/IMG_1.JPG.json": meta("IMG_1.JPG", may2023),
		gp + "Photos from 2026/NEW.HEIC":       "new",
		gp + "Photos from 2026/NEW.HEIC.json":  meta("NEW.HEIC", time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC).Unix()),
	})
	h.sync(t)
	if body, _ := h.read(t, "2026/03/NEW.HEIC"); body != "new" {
		t.Errorf("NEW.HEIC = %q", body)
	}
	if n := h.count(t); n != len(cases)+1 {
		t.Errorf("stored %d files after second import, want %d", n, len(cases)+1)
	}
	st, _ := h.eng.Catalog.SourceState(context.Background(), "gphotos")
	if st.LastError != "" {
		t.Errorf("last error = %q", st.LastError)
	}
}

func TestExtractedFolderAndBadZip(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.inbox, "Takeout-extracted", "Takeout", "Google Photos", "Album")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "a.jpg"), []byte("A"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.jpg.json"), []byte(meta("a.jpg", may2023)), 0o644)
	os.WriteFile(filepath.Join(h.inbox, "partial.zip"), []byte("not a zip yet"), 0o644)

	// An extracted export is a folder containing "Takeout/...". The partial
	// zip (still downloading) is skipped and does not fail the sync.
	h.sync(t)
	if body, _ := h.read(t, "2023/05/a.jpg"); body != "A" {
		t.Errorf("a.jpg = %q", body)
	}
}

func TestLookupSidecar(t *testing.T) {
	idx := map[sidecarKey]sidecar{
		{"d", "IMG.JPG", 0}: {Title: "IMG.JPG"},
		{"d", "IMG.JPG", 2}: {Title: "IMG.JPG", CreationTime: struct {
			Timestamp string `json:"timestamp"`
		}{"5"}},
		{"d", "photo.png", 0}: {Title: "photo.png"},
	}
	if _, ok := lookupSidecar(idx, "d", "IMG.JPG"); !ok {
		t.Error("plain lookup failed")
	}
	if sc, ok := lookupSidecar(idx, "d", "IMG(2).JPG"); !ok || sc.taken().Unix() != 5 {
		t.Error("dup lookup failed")
	}
	if _, ok := lookupSidecar(idx, "d", "photo-edited.png"); !ok {
		t.Error("edited lookup failed")
	}
	if _, ok := lookupSidecar(idx, "other", "IMG.JPG"); ok {
		t.Error("lookup must be per directory")
	}
}
