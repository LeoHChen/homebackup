// Package engine applies changes from sources to the store and catalog,
// runs scheduled syncs, and purges expired trash and versions.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/source"
	"github.com/LeoHChen/homebackup/internal/store"
)

// UploadExpiry is how long an unfinished upload session is kept.
const UploadExpiry = 7 * 24 * time.Hour

// ErrUnknownSource is returned for a source ID that is not registered.
var ErrUnknownSource = errors.New("unknown source")

// ErrPathConflict is returned when an item's path and its disambiguated
// alternative are both used by other items.
var ErrPathConflict = errors.New("path used by another item")

// ErrBusy is returned when a sync of the same source is already running.
var ErrBusy = errors.New("sync already running")

// Engine coordinates sources, the store, and the catalog.
type Engine struct {
	Store     *store.Store
	Catalog   *catalog.Catalog
	Retention config.Retention
	Log       *slog.Logger
	Now       func() time.Time

	mu      sync.Mutex
	sources map[string]registered
	running map[string]bool
}

type registered struct {
	src      source.Source
	typ      string
	schedule time.Duration
	trigger  chan struct{}
}

// New returns an Engine.
func New(st *store.Store, cat *catalog.Catalog, ret config.Retention, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Engine{
		Store: st, Catalog: cat, Retention: ret, Log: log, Now: time.Now,
		sources: map[string]registered{},
		running: map[string]bool{},
	}
}

// Register adds a source. schedule <= 0 means the source only syncs on demand.
func (e *Engine) Register(src source.Source, typ string, schedule time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sources[src.ID()] = registered{src: src, typ: typ, schedule: schedule, trigger: make(chan struct{}, 1)}
}

// SourceInfo describes a registered source.
type SourceInfo struct {
	ID       string
	Type     string
	Schedule time.Duration
	Running  bool
	State    catalog.SourceState
}

// Sources returns all registered sources and their sync state.
func (e *Engine) Sources(ctx context.Context) ([]SourceInfo, error) {
	e.mu.Lock()
	infos := make([]SourceInfo, 0, len(e.sources))
	for id, r := range e.sources {
		infos = append(infos, SourceInfo{ID: id, Type: r.typ, Schedule: r.schedule, Running: e.running[id]})
	}
	e.mu.Unlock()
	for i := range infos {
		st, err := e.Catalog.SourceState(ctx, infos[i].ID)
		if err != nil {
			return nil, err
		}
		infos[i].State = st
	}
	sortInfos(infos)
	return infos, nil
}

func sortInfos(s []SourceInfo) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].ID < s[j-1].ID; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// SourceType returns the configured type of a registered source.
func (e *Engine) SourceType(id string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.sources[id]
	return r.typ, ok
}

// Trigger asks the scheduler to sync a source as soon as possible.
func (e *Engine) Trigger(id string) error {
	e.mu.Lock()
	r, ok := e.sources[id]
	e.mu.Unlock()
	if !ok {
		return ErrUnknownSource
	}
	select {
	case r.trigger <- struct{}{}:
	default: // already pending
	}
	return nil
}

// Sync runs one full sync of a source: it pulls changes since the stored
// cursor until the source reports no more, applying each batch and then
// saving the cursor so an interrupted sync resumes where it stopped.
func (e *Engine) Sync(ctx context.Context, id string) (err error) {
	e.mu.Lock()
	r, ok := e.sources[id]
	if ok && e.running[id] {
		e.mu.Unlock()
		return ErrBusy
	}
	if ok {
		e.running[id] = true
	}
	e.mu.Unlock()
	if !ok {
		return ErrUnknownSource
	}
	defer func() {
		e.mu.Lock()
		delete(e.running, id)
		e.mu.Unlock()
		if rerr := e.Catalog.RecordSync(context.WithoutCancel(ctx), id, e.Now(), err); rerr != nil && err == nil {
			err = rerr
		}
	}()

	st, err := e.Catalog.SourceState(ctx, id)
	if err != nil {
		return err
	}
	cursor := st.Cursor
	var applied, failed int
	for {
		cs, err := r.src.Changes(ctx, cursor)
		if err != nil {
			return fmt.Errorf("list changes: %w", err)
		}
		for _, ch := range cs.Changes {
			if err := e.apply(ctx, r.src, ch); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if errors.Is(err, source.ErrPermanent) {
					e.Log.Warn("skipping item", "source", id, "path", ch.Item.Path, "err", err)
					continue
				}
				failed++
				e.Log.Error("apply change", "source", id, "path", ch.Item.Path, "err", err)
				continue
			}
			applied++
		}
		if failed > 0 {
			// Keep the old cursor so failed items are retried next run.
			return fmt.Errorf("%d of %d changes failed; will retry", failed, failed+applied)
		}
		cursor = cs.NextCursor
		if err := e.Catalog.SetCursor(ctx, id, cursor); err != nil {
			return err
		}
		if !cs.More {
			break
		}
	}
	e.Log.Info("sync done", "source", id, "applied", applied)
	return nil
}

func (e *Engine) apply(ctx context.Context, src source.Source, ch source.Change) error {
	switch ch.Kind {
	case source.Deleted:
		return e.applyDelete(ctx, src.ID(), ch.Item)
	case source.Upserted:
		return e.applyUpsert(ctx, src, ch.Item)
	case source.DeletedTree:
		return e.applyTree(ctx, src.ID(), ch.Item.Path, func(f catalog.File) error {
			return e.applyDelete(ctx, src.ID(), source.Item{SourceID: f.SourceItemID})
		})
	case source.MovedTree:
		if ch.OldPath == "" || ch.OldPath == ch.Item.Path {
			return nil
		}
		return e.applyTree(ctx, src.ID(), ch.OldPath, func(f catalog.File) error {
			to := strings.Trim(ch.Item.Path, "/") + strings.TrimPrefix(f.Path, strings.Trim(ch.OldPath, "/"))
			target, err := e.resolvePath(ctx, src.ID(), f.SourceItemID, to)
			if err != nil {
				return err
			}
			if err := e.Store.Move(src.ID(), f.Path, target); err != nil {
				return err
			}
			return e.Catalog.MoveFile(ctx, f.ID, target)
		})
	default:
		return fmt.Errorf("unknown change kind %d", ch.Kind)
	}
}

func (e *Engine) applyTree(ctx context.Context, sourceID, dir string, fn func(catalog.File) error) error {
	if strings.Trim(dir, "/") == "" {
		return fmt.Errorf("refusing tree change on the source root")
	}
	files, err := e.Catalog.LiveFilesUnder(ctx, sourceID, dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range files {
		if err := fn(f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.Path, err))
		}
	}
	return errors.Join(errs...)
}

// resolvePath returns the path to store an item at: want itself if it is free
// or already used by this item, otherwise a stable disambiguated name.
func (e *Engine) resolvePath(ctx context.Context, sourceID, itemID, want string) (string, error) {
	for _, p := range []string{want, disambiguate(want, itemID)} {
		other, err := e.Catalog.LiveFileByPath(ctx, sourceID, p)
		if errors.Is(err, catalog.ErrNotFound) || (err == nil && other.SourceItemID == itemID) {
			return p, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: %s", ErrPathConflict, want)
}

// disambiguate inserts a short, stable hash of the item ID before the extension:
// "dir/report.pdf" -> "dir/report~1a2b3c4d.pdf".
func disambiguate(p, itemID string) string {
	h := sha256.Sum256([]byte(itemID))
	tag := "~" + hex.EncodeToString(h[:4])
	dir, name := path.Split(p)
	ext := path.Ext(name)
	if ext == name { // dotfile like ".env"
		ext = ""
	}
	return dir + strings.TrimSuffix(name, ext) + tag + ext
}

func sourceItem(f catalog.File) source.Item {
	return source.Item{SourceID: f.SourceItemID, Path: f.Path}
}

func (e *Engine) applyDelete(ctx context.Context, sourceID string, it source.Item) error {
	f, err := e.Catalog.FileBySourceItem(ctx, sourceID, it.SourceID)
	if errors.Is(err, catalog.ErrNotFound) || (err == nil && f.DeletedAt != nil) {
		return nil // never seen, or already trashed
	}
	if err != nil {
		return err
	}
	trash, err := e.Store.Delete(sourceID, f.Path)
	if errors.Is(err, os.ErrNotExist) {
		// The local copy is already gone (e.g. removed by hand): just record it.
		e.Log.Warn("deleted item missing locally", "source", sourceID, "path", f.Path)
		trash = ""
	} else if err != nil {
		return err
	}
	return e.Catalog.MarkDeleted(ctx, f.ID, trash, e.Now())
}

func (e *Engine) applyUpsert(ctx context.Context, src source.Source, it source.Item) error {
	old, err := e.Catalog.FileBySourceItem(ctx, src.ID(), it.SourceID)
	known := err == nil && old.DeletedAt == nil
	if err != nil && !errors.Is(err, catalog.ErrNotFound) {
		return err
	}
	if it.Path, err = e.resolvePath(ctx, src.ID(), it.SourceID, it.Path); err != nil {
		return err
	}
	if known && unchanged(old, it) {
		if old.Path == it.Path {
			return nil
		}
		// Rename or move only: no download needed.
		if err := e.Store.Move(src.ID(), old.Path, it.Path); err != nil {
			return err
		}
		return e.Catalog.MoveFile(ctx, old.ID, it.Path)
	}
	if known && old.Path != it.Path {
		if err := e.Store.Move(src.ID(), old.Path, it.Path); err != nil {
			return err
		}
	}
	rc, err := src.Open(ctx, it)
	if err != nil {
		return err
	}
	defer rc.Close()
	return e.Put(ctx, src.ID(), it, rc)
}

// Put stores content for an item and records it in the catalog. It is used by
// pull sources and by push uploads (e.g. the iPhone app).
func (e *Engine) Put(ctx context.Context, sourceID string, it source.Item, r io.Reader) error {
	p, err := e.resolvePath(ctx, sourceID, it.SourceID, it.Path)
	if err != nil {
		return err
	}
	it.Path = p
	if typ, _ := e.SourceType(sourceID); typ == WritableType {
		if err := e.ensureParents(ctx, sourceID, it.Path); err != nil {
			return err
		}
	}
	res, err := e.Store.Put(sourceID, it.Path, r)
	if err != nil {
		return err
	}
	if res.PreviousVersion == "" {
		// An item that comes back after deletion keeps its trashed copy as a version.
		if old, err := e.Catalog.FileBySourceItem(ctx, sourceID, it.SourceID); err == nil && old.DeletedAt != nil {
			res.PreviousVersion = old.TrashPath
		}
	}
	if !it.Modified.IsZero() {
		if err := e.Store.SetModTime(sourceID, it.Path, it.Modified); err != nil {
			e.Log.Warn("set modification time", "path", it.Path, "err", err)
		}
	}
	if it.Size > 0 && res.Size != it.Size {
		e.Log.Warn("size mismatch", "source", sourceID, "path", it.Path, "want", it.Size, "got", res.Size)
	}
	_, err = e.Catalog.PutFile(ctx, catalog.File{
		Source: sourceID, SourceItemID: it.SourceID, Path: it.Path,
		Size: res.Size, Modified: it.Modified, SHA256: res.SHA256, SourceChecksum: it.Checksum,
	}, res.PreviousVersion, e.Now())
	return err
}

// unchanged reports whether a source item has the same content as the catalog
// entry, so the download can be skipped.
func unchanged(f catalog.File, it source.Item) bool {
	if it.Checksum != "" {
		return it.Checksum == f.SourceChecksum
	}
	return it.Size == f.Size && it.Modified.Equal(f.Modified)
}

// Janitor permanently removes trash and versions past the retention policy.
func (e *Engine) Janitor(ctx context.Context) error {
	now := e.Now()
	trashed, err := e.Catalog.TrashedBefore(ctx, now.AddDate(0, 0, -e.Retention.TrashDays))
	if err != nil {
		return err
	}
	for _, f := range trashed {
		vs, err := e.Catalog.ListVersions(ctx, f.ID)
		if err != nil {
			return err
		}
		for _, v := range vs {
			if err := e.Store.Purge(v.StoredPath); err != nil {
				return err
			}
		}
		if err := e.Store.Purge(f.TrashPath); err != nil {
			return err
		}
		if err := e.Catalog.DeleteFile(ctx, f.ID); err != nil {
			return err
		}
	}
	expired, err := e.Catalog.ExpiredVersions(ctx, now.AddDate(0, 0, -e.Retention.VersionDays), e.Retention.MaxVersions)
	if err != nil {
		return err
	}
	for _, v := range expired {
		if err := e.Store.Purge(v.StoredPath); err != nil {
			return err
		}
		if err := e.Catalog.DeleteVersion(ctx, v.ID); err != nil {
			return err
		}
	}
	stale, err := e.Catalog.StaleUploads(ctx, now.Add(-UploadExpiry))
	if err != nil {
		return err
	}
	for _, u := range stale {
		if u.TmpPath != "" {
			if err := os.Remove(u.TmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := e.Catalog.DeleteUpload(ctx, u.ID); err != nil {
			return err
		}
	}
	if len(trashed)+len(expired)+len(stale) > 0 {
		e.Log.Info("janitor", "purgedFiles", len(trashed), "purgedVersions", len(expired), "staleUploads", len(stale))
	}
	return nil
}

// Run starts the per-source schedulers and the daily janitor, and blocks
// until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	e.mu.Lock()
	regs := make([]registered, 0, len(e.sources))
	for _, r := range e.sources {
		regs = append(regs, r)
	}
	e.mu.Unlock()

	var wg sync.WaitGroup
	for _, r := range regs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.loop(ctx, r)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.every(ctx, 24*time.Hour, func() {
			if err := e.Janitor(ctx); err != nil && ctx.Err() == nil {
				e.Log.Error("janitor", "err", err)
			}
		})
	}()
	wg.Wait()
}

func (e *Engine) loop(ctx context.Context, r registered) {
	var tick <-chan time.Time
	if r.schedule > 0 {
		t := time.NewTicker(r.schedule)
		defer t.Stop()
		tick = t.C
		// Sync once at startup for scheduled sources.
		select {
		case r.trigger <- struct{}{}:
		default:
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-r.trigger:
		}
		if err := e.Sync(ctx, r.src.ID()); err != nil && ctx.Err() == nil && !errors.Is(err, ErrBusy) {
			e.Log.Error("sync failed", "source", r.src.ID(), "err", err)
		}
	}
}

func (e *Engine) every(ctx context.Context, d time.Duration, fn func()) {
	fn()
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

// ErrNotDeleted is returned when restoring from trash a file that is not deleted.
var ErrNotDeleted = errors.New("file is not in the trash")

// ErrNotInTrash is returned when a deleted file has no local copy to restore.
var ErrNotInTrash = errors.New("deleted file has no local copy")

// RestoreVersion makes a previous version the current content of its file.
// The replaced content is kept as a new version.
func (e *Engine) RestoreVersion(ctx context.Context, fileID, versionID int64) (catalog.File, error) {
	f, err := e.Catalog.GetFile(ctx, fileID)
	if err != nil {
		return catalog.File{}, err
	}
	v, err := e.Catalog.GetVersion(ctx, versionID)
	if err != nil {
		return catalog.File{}, err
	}
	if v.FileID != f.ID {
		return catalog.File{}, catalog.ErrNotFound
	}
	if f.DeletedAt != nil {
		return catalog.File{}, fmt.Errorf("restore the file from the trash first: %w", ErrNotDeleted)
	}
	res, err := e.Store.RestoreVersion(f.Source, f.Path, v.StoredPath)
	if err != nil {
		return catalog.File{}, err
	}
	return e.Catalog.PutFile(ctx, catalog.File{
		Source: f.Source, SourceItemID: f.SourceItemID, Path: f.Path,
		Size: res.Size, Modified: v.Modified, SHA256: res.SHA256,
		// Clear the source checksum so the next change at the source is
		// always downloaded rather than mistaken for this content.
		SourceChecksum: "",
	}, res.PreviousVersion, e.Now())
}

// RestoreTrash moves a deleted file back into place, at newPath if set,
// otherwise at its original path.
func (e *Engine) RestoreTrash(ctx context.Context, fileID int64, newPath string) (catalog.File, error) {
	f, err := e.Catalog.GetFile(ctx, fileID)
	if err != nil {
		return catalog.File{}, err
	}
	if f.DeletedAt == nil {
		return catalog.File{}, ErrNotDeleted
	}
	if f.TrashPath == "" {
		return catalog.File{}, ErrNotInTrash
	}
	p := f.Path
	if newPath != "" {
		p = newPath
	}
	if _, err := e.Catalog.LiveFileByPath(ctx, f.Source, p); err == nil {
		return catalog.File{}, store.ErrExists
	}
	if err := e.Store.RestoreTrash(f.Source, p, f.TrashPath); err != nil {
		return catalog.File{}, err
	}
	if err := e.Catalog.Undelete(ctx, f.ID, p); err != nil {
		return catalog.File{}, err
	}
	return e.Catalog.GetFile(ctx, f.ID)
}
