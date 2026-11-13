package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/sources/device"
	"github.com/LeoHChen/homebackup/internal/store"
)

type devEnv struct {
	ts  *httptest.Server
	eng *engine.Engine
}

func newDevEnv(t *testing.T) *devEnv {
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
	eng := engine.New(st, cat, config.Default().Retention, nil)
	eng.Register(device.New("iphone"), "device-upload", 0)
	s := &Server{Version: "test", Engine: eng, Status: func() Status { return Status{} },
		Tokens: Authenticators{DeviceTokens{Catalog: cat}, StaticTokens{"admin"}}}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &devEnv{ts: ts, eng: eng}
}

func (e *devEnv) req(t *testing.T, method, path, token string, body io.Reader, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func (e *devEnv) pairDevice(t *testing.T, name string) (string, string) {
	t.Helper()
	code, err := e.eng.Catalog.NewPairingCode(context.Background(), time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, b := e.req(t, "POST", "/api/v1/pair", "", jsonBody(map[string]string{
		"code": code[:3] + " " + code[3:], "deviceName": name, "platform": "ios"}), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("pair = %d %s", resp.StatusCode, b)
	}
	out := decode[map[string]string](t, b)
	return out["deviceId"], out["token"]
}

func TestPairAndDevices(t *testing.T) {
	e := newDevEnv(t)
	resp, _ := e.req(t, "POST", "/api/v1/pair", "", jsonBody(map[string]string{"code": "123456", "deviceName": "x", "platform": "ios"}), nil)
	if resp.StatusCode != 401 {
		t.Fatalf("bad code status = %d", resp.StatusCode)
	}
	resp, _ = e.req(t, "POST", "/api/v1/pair", "", jsonBody(map[string]string{"code": "123456", "deviceName": "", "platform": "android"}), nil)
	if resp.StatusCode != 400 {
		t.Fatalf("invalid body status = %d", resp.StatusCode)
	}

	id, token := e.pairDevice(t, "Leo's iPhone")
	resp, b := e.req(t, "GET", "/api/v1/devices", token, nil, nil)
	devs := decode[[]DeviceJSON](t, b)
	if resp.StatusCode != 200 || len(devs) != 1 || devs[0].ID != id || devs[0].Name != "Leo's iPhone" {
		t.Fatalf("devices = %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.req(t, "DELETE", "/api/v1/devices/"+id, "admin", nil, nil); resp.StatusCode != 204 {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if resp, _ := e.req(t, "GET", "/api/v1/devices", token, nil, nil); resp.StatusCode != 401 {
		t.Fatalf("revoked token status = %d", resp.StatusCode)
	}
}

func TestPairRateLimit(t *testing.T) {
	e := newDevEnv(t)
	limited := false
	for i := 0; i < 10; i++ {
		resp, _ := e.req(t, "POST", "/api/v1/pair", "", jsonBody(map[string]string{"code": "000000", "deviceName": "x", "platform": "ios"}), nil)
		if resp.StatusCode == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("expected pairing attempts to be rate limited")
	}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestResumableUpload(t *testing.T) {
	e := newDevEnv(t)
	_, token := e.pairDevice(t, "phone")
	_, other := e.pairDevice(t, "other phone")
	data := bytes.Repeat([]byte("0123456789"), 1000) // 10 KB
	taken := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	create := map[string]any{"source": "iphone", "path": "2026/07/IMG_0001.HEIC", "size": len(data),
		"sha256": sum(data), "assetId": "PH-ASSET-1", "created": taken}

	resp, b := e.req(t, "POST", "/api/v1/uploads", token, jsonBody(create), nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create = %d %s", resp.StatusCode, b)
	}
	id := decode[map[string]any](t, b)["uploadId"].(string)

	// Other devices cannot see the session.
	if resp, _ := e.req(t, "HEAD", "/api/v1/uploads/"+id, other, nil, nil); resp.StatusCode != 404 {
		t.Fatalf("other device HEAD = %d", resp.StatusCode)
	}

	patch := func(off int, chunk []byte) *http.Response {
		resp, _ := e.req(t, "PATCH", "/api/v1/uploads/"+id, token, bytes.NewReader(chunk),
			map[string]string{"Upload-Offset": strconv.Itoa(off), "Content-Type": "application/offset+octet-stream"})
		return resp
	}
	if r := patch(0, data[:4000]); r.StatusCode != 204 || r.Header.Get("Upload-Offset") != "4000" {
		t.Fatalf("chunk 1 = %d %s", r.StatusCode, r.Header.Get("Upload-Offset"))
	}
	// Wrong offset (e.g. a retried chunk) is rejected with the real offset.
	if r := patch(0, data[:10]); r.StatusCode != 409 || r.Header.Get("Upload-Offset") != "4000" {
		t.Fatalf("stale offset = %d %s", r.StatusCode, r.Header.Get("Upload-Offset"))
	}
	// App restarts: creating again resumes the same session.
	resp, b = e.req(t, "POST", "/api/v1/uploads", token, jsonBody(create), nil)
	if out := decode[map[string]any](t, b); resp.StatusCode != 200 || out["uploadId"] != id || out["offset"] != 4000.0 {
		t.Fatalf("resume = %d %s", resp.StatusCode, b)
	}
	// Completing early reports the missing bytes.
	if resp, _ := e.req(t, "POST", "/api/v1/uploads/"+id+"/complete", token, nil, nil); resp.StatusCode != 409 {
		t.Fatalf("early complete = %d", resp.StatusCode)
	}
	if r := patch(4000, append(append([]byte{}, data[4000:]...), "extra"...)); r.StatusCode != 413 {
		t.Fatalf("oversize chunk = %d", r.StatusCode)
	}
	resp, b = e.req(t, "POST", "/api/v1/uploads/"+id+"/complete", token, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("complete = %d %s", resp.StatusCode, b)
	}
	f := decode[FileJSON](t, b)
	if f.Path != "2026/07/IMG_0001.HEIC" || f.Size != int64(len(data)) || !f.Modified.Equal(taken) {
		t.Fatalf("file = %s", b)
	}
	p, _ := e.eng.Store.DataPath("iphone", f.Path)
	if got, _ := os.ReadFile(p); !bytes.Equal(got, data) {
		t.Fatal("stored content mismatch")
	}
	if tmp, _ := os.ReadDir(e.eng.Store.TmpDir()); len(tmp) != 0 {
		t.Fatalf("temp files left behind: %v", tmp)
	}

	// Uploading the same asset again is a no-op.
	resp, b = e.req(t, "POST", "/api/v1/uploads", token, jsonBody(create), nil)
	if resp.StatusCode != 200 || decode[map[string]any](t, b)["status"] != "exists" {
		t.Fatalf("re-upload = %d %s", resp.StatusCode, b)
	}
}

func TestUploadChecksumMismatchAndValidation(t *testing.T) {
	e := newDevEnv(t)
	_, token := e.pairDevice(t, "phone")
	data := []byte("hello")
	bad := map[string]any{"source": "iphone", "path": "a.jpg", "size": 5, "sha256": sum([]byte("other"))}
	_, b := e.req(t, "POST", "/api/v1/uploads", token, jsonBody(bad), nil)
	id := decode[map[string]any](t, b)["uploadId"].(string)
	e.req(t, "PATCH", "/api/v1/uploads/"+id, token, bytes.NewReader(data), map[string]string{"Upload-Offset": "0"})
	if resp, _ := e.req(t, "POST", "/api/v1/uploads/"+id+"/complete", token, nil, nil); resp.StatusCode != 422 {
		t.Fatalf("mismatch complete = %d", resp.StatusCode)
	}
	if resp, _ := e.req(t, "HEAD", "/api/v1/uploads/"+id, token, nil, nil); resp.StatusCode != 404 {
		t.Fatalf("session should be discarded, HEAD = %d", resp.StatusCode)
	}

	for name, body := range map[string]map[string]any{
		"not device source": {"source": "gdrive", "path": "a", "size": 1, "sha256": sum(data)},
		"escaping path":     {"source": "iphone", "path": "../a", "size": 1, "sha256": sum(data)},
		"bad sha":           {"source": "iphone", "path": "a", "size": 1, "sha256": "XYZ"},
		"negative size":     {"source": "iphone", "path": "a", "size": -1, "sha256": sum(data)},
	} {
		if resp, _ := e.req(t, "POST", "/api/v1/uploads", token, jsonBody(body), nil); resp.StatusCode != 400 {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
}

func TestStaleUploadsAreCleaned(t *testing.T) {
	e := newDevEnv(t)
	_, token := e.pairDevice(t, "phone")
	_, b := e.req(t, "POST", "/api/v1/uploads", token, jsonBody(map[string]any{
		"source": "iphone", "path": "a.jpg", "size": 5, "sha256": sum([]byte("hello"))}), nil)
	id := decode[map[string]any](t, b)["uploadId"].(string)
	e.eng.Now = func() time.Time { return time.Now().Add(engine.UploadExpiry + time.Hour) }
	if err := e.eng.Janitor(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.eng.Catalog.GetUpload(context.Background(), id); err == nil {
		t.Fatal("stale upload not removed")
	}
	if tmp, _ := os.ReadDir(e.eng.Store.TmpDir()); len(tmp) != 0 {
		t.Fatalf("temp files left behind: %v", tmp)
	}
}
