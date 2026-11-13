package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/store"
)

// WritableType is the source type whose files are edited through the API
// (the home server is the primary copy, like a cloud drive).
const WritableType = "drive"

// ErrReadOnly is returned when editing a source that mirrors a cloud service.
var ErrReadOnly = errors.New("source is read-only (it mirrors another service)")

// NewItemID returns a random item ID for files and folders created locally.
func NewItemID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "local:" + hex.EncodeToString(b)
}

func (e *Engine) writable(source string) error {
	typ, ok := e.SourceType(source)
	if !ok {
		return ErrUnknownSource
	}
	if typ != WritableType {
		return ErrReadOnly
	}
	return nil
}

// MkDir creates a folder (and missing parents) in a writable source.
func (e *Engine) MkDir(ctx context.Context, source, p string) (catalog.File, error) {
	if err := e.writable(source); err != nil {
		return catalog.File{}, err
	}
	p = strings.Trim(p, "/")
	if err := e.Store.MkdirAll(source, p); err != nil {
		return catalog.File{}, err
	}
	return e.Catalog.PutFolder(ctx, source, p, NewItemID, e.Now())
}

func (e *Engine) ensureParents(ctx context.Context, source, p string) error {
	dir := path.Dir(p)
	if dir == "." || dir == "" {
		return nil
	}
	_, err := e.Catalog.PutFolder(ctx, source, dir, NewItemID, e.Now())
	return err
}

// MoveItem renames or moves a file or folder within a writable source.
func (e *Engine) MoveItem(ctx context.Context, id int64, newPath string) (catalog.File, error) {
	f, err := e.Catalog.GetFile(ctx, id)
	if err != nil {
		return catalog.File{}, err
	}
	if f.DeletedAt != nil {
		return catalog.File{}, catalog.ErrNotFound
	}
	if err := e.writable(f.Source); err != nil {
		return catalog.File{}, err
	}
	newPath = strings.Trim(newPath, "/")
	dst, err := e.Store.DataPath(f.Source, newPath)
	if err != nil {
		return catalog.File{}, err
	}
	if newPath == f.Path {
		return f, nil
	}
	if f.IsDir && strings.HasPrefix(newPath+"/", f.Path+"/") {
		return catalog.File{}, fmt.Errorf("%w: cannot move a folder into itself", store.ErrInvalidPath)
	}
	if _, err := os.Stat(dst); err == nil {
		return catalog.File{}, store.ErrExists
	}
	if _, err := e.Catalog.LiveFileByPath(ctx, f.Source, newPath); err == nil {
		return catalog.File{}, store.ErrExists
	}
	if err := e.ensureParents(ctx, f.Source, newPath); err != nil {
		return catalog.File{}, err
	}
	if f.IsDir {
		if err := e.Store.MoveDir(f.Source, f.Path, newPath); err != nil {
			return catalog.File{}, err
		}
		if err := e.Catalog.MoveTree(ctx, f.Source, f.Path, newPath); err != nil {
			return catalog.File{}, err
		}
	} else {
		if err := e.Store.Move(f.Source, f.Path, newPath); err != nil {
			return catalog.File{}, err
		}
		if err := e.Catalog.MoveFile(ctx, f.ID, newPath); err != nil {
			return catalog.File{}, err
		}
	}
	return e.Catalog.GetFile(ctx, f.ID)
}

// DeleteItem moves a file, or a folder and everything in it, to the trash.
func (e *Engine) DeleteItem(ctx context.Context, id int64) error {
	f, err := e.Catalog.GetFile(ctx, id)
	if err != nil {
		return err
	}
	if f.DeletedAt != nil {
		return nil
	}
	if err := e.writable(f.Source); err != nil {
		return err
	}
	if !f.IsDir {
		return e.applyDelete(ctx, f.Source, sourceItem(f))
	}
	below, err := e.Catalog.LiveFilesUnder(ctx, f.Source, f.Path)
	if err != nil {
		return err
	}
	// Files first, then folders (deepest last in path order, so reverse).
	for _, d := range below {
		if !d.IsDir {
			if err := e.applyDelete(ctx, f.Source, sourceItem(d)); err != nil {
				return err
			}
		}
	}
	now := e.Now()
	for i := len(below) - 1; i >= 0; i-- {
		if below[i].IsDir {
			if err := e.Catalog.MarkDeleted(ctx, below[i].ID, "", now); err != nil {
				return err
			}
		}
	}
	if err := e.Catalog.MarkDeleted(ctx, f.ID, "", now); err != nil {
		return err
	}
	return e.Store.RemoveEmptyDirs(f.Source, f.Path)
}
