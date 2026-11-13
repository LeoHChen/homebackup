// Package device is the source for files pushed by paired devices (e.g. the
// iPhone app's photo backup) through the upload API. It has nothing to pull.
package device

import (
	"context"
	"errors"
	"io"

	"github.com/LeoHChen/homebackup/internal/source"
)

// Source is a push-only source.
type Source struct{ id string }

// New returns a device-upload source.
func New(id string) *Source { return &Source{id: id} }

// ID implements source.Source.
func (s *Source) ID() string { return s.id }

// Changes implements source.Source; uploads arrive through the API instead.
func (s *Source) Changes(_ context.Context, cursor string) (source.ChangeSet, error) {
	return source.ChangeSet{NextCursor: cursor}, nil
}

// Open implements source.Source.
func (s *Source) Open(context.Context, source.Item) (io.ReadCloser, error) {
	return nil, errors.New("device-upload sources receive files through the upload API")
}
