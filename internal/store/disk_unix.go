//go:build !windows

package store

import "syscall"

// DiskUsage reports the size and free space of the volume holding the root.
func (s *Store) DiskUsage() (DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.root, &st); err != nil {
		return DiskUsage{}, err
	}
	bsize := uint64(st.Bsize)
	return DiskUsage{Total: uint64(st.Blocks) * bsize, Free: uint64(st.Bavail) * bsize}, nil
}
