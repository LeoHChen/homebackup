package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)

func open(t *testing.T) *Catalog {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func put(t *testing.T, c *Catalog, item, p, sha, prev string) File {
	t.Helper()
	f, err := c.PutFile(context.Background(), File{
		Source: "gdrive", SourceItemID: item, Path: p, Size: 1, Modified: t0, SHA256: sha,
	}, prev, t0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func names(es []Entry) []string {
	var out []string
	for _, e := range es {
		if e.IsDir {
			out = append(out, e.Name+"/")
		} else {
			out = append(out, e.Name)
		}
	}
	return out
}

func TestPutFileRecordsVersions(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	f1 := put(t, c, "id1", "a.txt", "s1", "")
	f2 := put(t, c, "id1", "a.txt", "s2", "/v/1")
	if f1.ID != f2.ID {
		t.Fatalf("same source item got new id: %d vs %d", f1.ID, f2.ID)
	}
	got, err := c.GetFile(ctx, f1.ID)
	if err != nil || got.SHA256 != "s2" || !got.Modified.Equal(t0) {
		t.Fatalf("GetFile = %+v, %v", got, err)
	}
	vs, err := c.ListVersions(ctx, f1.ID)
	if err != nil || len(vs) != 1 || vs[0].SHA256 != "s1" || vs[0].StoredPath != "/v/1" {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	if _, err := c.GetFile(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListDir(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	put(t, c, "1", "top.txt", "x", "")
	put(t, c, "2", "docs/a.txt", "x", "")
	put(t, c, "3", "docs/sub/b.txt", "x", "")
	put(t, c, "4", "docs0/c.txt", "x", "") // must not leak into "docs"
	put(t, c, "5", "图片/d.jpg", "x", "")
	put(t, c, "6", "图片/子/e.jpg", "x", "")
	gone := put(t, c, "7", "old/z.txt", "x", "")
	if err := c.MarkDeleted(ctx, gone.ID, "/trash/z", t0); err != nil {
		t.Fatal(err)
	}

	for dir, want := range map[string][]string{
		"":     {"docs/", "docs0/", "图片/", "top.txt"},
		"docs": {"sub/", "a.txt"},
		"图片":   {"子/", "d.jpg"},
		"old":  nil,
	} {
		es, err := c.ListDir(ctx, "gdrive", dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(es); !equal(got, want) {
			t.Errorf("ListDir(%q) = %v, want %v", dir, got, want)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTrashLifecycle(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	f := put(t, c, "1", "a.txt", "x", "")
	if err := c.MarkDeleted(ctx, f.ID, "/trash/a", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LiveFileByPath(ctx, "gdrive", "a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted file still live: %v", err)
	}
	tr, _ := c.ListTrash(ctx, "")
	if len(tr) != 1 || tr[0].TrashPath != "/trash/a" {
		t.Fatalf("trash = %+v", tr)
	}
	old, _ := c.TrashedBefore(ctx, t0.Add(time.Hour))
	none, _ := c.TrashedBefore(ctx, t0)
	if len(old) != 1 || len(none) != 0 {
		t.Fatalf("TrashedBefore: %d, %d", len(old), len(none))
	}
	if err := c.Undelete(ctx, f.ID, "restored/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LiveFileByPath(ctx, "gdrive", "restored/a.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredVersionsKeepsWhicheverIsMore(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	f := put(t, c, "1", "a.txt", "v0", "")
	// 5 versions created at t0+0h .. t0+4h.
	for i := 0; i < 5; i++ {
		_, err := c.PutFile(ctx, File{Source: "gdrive", SourceItemID: "1", Path: "a.txt", Modified: t0, SHA256: "v"},
			"/v/"+string(rune('a'+i)), t0.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	// Older than t0+3h: versions a,b,c. Keep newest 3: c,d,e. Expired = a,b.
	exp, err := c.ExpiredVersions(ctx, t0.Add(3*time.Hour), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(exp) != 2 || exp[0].FileID != f.ID {
		t.Fatalf("expired = %+v", exp)
	}
	for _, v := range exp {
		if v.StoredPath != "/v/a" && v.StoredPath != "/v/b" {
			t.Errorf("unexpected expired version %s", v.StoredPath)
		}
		c.DeleteVersion(ctx, v.ID)
	}
	vs, _ := c.ListVersions(ctx, f.ID)
	if len(vs) != 3 || vs[0].StoredPath != "/v/e" {
		t.Fatalf("remaining = %+v", vs)
	}
}

func TestSourceState(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	s, err := c.SourceState(ctx, "gdrive")
	if err != nil || s.Cursor != "" || s.LastSync != nil {
		t.Fatalf("fresh state = %+v, %v", s, err)
	}
	c.SetCursor(ctx, "gdrive", "tok1")
	c.RecordSync(ctx, "gdrive", t0, errors.New("boom"))
	s, _ = c.SourceState(ctx, "gdrive")
	if s.Cursor != "tok1" || s.LastSync == nil || !s.LastSync.Equal(t0) || s.LastError != "boom" {
		t.Fatalf("state = %+v", s)
	}
	c.RecordSync(ctx, "gdrive", t0, nil)
	s, _ = c.SourceState(ctx, "gdrive")
	if s.LastError != "" || s.Cursor != "tok1" {
		t.Fatalf("state after success = %+v", s)
	}
}
