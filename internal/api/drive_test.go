package api

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeoHChen/homebackup/internal/sources/device"
)

type changesPage struct {
	Changes []ChangeJSON
	Cursor  string
	More    bool
}

// upload sends a whole file in one chunk and returns the complete response.
func (e *devEnv) upload(t *testing.T, token string, create map[string]any, data []byte) (int, []byte) {
	t.Helper()
	create["size"], create["sha256"] = len(data), sum(data)
	resp, b := e.req(t, "POST", "/api/v1/uploads", token, jsonBody(create), nil)
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		return resp.StatusCode, b
	}
	out := decode[map[string]any](t, b)
	if out["status"] == "exists" {
		return resp.StatusCode, b
	}
	id := out["uploadId"].(string)
	resp, b = e.req(t, "PATCH", "/api/v1/uploads/"+id, token, bytes.NewReader(data),
		map[string]string{"Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"})
	if resp.StatusCode != 204 {
		t.Fatalf("patch = %d %s", resp.StatusCode, b)
	}
	resp, b = e.req(t, "POST", "/api/v1/uploads/"+id+"/complete", token, nil, nil)
	return resp.StatusCode, b
}

func TestDriveAPI(t *testing.T) {
	e := newDevEnv(t)
	e.eng.Register(device.New("files"), "drive", 0)
	_, token := e.pairDevice(t, "mac")

	changes := func(cursor string) changesPage {
		t.Helper()
		resp, b := e.req(t, "GET", "/api/v1/changes?source=files&limit=2&cursor="+cursor, token, nil, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("changes = %d %s", resp.StatusCode, b)
		}
		return decode[changesPage](t, b)
	}
	// Reads all pages from cursor and returns the changes and the new cursor.
	drain := func(cursor string) ([]ChangeJSON, string) {
		t.Helper()
		var all []ChangeJSON
		for {
			p := changes(cursor)
			all = append(all, p.Changes...)
			cursor = p.Cursor
			if !p.More {
				return all, cursor
			}
		}
	}

	// Empty source still hands out a cursor.
	if p := changes("0"); len(p.Changes) != 0 || p.More || p.Cursor == "" {
		t.Fatalf("empty feed = %+v", p)
	}
	if resp, _ := e.req(t, "GET", "/api/v1/changes?source=nope", token, nil, nil); resp.StatusCode != 400 {
		t.Fatalf("unknown source = %d", resp.StatusCode)
	}

	// Folders: nested create, idempotent repeat, read-only sources refused.
	resp, b := e.req(t, "POST", "/api/v1/folders", token, jsonBody(map[string]string{"source": "files", "path": "Work/Reports"}), nil)
	if resp.StatusCode != 201 || !decode[FileJSON](t, b).IsDir {
		t.Fatalf("mkdir = %d %s", resp.StatusCode, b)
	}
	if resp, b := e.req(t, "POST", "/api/v1/folders", token, jsonBody(map[string]string{"source": "files", "path": "Work"}), nil); resp.StatusCode != 201 {
		t.Fatalf("mkdir existing = %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.req(t, "POST", "/api/v1/folders", token, jsonBody(map[string]string{"source": "iphone", "path": "x"}), nil); resp.StatusCode != 403 {
		t.Fatalf("mkdir read-only = %d", resp.StatusCode)
	}
	if resp, _ := e.req(t, "POST", "/api/v1/folders", token, jsonBody(map[string]string{"source": "files", "path": "../x"}), nil); resp.StatusCode != 400 {
		t.Fatalf("mkdir bad path = %d", resp.StatusCode)
	}
	all, cursor := drain("0")
	if len(all) != 2 || all[0].File.Path != "Work" || all[1].File.Path != "Work/Reports" {
		t.Fatalf("folder changes = %+v", all)
	}

	// New file.
	v1 := []byte("draft one")
	status, b := e.upload(t, token, map[string]any{"source": "files", "path": "Work/Reports/q3.txt"}, v1)
	if status != 200 {
		t.Fatalf("upload = %d %s", status, b)
	}
	f := decode[FileJSON](t, b)
	// Repeating a finished upload reports exists instead of making a copy.
	if status, b := e.upload(t, token, map[string]any{"source": "files", "path": "Work/Reports/q3.txt"}, v1); status != 200 || !strings.Contains(string(b), `"exists"`) {
		t.Fatalf("repeat upload = %d %s", status, b)
	}
	// Another file at a taken path needs fileId.
	if status, _ := e.upload(t, token, map[string]any{"source": "files", "path": "Work/Reports/q3.txt"}, []byte("other")); status != 409 {
		t.Fatalf("taken path = %d", status)
	}
	all, cursor = drain(cursor)
	if len(all) != 1 || all[0].File.ID != f.ID || all[0].File.SHA256 != sum(v1) {
		t.Fatalf("upload changes = %+v", all)
	}

	// New version based on the current content.
	v2 := []byte("draft two")
	status, b = e.upload(t, token, map[string]any{"source": "files", "path": f.Path, "fileId": f.ID, "baseSha256": sum(v1)}, v2)
	if g := decode[FileJSON](t, b); status != 200 || g.ID != f.ID || g.SHA256 != sum(v2) {
		t.Fatalf("new version = %d %s", status, b)
	}
	// A stale edit becomes a conflicted copy next to the file.
	status, b = e.upload(t, token, map[string]any{"source": "files", "path": f.Path, "fileId": f.ID, "baseSha256": sum(v1)}, []byte("offline edit"))
	conflict := decode[FileJSON](t, b)
	if status != 200 || conflict.ID == f.ID || !strings.HasPrefix(conflict.Path, "Work/Reports/q3 (conflict from device ") || !strings.HasSuffix(conflict.Path, ").txt") {
		t.Fatalf("conflict = %d %s", status, b)
	}
	if status, _ := e.upload(t, token, map[string]any{"source": "files", "path": f.Path, "fileId": "999"}, v2); status != 404 {
		t.Fatalf("unknown fileId = %d", status)
	}

	// Rename the folder: everything below moves with it.
	_, b = e.req(t, "GET", "/api/v1/files?source=files", token, nil, nil)
	root := decode[struct{ Items []FileJSON }](t, b).Items
	if len(root) != 1 || root[0].Path != "Work" || root[0].ID == "" {
		t.Fatalf("root = %s", b)
	}
	resp, b = e.req(t, "PATCH", "/api/v1/files/"+root[0].ID, token, jsonBody(map[string]string{"path": "Archive/2026"}), nil)
	if resp.StatusCode != 200 || decode[FileJSON](t, b).Path != "Archive/2026" {
		t.Fatalf("move folder = %d %s", resp.StatusCode, b)
	}
	_, b = e.req(t, "GET", "/api/v1/files/"+f.ID, token, nil, nil)
	if got := decode[FileJSON](t, b); got.Path != "Archive/2026/Reports/q3.txt" {
		t.Fatalf("moved file = %s", b)
	}
	resp, b = e.req(t, "GET", "/api/v1/files/"+f.ID+"/content", token, nil, nil)
	if resp.StatusCode != 200 || !bytes.Equal(b, v2) {
		t.Fatalf("content after move = %d %q", resp.StatusCode, b)
	}
	if resp, _ := e.req(t, "PATCH", "/api/v1/files/"+root[0].ID, token, jsonBody(map[string]string{"path": "Archive/2026/Reports/x"}), nil); resp.StatusCode != 400 {
		t.Fatalf("move into itself = %d", resp.StatusCode)
	}
	if resp, _ := e.req(t, "PATCH", "/api/v1/files/"+conflict.ID, token, jsonBody(map[string]string{"path": "Archive/2026/Reports/q3.txt"}), nil); resp.StatusCode != 409 {
		t.Fatalf("move onto existing = %d", resp.StatusCode)
	}
	all, cursor = drain(cursor)
	moved := map[string]bool{}
	for _, c := range all {
		moved[c.File.Path] = true
	}
	for _, p := range []string{"Archive", "Archive/2026", "Archive/2026/Reports", "Archive/2026/Reports/q3.txt"} {
		if !moved[p] {
			t.Fatalf("missing change for %s in %+v", p, all)
		}
	}

	// Delete the folder: its files go to the trash and the feed reports it.
	if resp, b := e.req(t, "DELETE", "/api/v1/files/"+root[0].ID, token, nil, nil); resp.StatusCode != 204 {
		t.Fatalf("delete = %d %s", resp.StatusCode, b)
	}
	all, _ = drain(cursor)
	deleted := 0
	for _, c := range all {
		if c.File.DeletedAt != nil {
			deleted++
		}
	}
	if len(all) != 4 || deleted != 4 { // Archive/2026, Reports, q3.txt, the conflict copy
		t.Fatalf("changes after delete: %+v", all)
	}
	_, b = e.req(t, "GET", "/api/v1/trash?source=files", token, nil, nil)
	if !strings.Contains(string(b), "q3.txt") {
		t.Fatalf("trash = %s", b)
	}
	if resp, _ := e.req(t, "DELETE", "/api/v1/files/"+f.ID, token, nil, nil); resp.StatusCode != 204 {
		t.Fatalf("delete again = %d", resp.StatusCode)
	}

	// Read-only sources refuse edits.
	data := []byte("photo")
	status, b = e.upload(t, token, map[string]any{"source": "iphone", "path": "2026/01/a.jpg"}, data)
	if status != 200 {
		t.Fatalf("iphone upload = %d %s", status, b)
	}
	photo := decode[FileJSON](t, b)
	if resp, _ := e.req(t, "PATCH", "/api/v1/files/"+photo.ID, token, jsonBody(map[string]string{"path": "b.jpg"}), nil); resp.StatusCode != 403 {
		t.Fatalf("move read-only = %d", resp.StatusCode)
	}
	if resp, _ := e.req(t, "DELETE", "/api/v1/files/"+photo.ID, token, nil, nil); resp.StatusCode != 403 {
		t.Fatalf("delete read-only = %d", resp.StatusCode)
	}
}
