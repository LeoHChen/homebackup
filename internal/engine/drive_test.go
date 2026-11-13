package engine

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/source"
	"github.com/LeoHChen/homebackup/internal/sources/device"
	"github.com/LeoHChen/homebackup/internal/store"
)

func newDrive(t *testing.T) *env {
	t.Helper()
	v := newEnv(t)
	v.e.Register(device.New("files"), WritableType, 0)
	return v
}

func (v *env) putDrive(t *testing.T, p, body string) catalog.File {
	t.Helper()
	ctx := context.Background()
	it := source.Item{SourceID: NewItemID(), Path: p, Modified: v.clock}
	if err := v.e.Put(ctx, "files", it, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	f, err := v.e.Catalog.FileBySourceItem(ctx, "files", it.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (v *env) driveFile(t *testing.T, p string) string {
	t.Helper()
	dp, _ := v.e.Store.DataPath("files", p)
	b, err := os.ReadFile(dp)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return string(b)
}

func TestDriveOperations(t *testing.T) {
	v := newDrive(t)
	ctx := context.Background()

	// Uploading into a new folder creates folder rows for the parents.
	doc := v.putDrive(t, "Work/Plans/plan.txt", "v1")
	work, err := v.e.Catalog.LiveFileByPath(ctx, "files", "Work")
	if err != nil || !work.IsDir {
		t.Fatalf("parent folder row: %+v %v", work, err)
	}
	empty, err := v.e.MkDir(ctx, "files", "Empty/Nested")
	if err != nil || !empty.IsDir {
		t.Fatal(err)
	}
	if dp, _ := v.e.Store.DataPath("files", "Empty/Nested"); !isDir(dp) {
		t.Fatal("folder not created on disk")
	}

	// Rename a file.
	got, err := v.e.MoveItem(ctx, doc.ID, "Work/Plans/plan-2026.txt")
	if err != nil || got.Path != "Work/Plans/plan-2026.txt" || v.driveFile(t, got.Path) != "v1" {
		t.Fatalf("rename: %+v %v", got, err)
	}
	// Move a folder with its contents.
	if _, err := v.e.MoveItem(ctx, work.ID, "Archive/Work"); err != nil {
		t.Fatal(err)
	}
	if v.driveFile(t, "Archive/Work/Plans/plan-2026.txt") != "v1" {
		t.Fatal("folder move lost contents")
	}
	moved, _ := v.e.Catalog.GetFile(ctx, doc.ID)
	if moved.Path != "Archive/Work/Plans/plan-2026.txt" {
		t.Fatalf("catalog path = %s", moved.Path)
	}
	// Conflicts and invalid moves.
	other := v.putDrive(t, "x.txt", "x")
	if _, err := v.e.MoveItem(ctx, other.ID, "Archive/Work/Plans/plan-2026.txt"); !errors.Is(err, store.ErrExists) {
		t.Fatalf("move onto existing: %v", err)
	}
	if _, err := v.e.MoveItem(ctx, work.ID, "Archive/Work/Plans/inside"); !errors.Is(err, store.ErrInvalidPath) {
		t.Fatalf("move into itself: %v", err)
	}
	if _, err := v.e.MoveItem(ctx, other.ID, "../escape"); !errors.Is(err, store.ErrInvalidPath) {
		t.Fatalf("escape: %v", err)
	}

	// Delete a folder: files to trash, folders marked deleted, disk cleaned.
	if err := v.e.DeleteItem(ctx, work.ID); err != nil {
		t.Fatal(err)
	}
	if dp, _ := v.e.Store.DataPath("files", "Archive/Work"); isDir(dp) {
		t.Fatal("deleted folder still on disk")
	}
	tr, _ := v.e.Catalog.ListTrash(ctx, "files")
	var trashedFile bool
	for _, f := range tr {
		if f.ID == doc.ID && f.TrashPath != "" {
			trashedFile = true
		}
	}
	if !trashedFile {
		t.Fatalf("file not in trash: %+v", tr)
	}
	es, _ := v.e.Catalog.ListDir(ctx, "files", "Archive")
	if len(es) != 0 {
		t.Fatalf("Archive should be empty: %+v", es)
	}
	// Read-only sources reject edits.
	v.src.content["1"] = "x"
	v.sync(t, []source.Change{up("1", "ro.txt", "c")})
	ro, _ := v.e.Catalog.FileBySourceItem(ctx, "fake", "1")
	if err := v.e.DeleteItem(ctx, ro.ID); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only delete: %v", err)
	}
	if _, err := v.e.MkDir(ctx, "fake", "x"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only mkdir: %v", err)
	}
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
