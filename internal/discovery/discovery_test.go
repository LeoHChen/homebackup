package discovery

import (
	"context"
	"runtime"
	"slices"
	"testing"
)

func TestCommand(t *testing.T) {
	cmd := Command(context.Background(), "homebackup on mini", 8420, []string{"v=1", "fp=abc"})
	switch runtime.GOOS {
	case "darwin":
		want := []string{"dns-sd", "-R", "homebackup on mini", "_homebackup._tcp", "local", "8420", "v=1", "fp=abc"}
		if cmd == nil || !slices.Equal(cmd.Args, want) {
			t.Fatalf("args = %v", cmd)
		}
	case "linux":
		if cmd != nil && !slices.Contains(cmd.Args, "_homebackup._tcp") {
			t.Fatalf("args = %v", cmd.Args)
		}
	}
}
