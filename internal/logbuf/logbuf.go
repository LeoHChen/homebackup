// Package logbuf keeps the most recent log lines in memory, so the dashboard
// and the apps can show what the server has been doing without access to its
// log file.
package logbuf

import (
	"bytes"
	"sync"
)

// maxPartial caps a line that never ends in a newline.
const maxPartial = 64 * 1024

// Ring is an io.Writer that remembers the last N complete lines.
type Ring struct {
	mu      sync.Mutex
	lines   []string
	max     int
	partial []byte
}

// New returns a Ring that keeps up to max lines.
func New(max int) *Ring {
	if max < 1 {
		max = 1
	}
	return &Ring{max: max}
}

// Write implements io.Writer. Text up to the last newline becomes lines; the
// rest is kept until a later write completes it.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		r.add(string(r.partial[:i]))
		r.partial = append([]byte(nil), r.partial[i+1:]...)
	}
	if len(r.partial) > maxPartial {
		r.add(string(r.partial))
		r.partial = nil
	}
	return len(p), nil
}

func (r *Ring) add(line string) {
	if len(r.lines) == r.max {
		copy(r.lines, r.lines[1:])
		r.lines = r.lines[:r.max-1]
	}
	r.lines = append(r.lines, line)
}

// Lines returns the most recent n lines, oldest first. n <= 0 returns all.
func (r *Ring) Lines(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}
