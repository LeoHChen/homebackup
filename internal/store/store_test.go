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
	sum1, err := s.Put("gdrive", "docs/a.txt", strings.NewReader("one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("gdrive", "docs/a.txt", strings.NewReader("two")); err != nil {
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
	const sha256One = "7692c3ad3540bb803c020b3aee66cd8887123234ea0c6e7143c0add73ff431ed"
	if sum1 != sha256One {
		t.Errorf("sum = %s, want %s", sum1, sha256One)
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
