package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/LeoHChen/homebackup/internal/logbuf"
	"github.com/LeoHChen/homebackup/internal/web"
)

func TestPairCodeFromPairedDevice(t *testing.T) {
	e := newDevEnv(t)
	if resp, _ := e.req(t, "POST", "/api/v1/pair/codes", "", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("unauthenticated = %d, want 401", resp.StatusCode)
	}
	_, token := e.pairDevice(t, "Leo's Mac")
	resp, b := e.req(t, "POST", "/api/v1/pair/codes", token, nil, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("pair/codes = %d %s", resp.StatusCode, b)
	}
	out := decode[map[string]any](t, b)
	code, _ := out["code"].(string)
	if !regexp.MustCompile(`^\d{6}$`).MatchString(code) {
		t.Fatalf("code = %q, want 6 digits", code)
	}
	exp, err := time.Parse(time.RFC3339, out["expiresAt"].(string))
	if err != nil || time.Until(exp) < 9*time.Minute || time.Until(exp) > 11*time.Minute {
		t.Fatalf("expiresAt = %v (%v)", out["expiresAt"], err)
	}
	// A browser can pair with it.
	resp, b = e.req(t, "POST", "/api/v1/pair", "", jsonBody(map[string]string{
		"code": code, "deviceName": "Safari on Mac", "platform": "web"}), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("browser pair = %d %s", resp.StatusCode, b)
	}
	resp, b = e.req(t, "GET", "/api/v1/devices", token, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"platform":"web"`) {
		t.Fatalf("devices = %d %s", resp.StatusCode, b)
	}
}

func TestRecentLog(t *testing.T) {
	ring := logbuf.New(10)
	for i := range 5 {
		fmt.Fprintf(ring, "line %d\n", i)
	}
	s := &Server{Version: "test", Tokens: StaticTokens{"secret"}, Logs: ring,
		Status: func() Status { return Status{} }}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if resp := get(t, ts.URL+"/api/v1/log", ""); resp.StatusCode != 401 {
		t.Fatalf("unauthenticated = %d", resp.StatusCode)
	}
	resp := get(t, ts.URL+"/api/v1/log?n=2", "secret")
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "{\"lines\":[\"line 3\",\"line 4\"]}\n" {
		t.Fatalf("log = %d %s", resp.StatusCode, b)
	}
	if resp := get(t, ts.URL+"/api/v1/log?n=0", "secret"); resp.StatusCode != 400 {
		t.Fatalf("n=0 = %d, want 400", resp.StatusCode)
	}
	// Without a log source the endpoint still answers.
	s2 := &Server{Version: "test", Tokens: StaticTokens{"secret"}, Status: func() Status { return Status{} }}
	ts2 := httptest.NewServer(s2.Handler())
	defer ts2.Close()
	resp = get(t, ts2.URL+"/api/v1/log", "secret")
	b, _ = io.ReadAll(resp.Body)
	if string(b) != "{\"lines\":[]}\n" {
		t.Fatalf("no source = %s", b)
	}
}

func TestDashboardServed(t *testing.T) {
	s := &Server{Version: "test", Tokens: StaticTokens{"secret"}, UI: web.Handler(),
		Status: func() Status { return Status{} }}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	cases := []struct {
		path   string
		status int
		ctype  string
		body   string
	}{
		{"/", 200, "text/html", "HomeBackup"},
		{"/static/app.js", 200, "javascript", "api/v1"},
		{"/static/style.css", 200, "text/css", ""},
		{"/favicon.ico", 200, "image/svg+xml", "<svg"},
		{"/nope", 404, "", ""},
		{"/api/v1/nope", 404, "", ""},
	}
	for _, c := range cases {
		resp := get(t, ts.URL+c.path, "")
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != c.status {
			t.Errorf("%s: status = %d, want %d", c.path, resp.StatusCode, c.status)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, c.ctype) {
			t.Errorf("%s: content-type = %q, want %q", c.path, ct, c.ctype)
		}
		if !strings.Contains(string(b), c.body) {
			t.Errorf("%s: body does not contain %q", c.path, c.body)
		}
	}
	// The API must still win over the catch-all.
	if resp := get(t, ts.URL+"/api/v1/health", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %d", resp.StatusCode)
	}
}
