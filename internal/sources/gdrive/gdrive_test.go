package gdrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/source"
	"github.com/LeoHChen/homebackup/internal/store"
)

const (
	docMime = "application/vnd.google-apps.document"
	pdfMime = "application/pdf"
)

// fakeDrive is a minimal in-memory Drive v3 server.
type fakeDrive struct {
	mu       sync.Mutex
	files    map[string]driveFile
	content  map[string]string
	changes  map[string]changeList // page token -> response
	fail429  int                   // number of 429s to return before succeeding
	requests int
}

func newFakeDrive() *fakeDrive {
	return &fakeDrive{files: map[string]driveFile{}, content: map[string]string{}, changes: map[string]changeList{}}
}

func (d *fakeDrive) add(id, name, mime, parent, content string) {
	f := driveFile{ID: id, Name: name, MimeType: mime, Parents: []string{parent},
		ModifiedTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Version: "1"}
	if mime != mimeFolder && !strings.HasPrefix(mime, mimePrefixGA) {
		f.MD5 = fmt.Sprintf("md5-%s", content)
		f.Size = fmt.Sprint(len(content))
	}
	d.files[id] = f
	d.content[id] = content
}

func (d *fakeDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests++
	if d.fail429 > 0 {
		d.fail429--
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	q := r.URL.Query()
	p := r.URL.Path
	switch {
	case p == "/files/root":
		json.NewEncoder(w).Encode(driveFile{ID: "root"})
	case p == "/changes/startPageToken":
		fmt.Fprint(w, `{"startPageToken":"c1"}`)
	case p == "/changes":
		cl, ok := d.changes[q.Get("pageToken")]
		if !ok {
			cl.NewStartPageToken = q.Get("pageToken") // no changes yet
		}
		json.NewEncoder(w).Encode(cl)
	case p == "/files":
		d.list(w, q)
	case strings.HasSuffix(p, "/export"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/files/"), "/export")
		if d.content[id] == "TOO_BIG" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"errors":[{"reason":"exportSizeLimitExceeded"}]}}`)
			return
		}
		fmt.Fprintf(w, "exported(%s as %s)", d.content[id], q.Get("mimeType"))
	case strings.HasPrefix(p, "/files/"):
		id := strings.TrimPrefix(p, "/files/")
		f, ok := d.files[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"errors":[{"reason":"notFound"}]}}`)
			return
		}
		if q.Get("alt") == "media" {
			io.WriteString(w, d.content[id])
			return
		}
		json.NewEncoder(w).Encode(f)
	default:
		http.NotFound(w, r)
	}
}

// list supports the three queries the source issues, with 2 items per page.
func (d *fakeDrive) list(w http.ResponseWriter, q getter) {
	query := q.Get("q")
	var match []driveFile
	for _, f := range d.files {
		if f.Trashed {
			continue
		}
		switch {
		case strings.HasPrefix(query, "mimeType = "):
			if f.MimeType == mimeFolder {
				match = append(match, f)
			}
		case strings.HasPrefix(query, "mimeType != "):
			if f.MimeType != mimeFolder {
				match = append(match, f)
			}
		case strings.Contains(query, "in parents"):
			parent := strings.Split(query, "'")[1]
			if f.parent() == parent {
				match = append(match, f)
			}
		}
	}
	sort.Slice(match, func(i, j int) bool { return match[i].ID < match[j].ID })
	start := 0
	fmt.Sscan(q.Get("pageToken"), &start)
	end := min(start+2, len(match))
	out := fileList{Files: match[start:end]}
	if end < len(match) {
		out.NextPageToken = fmt.Sprint(end)
	}
	json.NewEncoder(w).Encode(out)
}

type getter interface{ Get(string) string }

type harness struct {
	drive *fakeDrive
	eng   *engine.Engine
	st    *store.Store
	src   *Source
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d := newFakeDrive()
	ts := httptest.NewServer(d)
	t.Cleanup(ts.Close)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Open(filepath.Join(st.StateDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	src, err := New(Options{ID: "gdrive", Client: ts.Client(), BaseURL: ts.URL,
		StateFile: filepath.Join(st.StateDir(), "gdrive-folders.json"), RetryBase: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, cat, config.Default().Retention, nil)
	eng.Register(src, "gdrive", 0)
	return &harness{drive: d, eng: eng, st: st, src: src}
}

func (h *harness) sync(t *testing.T) {
	t.Helper()
	if err := h.eng.Sync(context.Background(), "gdrive"); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) read(t *testing.T, p string) string {
	t.Helper()
	dp, _ := h.st.DataPath("gdrive", p)
	b, err := os.ReadFile(dp)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return string(b)
}

func (h *harness) exists(p string) bool {
	dp, _ := h.st.DataPath("gdrive", p)
	_, err := os.Stat(dp)
	return err == nil
}

func (h *harness) change(token, next string, chs ...string) {
	// chs entries: "id" for an update to the current state, "-id" for removal.
	var cl changeList
	for _, c := range chs {
		entry := struct {
			FileID  string     `json:"fileId"`
			Removed bool       `json:"removed"`
			File    *driveFile `json:"file"`
		}{FileID: strings.TrimPrefix(c, "-")}
		if strings.HasPrefix(c, "-") {
			entry.Removed = true
		} else {
			f := h.drive.files[c]
			entry.File = &f
		}
		cl.Changes = append(cl.Changes, entry)
	}
	cl.NewStartPageToken = next
	h.drive.changes[token] = cl
}

func TestInitialSyncAndChanges(t *testing.T) {
	h := newHarness(t)
	d := h.drive
	d.add("f1", "Work", mimeFolder, "root", "")
	d.add("f2", "Reports", mimeFolder, "f1", "")
	d.add("a", "notes.txt", "text/plain", "root", "hello")
	d.add("b", "q3.pdf", pdfMime, "f2", "pdfdata")
	d.add("c", "Plan", docMime, "f1", "plan")
	d.add("d", "Survey", "application/vnd.google-apps.form", "root", "")
	d.add("e", "a/b:c", "text/plain", "f1", "weird")
	d.add("x", "shared.txt", "text/plain", "someone-elses-folder", "nope")
	d.fail429 = 2 // first requests are rate limited and retried

	h.sync(t)
	if got := h.read(t, "notes.txt"); got != "hello" {
		t.Errorf("notes.txt = %q", got)
	}
	if got := h.read(t, "Work/Reports/q3.pdf"); got != "pdfdata" {
		t.Errorf("q3.pdf = %q", got)
	}
	if got := h.read(t, "Work/Plan.docx"); !strings.HasPrefix(got, "exported(plan as application/vnd.openxmlformats") {
		t.Errorf("Plan.docx = %q", got)
	}
	if got := h.read(t, "Work/a_b:c"); got != "weird" {
		t.Errorf("sanitized name = %q", got)
	}
	if h.exists("Survey") || h.exists("shared.txt") {
		t.Error("forms and files outside My Drive must be skipped")
	}
	st, _ := h.eng.Catalog.SourceState(context.Background(), "gdrive")
	if st.Cursor != "changes|c1" {
		t.Fatalf("cursor = %q", st.Cursor)
	}

	// Incremental: rename folder Work -> Job, edit notes, delete pdf, new folder with a file.
	reqs := d.requests
	f1 := d.files["f1"]
	f1.Name = "Job"
	d.files["f1"] = f1
	d.add("a", "notes.txt", "text/plain", "root", "hello v2")
	d.add("f3", "New", mimeFolder, "root", "")
	d.add("n", "new.txt", "text/plain", "f3", "fresh")
	h.change("c1", "c2", "f1", "a", "-b", "f3")
	h.sync(t)

	if got := h.read(t, "Job/Plan.docx"); !strings.Contains(got, "plan") || h.exists("Work/Plan.docx") {
		t.Error("folder rename must move children")
	}
	if got := h.read(t, "notes.txt"); got != "hello v2" {
		t.Errorf("notes.txt = %q", got)
	}
	if h.exists("Job/Reports/q3.pdf") || h.exists("Work/Reports/q3.pdf") {
		t.Error("removed file must leave the data dir")
	}
	if got := h.read(t, "New/new.txt"); got != "fresh" {
		t.Errorf("new.txt = %q", got)
	}
	if d.requests-reqs > 8 {
		t.Errorf("incremental sync used %d requests; renamed children should not be re-downloaded", d.requests-reqs)
	}

	// Trash a folder: everything below goes to the trash.
	f1 = d.files["f1"]
	f1.Trashed = true
	d.files["f1"] = f1
	h.change("c2", "c3", "f1")
	h.sync(t)
	if h.exists("Job/Plan.docx") || h.exists("Job/a_b:c") {
		t.Error("trashed folder contents must be removed")
	}
	tr, _ := h.eng.Catalog.ListTrash(context.Background(), "gdrive")
	if len(tr) != 3 { // q3.pdf, Plan.docx, a_b:c
		t.Errorf("trash has %d files, want 3", len(tr))
	}
}

func TestFolderStatePersists(t *testing.T) {
	h := newHarness(t)
	h.drive.add("f1", "Work", mimeFolder, "root", "")
	h.sync(t)
	src2, err := New(Options{ID: "gdrive", StateFile: h.src.stateFile})
	if err != nil {
		t.Fatal(err)
	}
	if src2.state.RootID != "root" || src2.state.Folders["f1"].Name != "Work" {
		t.Fatalf("state not persisted: %+v", src2.state)
	}
}

func TestExportTooLargeIsSkipped(t *testing.T) {
	h := newHarness(t)
	h.drive.add("big", "Huge", docMime, "root", "TOO_BIG")
	h.drive.add("ok", "small.txt", "text/plain", "root", "fine")
	h.sync(t) // must not fail or block the cursor
	if h.exists("Huge.docx") || h.read(t, "small.txt") != "fine" {
		t.Fatal("unexpected result")
	}
}

func TestOpenMissingFileIsPermanent(t *testing.T) {
	h := newHarness(t)
	_, err := h.src.Open(context.Background(), source.Item{SourceID: "gone"})
	if err == nil || !strings.Contains(err.Error(), source.ErrPermanent.Error()) {
		t.Fatalf("err = %v", err)
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{"a/b": "a_b", `a\b`: "a_b", "..": "_", "": "_", "ok.txt": "ok.txt"} {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
