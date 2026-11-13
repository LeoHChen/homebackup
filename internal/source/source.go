// Package source defines the interface implemented by every backup source
// (Google Drive, Google Photos Takeout, device uploads, local iCloud Drive).
package source

import (
	"context"
	"io"
	"time"
)

// ChangeKind describes what happened to an item.
type ChangeKind int

const (
	// Upserted means the item is new or its content/metadata changed.
	Upserted ChangeKind = iota
	// Deleted means the item was removed at the source.
	Deleted
)

// Item is one file known to a source.
type Item struct {
	// SourceID is the stable identifier of the item at the source
	// (e.g. a Drive file ID). It must not change when the item is renamed.
	SourceID string
	// Path is the item's path relative to the source root, using '/'.
	Path     string
	Size     int64
	Modified time.Time
	// Checksum is an optional source-provided checksum (e.g. "md5:...").
	Checksum string
}

// Change is a single change reported by a source.
type Change struct {
	Kind ChangeKind
	Item Item
}

// ChangeSet is a batch of changes plus the cursor to resume from.
type ChangeSet struct {
	Changes    []Change
	NextCursor string
	// More is true when the caller should call Changes again immediately.
	More bool
}

// Source is implemented by every backup source.
type Source interface {
	// ID returns the configured source ID, used as the directory name in the store.
	ID() string
	// Changes returns items changed since cursor. An empty cursor means a full listing.
	Changes(ctx context.Context, cursor string) (ChangeSet, error)
	// Open streams the content of one item.
	Open(ctx context.Context, item Item) (io.ReadCloser, error)
}
