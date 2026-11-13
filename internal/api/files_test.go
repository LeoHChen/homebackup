package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/source"
	"github.com/LeoHChen/homebackup/internal/store"
)

type memSource struct {
	changes []source.Change
	content map[string]string
}

func (m *memSource) ID() string { return "mem" }
func (m *memSource) Changes(context.Context, string) (source.ChangeSet, error) {
	cs := source.ChangeSet{Changes: m.changes, NextCursor: "x"}
	m.changes = nil
	return cs, nil
}
func (m *memSource) Open(_ context.Context, it source.Item) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(m.content[it.SourceID])), nil
}

type fileEnv struct {
	ts  *httptest.Server
	eng *engine.Engine
	src *memSource
}

func newFileEnv(t *testing.T) *fileEnv {
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
	eng := engine.New(st, cat, config.Default().Retention, nil)
	src := &memSource{content: map[string]string{}}
	eng.Register(src, "mem", time.Hour)
	s := &Server{Version: "test", Tokens: StaticTokens{"tok"}, Engine: eng, Status: func() Status { return Status{} }}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &fileEnv{ts: ts, eng: eng, src: src}
}

func (e *fileEnv) sync(t *testing.T, chs ...source.Change) {
	t.Helper()
	e.src.changes = chs
	if err := e.eng.Sync(context.Background(), "mem"); err != nil {
		t.Fatal(err)
	}
}

func (e *fileEnv) do(t *testing.T, method, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer tok")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

func up(id, p, sum string) source.Change {
	return source.Change{Kind: source.Upserted, Item: source.Item{SourceID: id, Path: p, Checksum: sum, Modified: time.Unix(1700000000, 0)}}
}

func TestFilesAPI(t *testing.T) {
	e := newFileEnv(t)
	e.src.content["1"] = "hello world"
	e.src.content["2"] = "nested"
	e.sync(t, up("1", "a.txt", "c1"), up("2", "docs/b.txt", "c2"))

	// Sources.
	resp, b := e.do(t, "GET", "/api/v1/sources", nil, nil)
	srcs := decode[[]SourceJSON](t, b)
	if resp.StatusCode != 200 || len(srcs) != 1 || srcs[0].ID != "mem" || srcs[0].Schedule != "1h0m0s" || srcs[0].LastSync == nil {
		t.Fatalf("sources = %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/sources/mem/sync", nil, nil); resp.StatusCode != 202 {
		t.Fatalf("sync status = %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/sources/nope/sync", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("unknown sync status = %d", resp.StatusCode)
	}

	// Listing.
	_, b = e.do(t, "GET", "/api/v1/files?source=mem", nil, nil)
	list := decode[struct{ Items []FileJSON }](t, b).Items
	if len(list) != 2 || !list[0].IsDir || list[0].Path != "docs" || list[1].Name != "a.txt" {
		t.Fatalf("root listing = %s", b)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/files", nil, nil); resp.StatusCode != 400 {
		t.Fatalf("missing source status = %d", resp.StatusCode)
	}
	id := list[1].ID

	// Metadata and content with Range.
	_, b = e.do(t, "GET", "/api/v1/files/"+id, nil, nil)
	if f := decode[FileJSON](t, b); f.Size != 11 || f.SHA256 == "" {
		t.Fatalf("file = %s", b)
	}
	resp, b = e.do(t, "GET", "/api/v1/files/"+id+"/content", nil, map[string]string{"Range": "bytes=6-"})
	if resp.StatusCode != 206 || string(b) != "world" {
		t.Fatalf("range = %d %q", resp.StatusCode, b)
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="a.txt"`) {
		t.Fatalf("disposition = %q", resp.Header.Get("Content-Disposition"))
	}
	if resp, _ := e.do(t, "GET", "/api/v1/files/999", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("missing file status = %d", resp.StatusCode)
	}

	// New content -> one version; download it and restore it.
	e.src.content["1"] = "changed"
	e.sync(t, up("1", "a.txt", "c1b"))
	_, b = e.do(t, "GET", "/api/v1/files/"+id+"/versions", nil, nil)
	vs := decode[[]VersionJSON](t, b)
	if len(vs) != 1 {
		t.Fatalf("versions = %s", b)
	}
	if _, b := e.do(t, "GET", "/api/v1/files/"+id+"/content?version="+vs[0].ID, nil, nil); string(b) != "hello world" {
		t.Fatalf("version content = %q", b)
	}
	resp, _ = e.do(t, "POST", "/api/v1/files/"+id+"/restore", map[string]string{"version": vs[0].ID}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("restore version status = %d", resp.StatusCode)
	}
	if _, b := e.do(t, "GET", "/api/v1/files/"+id+"/content", nil, nil); string(b) != "hello world" {
		t.Fatalf("content after restore = %q", b)
	}

	// Restore of a live file from trash conflicts.
	if resp, _ := e.do(t, "POST", "/api/v1/files/"+id+"/restore", nil, nil); resp.StatusCode != 409 {
		t.Fatalf("restore live status = %d", resp.StatusCode)
	}

	// Delete -> trash -> restore.
	e.sync(t, source.Change{Kind: source.Deleted, Item: source.Item{SourceID: "1"}})
	_, b = e.do(t, "GET", "/api/v1/trash", nil, nil)
	if tr := decode[[]FileJSON](t, b); len(tr) != 1 || tr[0].DeletedAt == nil {
		t.Fatalf("trash = %s", b)
	}
	if _, b := e.do(t, "GET", "/api/v1/files/"+id+"/content", nil, nil); string(b) != "hello world" {
		t.Fatalf("trashed content = %q", b)
	}
	resp, b = e.do(t, "POST", "/api/v1/files/"+id+"/restore", map[string]string{"path": "../escape"}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("escape restore = %d %s", resp.StatusCode, b)
	}
	resp, b = e.do(t, "POST", "/api/v1/files/"+id+"/restore", map[string]string{}, nil)
	if f := decode[FileJSON](t, b); resp.StatusCode != 200 || f.DeletedAt != nil || f.Path != "a.txt" {
		t.Fatalf("restore trash = %d %s", resp.StatusCode, b)
	}
}

func TestFilesAPIRequiresAuth(t *testing.T) {
	e := newFileEnv(t)
	for _, p := range []string{"/api/v1/sources", "/api/v1/files?source=mem", "/api/v1/trash", "/api/v1/files/1/content"} {
		resp, err := http.Get(e.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s: status = %d, want 401", p, resp.StatusCode)
		}
	}
}

func TestContentDisposition(t *testing.T) {
	got := contentDisposition(`报告 "Q3".pdf`)
	want := `attachment; filename="__ _Q3_.pdf"; filename*=UTF-8''%E6%8A%A5%E5%91%8A%20%22Q3%22.pdf`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
