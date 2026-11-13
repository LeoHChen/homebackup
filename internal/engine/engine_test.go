package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/source"
	"github.com/LeoHChen/homebackup/internal/store"
)

// fakeSource replays scripted batches. The cursor is the batch index.
type fakeSource struct {
	batches  [][]source.Change
	content  map[string]string // SourceID -> content
	opens    int
	failOpen string
	failErr  error
}

func (f *fakeSource) ID() string { return "fake" }

func (f *fakeSource) Changes(_ context.Context, cursor string) (source.ChangeSet, error) {
	i := 0
	if cursor != "" {
		i, _ = strconv.Atoi(cursor)
	}
	if i >= len(f.batches) {
		return source.ChangeSet{NextCursor: cursor}, nil
	}
	return source.ChangeSet{Changes: f.batches[i], NextCursor: strconv.Itoa(i + 1), More: i+1 < len(f.batches)}, nil
}

func (f *fakeSource) Open(_ context.Context, it source.Item) (io.ReadCloser, error) {
	if it.SourceID == f.failOpen {
		if f.failErr != nil {
			return nil, f.failErr
		}
		return nil, errors.New("network down")
	}
	f.opens++
	return io.NopCloser(strings.NewReader(f.content[it.SourceID])), nil
}

type env struct {
	e     *Engine
	src   *fakeSource
	clock time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Open(filepath.Join(st.StateDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	v := &env{src: &fakeSource{content: map[string]string{}}, clock: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	v.e = New(st, cat, config.Retention{TrashDays: 30, VersionDays: 30, MaxVersions: 2}, nil)
	v.e.Now = func() time.Time { return v.clock }
	v.e.Register(v.src, "fake", 0)
	return v
}

func up(id, p, sum string) source.Change {
	return source.Change{Kind: source.Upserted, Item: source.Item{SourceID: id, Path: p, Checksum: sum}}
}

func del(id string) source.Change {
	return source.Change{Kind: source.Deleted, Item: source.Item{SourceID: id}}
}

func (v *env) sync(t *testing.T, batches ...[]source.Change) {
	t.Helper()
	v.src.batches = append(v.src.batches, batches...)
	if err := v.e.Sync(context.Background(), "fake"); err != nil {
		t.Fatal(err)
	}
}

func (v *env) current(t *testing.T, p string) string {
	t.Helper()
	dp, _ := v.e.Store.DataPath("fake", p)
	b, err := os.ReadFile(dp)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestSyncLifecycle(t *testing.T) {
	v := newEnv(t)
	ctx := context.Background()

	v.src.content["1"] = "hello"
	v.src.content["2"] = "world"
	v.sync(t, []source.Change{up("1", "a.txt", "c1")}, []source.Change{up("2", "dir/b.txt", "c2")})
	if v.current(t, "a.txt") != "hello" || v.current(t, "dir/b.txt") != "world" {
		t.Fatal("initial sync content mismatch")
	}
	if st, _ := v.e.Catalog.SourceState(ctx, "fake"); st.Cursor != "2" || st.LastSync == nil || st.LastError != "" {
		t.Fatalf("state = %+v", st)
	}

	// Rename with same checksum: no download.
	opens := v.src.opens
	v.sync(t, []source.Change{up("1", "renamed.txt", "c1")})
	if v.src.opens != opens || v.current(t, "renamed.txt") != "hello" {
		t.Fatal("rename should move without downloading")
	}

	// Content change: old content becomes a version.
	v.src.content["1"] = "hello v2"
	v.sync(t, []source.Change{up("1", "renamed.txt", "c1b")})
	f, _ := v.e.Catalog.FileBySourceItem(ctx, "fake", "1")
	vs, _ := v.e.Catalog.ListVersions(ctx, f.ID)
	if v.current(t, "renamed.txt") != "hello v2" || len(vs) != 1 {
		t.Fatalf("versions = %+v", vs)
	}

	// Delete: goes to trash and leaves the listing.
	v.sync(t, []source.Change{del("2")})
	if es, _ := v.e.Catalog.ListDir(ctx, "fake", ""); len(es) != 1 || es[0].Name != "renamed.txt" {
		t.Fatalf("listing after delete = %+v", es)
	}
	trash, _ := v.e.Catalog.ListTrash(ctx, "fake")
	if len(trash) != 1 {
		t.Fatalf("trash = %+v", trash)
	}
	if b, _ := os.ReadFile(trash[0].TrashPath); string(b) != "world" {
		t.Fatal("trashed content mismatch")
	}

	// Janitor after retention purges the trash.
	v.clock = v.clock.AddDate(0, 0, 31)
	if err := v.e.Janitor(ctx); err != nil {
		t.Fatal(err)
	}
	if trash, _ := v.e.Catalog.ListTrash(ctx, "fake"); len(trash) != 0 {
		t.Fatalf("trash not purged: %+v", trash)
	}
}

func TestFailedChangeKeepsCursor(t *testing.T) {
	v := newEnv(t)
	ctx := context.Background()
	v.src.content["1"] = "x"
	v.src.failOpen = "2"
	v.src.batches = [][]source.Change{{up("1", "a", "c1"), up("2", "b", "c2")}}
	if err := v.e.Sync(ctx, "fake"); err == nil {
		t.Fatal("expected error")
	}
	st, _ := v.e.Catalog.SourceState(ctx, "fake")
	if st.Cursor != "" || st.LastError == "" {
		t.Fatalf("cursor must not advance on failure: %+v", st)
	}
	// Retry succeeds and skips the already-stored item.
	v.src.failOpen = ""
	v.src.content["2"] = "y"
	opens := v.src.opens
	if err := v.e.Sync(ctx, "fake"); err != nil {
		t.Fatal(err)
	}
	if v.src.opens != opens+1 {
		t.Fatalf("expected only the failed item to be downloaded again, opens %d -> %d", opens, v.src.opens)
	}
}

func TestDuplicatePathsAreDisambiguated(t *testing.T) {
	v := newEnv(t)
	v.src.content["1"], v.src.content["2"] = "a", "b"
	v.sync(t, []source.Change{up("1", "dir/same.txt", "c1"), up("2", "dir/same.txt", "c2")})
	alt := disambiguate("dir/same.txt", "2")
	if v.current(t, "dir/same.txt") != "a" || v.current(t, alt) != "b" {
		t.Fatal("duplicate names must both be kept")
	}
	// Re-syncing the second item is stable: no move, no re-download.
	opens := v.src.opens
	v.sync(t, []source.Change{up("2", "dir/same.txt", "c2")})
	if v.src.opens != opens || v.current(t, alt) != "b" {
		t.Fatal("disambiguated path must be stable")
	}
	if got := disambiguate("a/.env", "x"); !strings.HasPrefix(got, "a/.env~") {
		t.Errorf("dotfile disambiguation = %s", got)
	}
}

func TestTreeChanges(t *testing.T) {
	v := newEnv(t)
	ctx := context.Background()
	v.src.content["1"], v.src.content["2"], v.src.content["3"] = "a", "b", "c"
	v.sync(t, []source.Change{up("1", "docs/a", "c1"), up("2", "docs/sub/b", "c2"), up("3", "docs2/c", "c3")})

	opens := v.src.opens
	v.sync(t, []source.Change{{Kind: source.MovedTree, OldPath: "docs", Item: source.Item{Path: "archive/docs"}}})
	if v.src.opens != opens || v.current(t, "archive/docs/a") != "a" || v.current(t, "archive/docs/sub/b") != "b" {
		t.Fatal("folder move should move descendants without downloading")
	}
	if v.current(t, "docs2/c") != "c" {
		t.Fatal("sibling folder with shared prefix must not move")
	}

	v.sync(t, []source.Change{{Kind: source.DeletedTree, Item: source.Item{Path: "archive"}}})
	if tr, _ := v.e.Catalog.ListTrash(ctx, "fake"); len(tr) != 2 {
		t.Fatalf("trash = %+v", tr)
	}
	if es, _ := v.e.Catalog.ListDir(ctx, "fake", ""); len(es) != 1 || es[0].Name != "docs2" {
		t.Fatalf("listing = %+v", es)
	}
	v.src.batches = append(v.src.batches, []source.Change{{Kind: source.DeletedTree, Item: source.Item{Path: "/"}}})
	if err := v.e.Sync(ctx, "fake"); err == nil {
		t.Fatal("deleting the root must be refused")
	}
}

func TestPermanentErrorSkipsItem(t *testing.T) {
	v := newEnv(t)
	v.src.failOpen = "2"
	v.src.failErr = fmt.Errorf("too big: %w", source.ErrPermanent)
	v.src.content["1"] = "x"
	v.sync(t, []source.Change{up("1", "a", "c1"), up("2", "b", "c2")})
	if st, _ := v.e.Catalog.SourceState(context.Background(), "fake"); st.Cursor != "1" {
		t.Fatalf("cursor should advance past permanent failures: %+v", st)
	}
}

func TestResurrectedItemKeepsTrashAsVersion(t *testing.T) {
	v := newEnv(t)
	ctx := context.Background()
	v.src.content["1"] = "old"
	v.sync(t, []source.Change{up("1", "a", "c1")}, []source.Change{del("1")})
	v.src.content["1"] = "new"
	v.sync(t, []source.Change{up("1", "a", "c2")})
	f, _ := v.e.Catalog.FileBySourceItem(ctx, "fake", "1")
	vs, _ := v.e.Catalog.ListVersions(ctx, f.ID)
	if v.current(t, "a") != "new" || len(vs) != 1 {
		t.Fatalf("versions = %+v", vs)
	}
	if b, _ := os.ReadFile(vs[0].StoredPath); string(b) != "old" {
		t.Fatal("trashed copy not kept as version")
	}
}

func TestUnknownSourceAndTrigger(t *testing.T) {
	v := newEnv(t)
	if err := v.e.Sync(context.Background(), "nope"); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("err = %v", err)
	}
	if err := v.e.Trigger("nope"); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("err = %v", err)
	}
	if err := v.e.Trigger("fake"); err != nil {
		t.Fatal(err)
	}
	if err := v.e.Trigger("fake"); err != nil { // coalesces, must not block
		t.Fatal(err)
	}
}

func TestRunSyncsOnTrigger(t *testing.T) {
	v := newEnv(t)
	v.src.content["1"] = "x"
	v.src.batches = [][]source.Change{{up("1", "a", "c1")}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { v.e.Run(ctx); close(done) }()
	v.e.Trigger("fake")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, _ := v.e.Catalog.SourceState(context.Background(), "fake"); st.LastSync != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("triggered sync did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestRestore(t *testing.T) {
	v := newEnv(t)
	ctx := context.Background()
	v.src.content["1"] = "v1"
	v.sync(t, []source.Change{up("1", "a", "c1")})
	v.src.content["1"] = "v2"
	v.sync(t, []source.Change{up("1", "a", "c2")})

	f, _ := v.e.Catalog.FileBySourceItem(ctx, "fake", "1")
	vs, _ := v.e.Catalog.ListVersions(ctx, f.ID)
	if _, err := v.e.RestoreVersion(ctx, f.ID, vs[0].ID); err != nil {
		t.Fatal(err)
	}
	if v.current(t, "a") != "v1" {
		t.Fatal("version not restored")
	}
	if vs, _ = v.e.Catalog.ListVersions(ctx, f.ID); len(vs) != 2 {
		t.Fatalf("restored-over content should be a version: %+v", vs)
	}

	if _, err := v.e.RestoreTrash(ctx, f.ID, ""); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("err = %v, want ErrNotDeleted", err)
	}
	v.sync(t, []source.Change{del("1")})
	got, err := v.e.RestoreTrash(ctx, f.ID, "back/a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "back/a" || got.DeletedAt != nil || v.current(t, "back/a") != "v1" {
		t.Fatalf("restored = %+v", got)
	}
}

func TestDeleteMissingLocalFile(t *testing.T) {
	v := newEnv(t)
	ctx := context.Background()
	v.src.content["1"] = "x"
	v.sync(t, []source.Change{up("1", "a", "c1")})
	dp, _ := v.e.Store.DataPath("fake", "a")
	os.Remove(dp)
	v.sync(t, []source.Change{del("1")})
	f, _ := v.e.Catalog.FileBySourceItem(ctx, "fake", "1")
	if f.DeletedAt == nil {
		t.Fatal("expected file marked deleted")
	}
	if _, err := v.e.RestoreTrash(ctx, f.ID, ""); !errors.Is(err, ErrNotInTrash) {
		t.Fatalf("err = %v", err)
	}
	v.clock = v.clock.AddDate(0, 0, 31)
	if err := v.e.Janitor(ctx); err != nil {
		t.Fatal(err)
	}
}
