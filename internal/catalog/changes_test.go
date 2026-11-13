package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func ids() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("dir-%d", n) }
}

func TestChangeFeed(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	a := put(t, c, "a", "a.txt", "s1", "")
	b := put(t, c, "b", "docs/b.txt", "s1", "")

	all, more, err := c.Changes(ctx, "gdrive", 0, 100)
	if err != nil || more || len(all) != 2 {
		t.Fatalf("initial = %+v %v %v", all, more, err)
	}
	cursor := all[len(all)-1].Seq

	// Nothing new.
	if chs, _, _ := c.Changes(ctx, "gdrive", cursor, 100); len(chs) != 0 {
		t.Fatalf("expected no changes, got %+v", chs)
	}

	// Edit a, delete b to trash, purge a: feed shows the latest state of each once.
	put(t, c, "a", "a.txt", "s2", "/v/1")
	c.MarkDeleted(ctx, b.ID, "/trash/b", t0)
	chs, _, _ := c.Changes(ctx, "gdrive", cursor, 100)
	if len(chs) != 2 || chs[0].File.ID != a.ID || chs[0].File.SHA256 != "s2" || chs[1].File.DeletedAt == nil {
		t.Fatalf("changes = %+v", chs)
	}
	cursor = chs[1].Seq
	c.DeleteFile(ctx, a.ID)
	chs, _, _ = c.Changes(ctx, "gdrive", cursor, 100)
	if len(chs) != 1 || !chs[0].Removed || chs[0].File.ID != a.ID || chs[0].File.Path != "a.txt" {
		t.Fatalf("tombstone = %+v", chs)
	}
	latest, _ := c.LatestSeq(ctx)
	if latest != chs[0].Seq {
		t.Fatalf("latest = %d, want %d", latest, chs[0].Seq)
	}

	// Paging.
	for i := 0; i < 5; i++ {
		put(t, c, fmt.Sprint("p", i), fmt.Sprintf("p%d", i), "x", "")
	}
	page, more, _ := c.Changes(ctx, "gdrive", latest, 3)
	if len(page) != 3 || !more {
		t.Fatalf("page 1 = %d more=%v", len(page), more)
	}
	page2, more, _ := c.Changes(ctx, "gdrive", page[2].Seq, 3)
	if len(page2) != 2 || more {
		t.Fatalf("page 2 = %d more=%v", len(page2), more)
	}
	// Other sources are separate.
	if chs, _, _ := c.Changes(ctx, "other", 0, 10); len(chs) != 0 {
		t.Fatalf("other source = %+v", chs)
	}
}

func TestFoldersAndMoveTree(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	newID := ids()
	f, err := c.PutFolder(ctx, "gdrive", "Work/Projects", newID, t0)
	if err != nil || !f.IsDir || f.Path != "Work/Projects" {
		t.Fatalf("folder = %+v %v", f, err)
	}
	again, err := c.PutFolder(ctx, "gdrive", "Work/Projects", newID, t0)
	if err != nil || again.ID != f.ID {
		t.Fatalf("PutFolder must be idempotent: %+v %v", again, err)
	}
	put(t, c, "x", "Work/Projects/plan.txt", "s", "")
	put(t, c, "y", "Work2/other.txt", "s", "")

	es, _ := c.ListDir(ctx, "gdrive", "")
	if got := names(es); !equal(got, []string{"Work/", "Work2/"}) {
		t.Fatalf("root = %v", got)
	}
	if es[0].File == nil || !es[0].File.IsDir {
		t.Fatal("explicit folder should carry its row")
	}
	es, _ = c.ListDir(ctx, "gdrive", "Work")
	if got := names(es); !equal(got, []string{"Projects/"}) {
		t.Fatalf("Work = %v", got)
	}
	// Empty folders are listed too.
	c.PutFolder(ctx, "gdrive", "Empty", newID, t0)
	es, _ = c.ListDir(ctx, "gdrive", "")
	if got := names(es); !equal(got, []string{"Empty/", "Work/", "Work2/"}) {
		t.Fatalf("root with empty = %v", got)
	}

	if err := c.MoveTree(ctx, "gdrive", "Work", "Archive/Work"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LiveFileByPath(ctx, "gdrive", "Archive/Work/Projects/plan.txt"); err != nil {
		t.Fatal("descendant not moved")
	}
	if _, err := c.LiveFileByPath(ctx, "gdrive", "Work2/other.txt"); err != nil {
		t.Fatal("sibling with shared prefix must not move")
	}
	if _, err := c.PutFolder(ctx, "gdrive", "Work2/other.txt/sub", newID, t0); !errors.Is(err, ErrExists) {
		t.Fatalf("folder under a file: err = %v", err)
	}
}

func TestMigrateOldDatabase(t *testing.T) {
	p := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE files (
		id INTEGER PRIMARY KEY, source TEXT NOT NULL, source_item_id TEXT NOT NULL, path TEXT NOT NULL,
		dir TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL, sha256 TEXT NOT NULL,
		source_checksum TEXT NOT NULL DEFAULT '', deleted_at INTEGER, trash_path TEXT NOT NULL DEFAULT '',
		UNIQUE (source, source_item_id));
		INSERT INTO files (source, source_item_id, path, dir, size, modified, sha256) VALUES ('s', 'i', 'a', '', 1, 0, 'x');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	chs, _, err := c.Changes(ctx, "s", 0, 10)
	if err != nil || len(chs) != 1 || chs[0].Seq == 0 {
		t.Fatalf("migrated changes = %+v %v", chs, err)
	}
	f, err := c.PutFile(ctx, File{Source: "s", SourceItemID: "j", Path: "b", Modified: time.Now(), SHA256: "y"}, "", time.Now())
	if err != nil || f.ID == 0 {
		t.Fatal(err)
	}
	chs, _, _ = c.Changes(ctx, "s", chs[0].Seq, 10)
	if len(chs) != 1 || chs[0].File.Path != "b" {
		t.Fatalf("new row after migration = %+v", chs)
	}
}
