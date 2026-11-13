//go:build windows

package store

import "golang.org/x/sys/windows"

// DiskUsage reports the size and free space of the volume holding the root.
func (s *Store) DiskUsage() (DiskUsage, error) {
	p, err := windows.UTF16PtrFromString(s.root)
	if err != nil {
		return DiskUsage{}, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return DiskUsage{}, err
	}
	return DiskUsage{Total: total, Free: free}, nil
}
