// Package sources builds source implementations from configuration.
package sources

import (
	"errors"
	"fmt"

	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/source"
)

// ErrNotImplemented is returned for source types that are planned but not built yet.
var ErrNotImplemented = errors.New("source type not implemented")

// New returns the source for a configuration entry.
func New(c config.Source) (source.Source, error) {
	switch c.Type {
	case "gdrive", "gphotos-takeout", "device-upload", "icloud-local":
		return nil, ErrNotImplemented
	default:
		return nil, fmt.Errorf("unknown source type %q", c.Type)
	}
}
