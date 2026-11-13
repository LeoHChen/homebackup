// Package gdrive backs up "My Drive" from Google Drive.
//
// The first sync lists every folder, then every file, page by page. After
// that it follows the Drive Changes API, so only changed items are fetched.
// Google Docs, Sheets, Slides, and Drawings are exported to Office/PDF files.
//
// Drive identifies items by ID and lets a file move or a folder be renamed
// without touching its children, so the source keeps a persistent map of
// folders (ID -> name, parent) to turn IDs into paths and to report folder
// renames and deletions as tree changes.
package gdrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LeoHChen/homebackup/internal/source"
)

// DefaultBaseURL is the Drive v3 API endpoint.
const DefaultBaseURL = "https://www.googleapis.com/drive/v3"

const (
	mimeFolder   = "application/vnd.google-apps.folder"
	mimePrefixGA = "application/vnd.google-apps."
	itemFields   = "id,name,mimeType,parents,md5Checksum,size,modifiedTime,trashed,version"
)

// export describes how a Google Workspace type is saved locally.
type export struct {
	mime string
	ext  string
}

var exports = map[string]export{
	"application/vnd.google-apps.document":     {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
	"application/vnd.google-apps.spreadsheet":  {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
	"application/vnd.google-apps.presentation": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"},
	"application/vnd.google-apps.drawing":      {"application/pdf", ".pdf"},
}

// Source is a Google Drive source.
type Source struct {
	id        string
	client    *http.Client
	baseURL   string
	stateFile string
	log       *slog.Logger
	// retryBase is the first retry delay; it doubles on each attempt.
	retryBase time.Duration

	mu    sync.Mutex
	state folderState
	// mimes remembers MIME types seen while listing so Open can skip a lookup.
	mimes sync.Map // file ID -> string
}

type folder struct {
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

type folderState struct {
	RootID  string            `json:"rootId"`
	Folders map[string]folder `json:"folders"`
}

// Options configures a Source.
type Options struct {
	ID string
	// Client must add OAuth credentials (see NewClient).
	Client *http.Client
	// StateFile stores the folder map between runs.
	StateFile string
	BaseURL   string
	Log       *slog.Logger
	RetryBase time.Duration
}

// New returns a Drive source.
func New(o Options) (*Source, error) {
	s := &Source{
		id: o.ID, client: o.Client, baseURL: o.BaseURL, stateFile: o.StateFile,
		log: o.Log, retryBase: o.RetryBase,
		state: folderState{Folders: map[string]folder{}},
	}
	if s.baseURL == "" {
		s.baseURL = DefaultBaseURL
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.retryBase == 0 {
		s.retryBase = time.Second
	}
	b, err := os.ReadFile(s.stateFile)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.state); err != nil {
			return nil, fmt.Errorf("read %s: %w", s.stateFile, err)
		}
		if s.state.Folders == nil {
			s.state.Folders = map[string]folder{}
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	return s, nil
}

// ID implements source.Source.
func (s *Source) ID() string { return s.id }

// driveFile is the subset of the Drive file resource we use.
type driveFile struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	MimeType     string    `json:"mimeType"`
	Parents      []string  `json:"parents"`
	MD5          string    `json:"md5Checksum"`
	Size         string    `json:"size"`
	ModifiedTime time.Time `json:"modifiedTime"`
	Trashed      bool      `json:"trashed"`
	Version      string    `json:"version"`
}

func (f driveFile) parent() string {
	if len(f.Parents) == 0 {
		return ""
	}
	return f.Parents[0]
}

type fileList struct {
	NextPageToken string      `json:"nextPageToken"`
	Files         []driveFile `json:"files"`
}

type changeList struct {
	NextPageToken     string `json:"nextPageToken"`
	NewStartPageToken string `json:"newStartPageToken"`
	Changes           []struct {
		FileID  string     `json:"fileId"`
		Removed bool       `json:"removed"`
		File    *driveFile `json:"file"`
	} `json:"changes"`
}

// Cursor format:
//
//	""                         start: list folders
//	"files|<start>|<page>"     listing files
//	"changes|<page>"           following the Changes API
func (s *Source) Changes(ctx context.Context, cursor string) (source.ChangeSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(cursor, "|")
	switch {
	case cursor == "":
		return s.listFolders(ctx)
	case parts[0] == "files" && len(parts) == 3:
		return s.listFiles(ctx, parts[1], parts[2])
	case parts[0] == "changes" && len(parts) == 2:
		return s.changes(ctx, parts[1])
	default:
		return source.ChangeSet{}, fmt.Errorf("invalid cursor %q", cursor)
	}
}

func (s *Source) listFolders(ctx context.Context) (source.ChangeSet, error) {
	var root driveFile
	if err := s.getJSON(ctx, "/files/root", url.Values{"fields": {"id"}}, &root); err != nil {
		return source.ChangeSet{}, fmt.Errorf("get root: %w", err)
	}
	var start struct {
		StartPageToken string `json:"startPageToken"`
	}
	if err := s.getJSON(ctx, "/changes/startPageToken", nil, &start); err != nil {
		return source.ChangeSet{}, fmt.Errorf("get start token: %w", err)
	}
	st := folderState{RootID: root.ID, Folders: map[string]folder{}}
	q := url.Values{
		"q":        {"mimeType = '" + mimeFolder + "' and trashed = false"},
		"fields":   {"nextPageToken,files(id,name,parents)"},
		"pageSize": {"1000"},
		"spaces":   {"drive"},
	}
	for {
		var fl fileList
		if err := s.getJSON(ctx, "/files", q, &fl); err != nil {
			return source.ChangeSet{}, fmt.Errorf("list folders: %w", err)
		}
		for _, f := range fl.Files {
			st.Folders[f.ID] = folder{Name: f.Name, Parent: f.parent()}
		}
		if fl.NextPageToken == "" {
			break
		}
		q.Set("pageToken", fl.NextPageToken)
	}
	s.state = st
	if err := s.saveState(); err != nil {
		return source.ChangeSet{}, err
	}
	s.log.Info("gdrive folders listed", "source", s.id, "folders", len(st.Folders))
	return source.ChangeSet{NextCursor: "files|" + start.StartPageToken + "|", More: true}, nil
}

func (s *Source) listFiles(ctx context.Context, startToken, pageToken string) (source.ChangeSet, error) {
	q := url.Values{
		"q":        {"mimeType != '" + mimeFolder + "' and trashed = false"},
		"fields":   {"nextPageToken,files(" + itemFields + ")"},
		"pageSize": {"1000"},
		"spaces":   {"drive"},
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	var fl fileList
	if err := s.getJSON(ctx, "/files", q, &fl); err != nil {
		return source.ChangeSet{}, fmt.Errorf("list files: %w", err)
	}
	var cs source.ChangeSet
	for _, f := range fl.Files {
		if it, ok := s.item(ctx, f); ok {
			cs.Changes = append(cs.Changes, source.Change{Kind: source.Upserted, Item: it})
		}
	}
	cs.More = true
	if fl.NextPageToken != "" {
		cs.NextCursor = "files|" + startToken + "|" + fl.NextPageToken
	} else {
		cs.NextCursor = "changes|" + startToken
	}
	return cs, s.saveState()
}

func (s *Source) changes(ctx context.Context, pageToken string) (source.ChangeSet, error) {
	q := url.Values{
		"pageToken":         {pageToken},
		"fields":            {"nextPageToken,newStartPageToken,changes(fileId,removed,file(" + itemFields + "))"},
		"pageSize":          {"1000"},
		"spaces":            {"drive"},
		"includeRemoved":    {"true"},
		"restrictToMyDrive": {"true"},
	}
	var cl changeList
	if err := s.getJSON(ctx, "/changes", q, &cl); err != nil {
		return source.ChangeSet{}, fmt.Errorf("list changes: %w", err)
	}
	var cs source.ChangeSet
	for _, c := range cl.Changes {
		chs, err := s.translate(ctx, c.FileID, c.Removed, c.File)
		if err != nil {
			return source.ChangeSet{}, err
		}
		cs.Changes = append(cs.Changes, chs...)
	}
	if cl.NextPageToken != "" {
		cs.NextCursor, cs.More = "changes|"+cl.NextPageToken, true
	} else if cl.NewStartPageToken != "" {
		cs.NextCursor = "changes|" + cl.NewStartPageToken
	} else {
		cs.NextCursor = "changes|" + pageToken
	}
	return cs, s.saveState()
}

// translate turns one Drive change into source changes, updating the folder map.
func (s *Source) translate(ctx context.Context, id string, removed bool, f *driveFile) ([]source.Change, error) {
	_, wasFolder := s.state.Folders[id]
	gone := removed || f == nil || f.Trashed

	if wasFolder {
		oldPath, wasReachable := s.folderPath(ctx, id)
		if gone {
			delete(s.state.Folders, id)
			if wasReachable && oldPath != "" {
				return []source.Change{{Kind: source.DeletedTree, Item: source.Item{Path: oldPath}}}, nil
			}
			return nil, nil
		}
		s.state.Folders[id] = folder{Name: f.Name, Parent: f.parent()}
		newPath, reachable := s.folderPath(ctx, id)
		switch {
		case wasReachable && !reachable:
			return []source.Change{{Kind: source.DeletedTree, Item: source.Item{Path: oldPath}}}, nil
		case wasReachable && oldPath != newPath:
			return []source.Change{{Kind: source.MovedTree, OldPath: oldPath, Item: source.Item{Path: newPath}}}, nil
		case !wasReachable && reachable:
			return s.listTree(ctx, id)
		}
		return nil, nil
	}

	if gone {
		return []source.Change{{Kind: source.Deleted, Item: source.Item{SourceID: id}}}, nil
	}
	if f.MimeType == mimeFolder {
		// New folder, or one restored from the Drive trash: pick up its contents.
		s.state.Folders[id] = folder{Name: f.Name, Parent: f.parent()}
		if _, ok := s.folderPath(ctx, id); !ok {
			return nil, nil
		}
		return s.listTree(ctx, id)
	}
	if it, ok := s.item(ctx, *f); ok {
		return []source.Change{{Kind: source.Upserted, Item: it}}, nil
	}
	// Moved out of My Drive, or a type we do not back up.
	return []source.Change{{Kind: source.Deleted, Item: source.Item{SourceID: id}}}, nil
}

// listTree lists everything below a folder, adding sub-folders to the map.
func (s *Source) listTree(ctx context.Context, folderID string) ([]source.Change, error) {
	var out []source.Change
	queue := []string{folderID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		q := url.Values{
			"q":        {"'" + id + "' in parents and trashed = false"},
			"fields":   {"nextPageToken,files(" + itemFields + ")"},
			"pageSize": {"1000"},
		}
		for {
			var fl fileList
			if err := s.getJSON(ctx, "/files", q, &fl); err != nil {
				return nil, fmt.Errorf("list folder %s: %w", id, err)
			}
			for _, f := range fl.Files {
				if f.MimeType == mimeFolder {
					s.state.Folders[f.ID] = folder{Name: f.Name, Parent: f.parent()}
					queue = append(queue, f.ID)
					continue
				}
				if it, ok := s.item(ctx, f); ok {
					out = append(out, source.Change{Kind: source.Upserted, Item: it})
				}
			}
			if fl.NextPageToken == "" {
				break
			}
			q.Set("pageToken", fl.NextPageToken)
		}
	}
	return out, nil
}

// item converts a Drive file to a source item. ok is false for files outside
// My Drive and for Google types that cannot be exported (Forms, Sites, ...).
func (s *Source) item(ctx context.Context, f driveFile) (source.Item, bool) {
	s.mimes.Store(f.ID, f.MimeType)
	name := sanitize(f.Name)
	checksum := "md5:" + f.MD5
	if strings.HasPrefix(f.MimeType, mimePrefixGA) {
		ex, ok := exports[f.MimeType]
		if !ok {
			return source.Item{}, false
		}
		if !strings.HasSuffix(strings.ToLower(name), ex.ext) {
			name += ex.ext
		}
		checksum = "v:" + f.Version
	} else if f.MD5 == "" {
		checksum = "v:" + f.Version
	}
	dir, ok := s.folderPath(ctx, f.parent())
	if !ok {
		return source.Item{}, false
	}
	p := name
	if dir != "" {
		p = dir + "/" + name
	}
	size, _ := strconv.ParseInt(f.Size, 10, 64)
	return source.Item{SourceID: f.ID, Path: p, Size: size, Modified: f.ModifiedTime, Checksum: checksum}, true
}

// folderPath returns the path of a folder relative to My Drive. ok is false
// when the folder is not inside My Drive.
func (s *Source) folderPath(ctx context.Context, id string) (string, bool) {
	var names []string
	seen := map[string]bool{}
	for id != s.state.RootID {
		if id == "" || seen[id] {
			return "", false
		}
		seen[id] = true
		fo, ok := s.state.Folders[id]
		if !ok {
			// Unknown folder (e.g. created after the listing): fetch it.
			var f driveFile
			err := s.getJSON(ctx, "/files/"+url.PathEscape(id), url.Values{"fields": {"id,name,parents,mimeType,trashed"}}, &f)
			if err != nil || f.MimeType != mimeFolder || f.Trashed {
				return "", false
			}
			fo = folder{Name: f.Name, Parent: f.parent()}
			s.state.Folders[id] = fo
		}
		names = append(names, sanitize(fo.Name))
		id = fo.Parent
	}
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}
	return strings.Join(names, "/"), true
}

// sanitize makes a Drive name safe as a single path element.
func sanitize(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', 0:
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "_"
	}
	return name
}

func (s *Source) saveState() error {
	b, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	tmp := s.stateFile + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.stateFile), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.stateFile)
}

// Open implements source.Source: it downloads a file or exports a Google doc.
func (s *Source) Open(ctx context.Context, it source.Item) (io.ReadCloser, error) {
	mime, _ := s.mimes.Load(it.SourceID)
	if mime == nil {
		var meta driveFile
		err := s.getJSON(ctx, "/files/"+url.PathEscape(it.SourceID), url.Values{"fields": {"id,mimeType"}}, &meta)
		if err != nil {
			var ae *apiError
			if errors.As(err, &ae) && ae.permanent() {
				return nil, fmt.Errorf("%w: %v", source.ErrPermanent, err)
			}
			return nil, err
		}
		mime = meta.MimeType
	}
	var u string
	if ex, ok := exports[mime.(string)]; ok {
		u = s.baseURL + "/files/" + url.PathEscape(it.SourceID) + "/export?" + url.Values{"mimeType": {ex.mime}}.Encode()
	} else {
		u = s.baseURL + "/files/" + url.PathEscape(it.SourceID) + "?alt=media"
	}
	resp, err := s.do(ctx, u)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.permanent() {
			return nil, fmt.Errorf("%w: %v", source.ErrPermanent, err)
		}
		return nil, err
	}
	return resp.Body, nil
}

// apiError is a non-2xx Drive response.
type apiError struct {
	Status int
	Reason string
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("drive API: HTTP %d %s: %s", e.Status, e.Reason, e.Body)
}

func (e *apiError) retryable() bool {
	switch {
	case e.Status == http.StatusTooManyRequests, e.Status >= 500:
		return true
	case e.Status == http.StatusForbidden:
		return e.Reason == "rateLimitExceeded" || e.Reason == "userRateLimitExceeded"
	}
	return false
}

func (e *apiError) permanent() bool {
	switch e.Reason {
	case "exportSizeLimitExceeded", "cannotDownloadAbusiveFile", "cannotExportFile", "fileNotDownloadable":
		return true
	}
	return e.Status == http.StatusNotFound
}

func parseAPIError(resp *http.Response) *apiError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	ae := &apiError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	var body struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &body) == nil && len(body.Error.Errors) > 0 {
		ae.Reason = body.Error.Errors[0].Reason
	}
	return ae
}

const maxAttempts = 6

// do performs a GET with retries on rate limits and server errors.
func (s *Source) do(ctx context.Context, u string) (*http.Response, error) {
	delay := s.retryBase
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := s.client.Do(req)
		if err == nil && resp.StatusCode/100 == 2 {
			return resp, nil
		}
		var ae *apiError
		if err == nil {
			ae = parseAPIError(resp)
			resp.Body.Close()
			if !ae.retryable() {
				return nil, ae
			}
			err = ae
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == maxAttempts {
			return nil, err
		}
		s.log.Warn("drive request failed, retrying", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

func (s *Source) getJSON(ctx context.Context, path string, q url.Values, v any) error {
	u := s.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := s.do(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}
