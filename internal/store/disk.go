package store

// DiskUsage is the space on the volume that holds the backup root.
type DiskUsage struct {
	Total uint64
	Free  uint64
}

// Used returns the bytes in use on the volume.
func (d DiskUsage) Used() uint64 {
	if d.Free > d.Total {
		return 0
	}
	return d.Total - d.Free
}
