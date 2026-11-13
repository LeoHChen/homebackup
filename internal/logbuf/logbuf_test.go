package logbuf

import (
	"fmt"
	"slices"
	"testing"
)

func TestRingKeepsLastLines(t *testing.T) {
	r := New(3)
	fmt.Fprint(r, "a\nb\n")
	fmt.Fprint(r, "c")
	if got := r.Lines(0); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("partial line must wait: %q", got)
	}
	fmt.Fprint(r, "1\nd\n")
	if got, want := r.Lines(0), []string{"b", "c1", "d"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if got := r.Lines(2); !slices.Equal(got, []string{"c1", "d"}) {
		t.Fatalf("last 2 = %q", got)
	}
	if got := r.Lines(10); len(got) != 3 {
		t.Fatalf("asking for more than stored = %q", got)
	}
}

func TestRingEmpty(t *testing.T) {
	if got := New(0).Lines(5); len(got) != 0 {
		t.Fatalf("empty ring = %q", got)
	}
}
