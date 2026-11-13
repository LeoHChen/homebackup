package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	s.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	return s
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPutKeepsPreviousVersions(t *testing.T) {
	s := newStore(t)
	res1, err := s.Put("gdrive", "docs/a.txt", strings.NewReader("one"))
	if err != nil {
		t.Fatal(err)
	}
	res2, err := s.Put("gdrive", "docs/a.txt", strings.NewReader("two"))
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := s.DataPath("gdrive", "docs/a.txt")
	if got := read(t, cur); got != "two" {
		t.Fatalf("current = %q, want two", got)
	}
	vers, err := os.ReadDir(filepath.Join(s.Root(), "versions", "gdrive", "docs", "a.txt"))
	if err != nil || len(vers) != 1 {
		t.Fatalf("versions = %v, %v; want 1 entry", vers, err)
	}
	old := filepath.Join(s.Root(), "versions", "gdrive", "docs", "a.txt", vers[0].Name())
	if got := read(t, old); got != "one" {
		t.Fatalf("old version = %q, want one", got)
	}
	if res2.PreviousVersion != old || res1.PreviousVersion != "" || res1.Size != 3 {
		t.Errorf("results = %+v / %+v", res1, res2)
	}
	const sha256One = "7692c3ad3540bb803c020b3aee66cd8887123234ea0c6e7143c0add73ff431ed"
	if res1.SHA256 != sha256One {
		t.Errorf("sum = %s, want %s", res1.SHA256, sha256One)
	}
}

func TestDeleteMovesToTrash(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put("gphotos", "2026/11/img.jpg", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	dst, err := s.Delete("gphotos", "2026/11/img.jpg")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.Root(), "trash", "gphotos", "2026-11-13", "2026", "11", "img.jpg")
	if dst != want {
		t.Fatalf("trash path = %s, want %s", dst, want)
	}
	cur, _ := s.DataPath("gphotos", "2026/11/img.jpg")
	if _, err := os.Stat(cur); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current file still exists: %v", err)
	}

	// Deleting the same path again on the same day keeps both copies.
	if _, err := s.Put("gphotos", "2026/11/img.jpg", strings.NewReader("y")); err != nil {
		t.Fatal(err)
	}
	dst2, err := s.Delete("gphotos", "2026/11/img.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if dst2 == dst || read(t, dst) != "x" || read(t, dst2) != "y" {
		t.Fatalf("second delete overwrote the first: %s %s", dst, dst2)
	}
}

func TestRejectsEscapingPaths(t *testing.T) {
	s := newStore(t)
	for _, tc := range []struct{ src, rel string }{
		{"gdrive", "../etc/passwd"},
		{"gdrive", "/abs"},
		{"gdrive", "a/../../b"},
		{"gdrive", ""},
		{"gdrive", `a\b`},
		{"../x", "a"},
		{"", "a"},
	} {
		if _, err := s.Put(tc.src, tc.rel, strings.NewReader("")); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Put(%q, %q) err = %v, want ErrInvalidPath", tc.src, tc.rel, err)
		}
	}
}

func TestRestoreAndPurge(t *testing.T) {
	s := newStore(t)
	s.Put("gdrive", "a.txt", strings.NewReader("v1"))
	res, _ := s.Put("gdrive", "a.txt", strings.NewReader("v2"))

	// Restore v1: v2 becomes a version.
	r, err := s.RestoreVersion("gdrive", "a.txt", res.PreviousVersion)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := s.DataPath("gdrive", "a.txt")
	if read(t, cur) != "v1" || read(t, r.PreviousVersion) != "v2" {
		t.Fatal("restore version did not swap contents")
	}

	trashed, _ := s.Delete("gdrive", "a.txt")
	s.Put("gdrive", "a.txt", strings.NewReader("new"))
	if err := s.RestoreTrash("gdrive", "a.txt", trashed); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if err := s.RestoreTrash("gdrive", "b.txt", trashed); err != nil {
		t.Fatal(err)
	}
	b, _ := s.DataPath("gdrive", "b.txt")
	if read(t, b) != "v1" {
		t.Fatal("restored trash content mismatch")
	}

	if err := s.Purge(b); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("purging a current file must fail, got %v", err)
	}
	if err := s.Purge(r.PreviousVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.PreviousVersion); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("version not purged")
	}
	if _, err := s.RestoreVersion("gdrive", "a.txt", "/etc/passwd"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("restore from outside versions must fail, got %v", err)
	}
}

func TestMove(t *testing.T) {
	s := newStore(t)
	s.Put("gdrive", "old/a.txt", strings.NewReader("x"))
	if err := s.Move("gdrive", "old/a.txt", "new/b.txt"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.DataPath("gdrive", "new/b.txt")
	if read(t, p) != "x" {
		t.Fatal("move failed")
	}
}
