package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestServer() *httptest.Server {
	s := &Server{
		Version: "test",
		Tokens:  StaticTokens{"secret"},
		Status: func() Status {
			return Status{Version: "test", Started: time.Unix(0, 0).UTC(), Root: "/data"}
		},
	}
	return httptest.NewServer(s.Handler())
}

func get(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestHealthNeedsNoAuth(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	resp := get(t, ts.URL+"/api/v1/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestStatusRequiresToken(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	for _, tok := range []string{"", "wrong"} {
		resp := get(t, ts.URL+"/api/v1/status", tok)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", tok, resp.StatusCode)
		}
		var body map[string]Error
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body["error"].Code != "unauthorized" {
			t.Errorf("token %q: body = %v, %v", tok, body, err)
		}
	}
	resp := get(t, ts.URL+"/api/v1/status", "secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var st Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Root != "/data" || st.Sources == nil {
		t.Errorf("unexpected status: %+v", st)
	}
}

func TestStaticTokensIgnoresEmpty(t *testing.T) {
	if _, ok := (StaticTokens{""}).Authenticate(context.Background(), ""); ok {
		t.Fatal("empty token must never be valid")
	}
}
