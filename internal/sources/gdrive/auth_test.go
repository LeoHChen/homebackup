package gdrive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthorizeLoopbackFlow(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenSrv.Close()

	dir := t.TempDir()
	creds := filepath.Join(dir, "client.json")
	os.WriteFile(creds, []byte(fmt.Sprintf(`{"installed":{"client_id":"cid","client_secret":"sec","auth_uri":"https://accounts.example/auth","token_uri":%q}}`, tokenSrv.URL)), 0o600)
	cfg, err := LoadOAuthConfig(creds)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(dir, "state", "token.json")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = Authorize(ctx, cfg, tokenFile, func(authURL string) {
		u, _ := url.Parse(authURL)
		q := u.Query()
		if q.Get("access_type") != "offline" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != Scope {
			t.Errorf("auth URL missing params: %s", authURL)
		}
		// Simulate the browser redirect, first with a forged state.
		redirect := q.Get("redirect_uri")
		go func() {
			if resp, err := http.Get(redirect + "?state=forged&code=evil"); err == nil {
				resp.Body.Close()
			}
			resp, err := http.Get(redirect + "?" + url.Values{"state": {q.Get("state")}, "code": {"the-code"}}.Encode())
			if err == nil {
				resp.Body.Close()
			}
		}()
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := readToken(tokenFile)
	if err != nil || tok.RefreshToken != "rt" {
		t.Fatalf("token = %+v, %v", tok, err)
	}
	if fi, _ := os.Stat(tokenFile); fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
	}

	if _, err := NewClient(ctx, cfg, filepath.Join(dir, "missing.json")); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	if _, err := NewClient(ctx, cfg, tokenFile); err != nil {
		t.Fatal(err)
	}
}

func TestLoadOAuthConfigRejectsWebClient(t *testing.T) {
	p := filepath.Join(t.TempDir(), "web.json")
	os.WriteFile(p, []byte(`{"web":{"client_id":"x"}}`), 0o600)
	if _, err := LoadOAuthConfig(p); err == nil {
		t.Fatal("expected error for non-desktop client")
	}
}
