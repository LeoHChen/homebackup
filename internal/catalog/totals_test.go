package catalog

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSourceTotals(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	now := time.Now()
	put := func(src, item, p string, size int64) File {
		f, err := c.PutFile(ctx, File{Source: src, SourceItemID: item, Path: p, Size: size, Modified: now, SHA256: item}, "", now)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if got, err := c.SourceTotals(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty catalog: %v, %v", got, err)
	}
	a := put("files", "a", "a.txt", 10)
	put("files", "b", "b.txt", 20)
	put("iphone", "c", "2026/11/c.jpg", 30)
	if _, err := c.PutFolder(ctx, "files", "docs", func() string { return "dir1" }, now); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkDeleted(ctx, a.ID, "trash/a.txt", now); err != nil {
		t.Fatal(err)
	}
	got, err := c.SourceTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Totals{
		"files":  {Files: 1, Bytes: 20, TrashedFiles: 1, TrashedBytes: 10},
		"iphone": {Files: 1, Bytes: 30},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("totals = %+v, want %+v", got, want)
	}
}
