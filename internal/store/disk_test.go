package store

import "testing"

func TestDiskUsage(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.DiskUsage()
	if err != nil {
		t.Fatal(err)
	}
	if d.Total == 0 || d.Free > d.Total {
		t.Fatalf("usage = %+v", d)
	}
	if d.Used() != d.Total-d.Free {
		t.Fatalf("used = %d, want %d", d.Used(), d.Total-d.Free)
	}
	if (DiskUsage{Total: 1, Free: 2}).Used() != 0 {
		t.Fatal("used must not underflow")
	}
}
