package gdrive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
)

// Scope is the OAuth scope requested: read-only access to Drive.
const Scope = "https://www.googleapis.com/auth/drive.readonly"

// LoadOAuthConfig reads an OAuth client JSON file downloaded from the Google
// Cloud Console (type "Desktop app").
func LoadOAuthConfig(credentialsFile string) (*oauth2.Config, error) {
	b, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, err
	}
	var f struct {
		Installed *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			AuthURI      string `json:"auth_uri"`
			TokenURI     string `json:"token_uri"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", credentialsFile, err)
	}
	if f.Installed == nil || f.Installed.ClientID == "" {
		return nil, fmt.Errorf("%s is not a Desktop app OAuth client file (missing \"installed\")", credentialsFile)
	}
	in := f.Installed
	if in.AuthURI == "" {
		in.AuthURI = "https://accounts.google.com/o/oauth2/auth"
	}
	if in.TokenURI == "" {
		in.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return &oauth2.Config{
		ClientID:     in.ClientID,
		ClientSecret: in.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: in.AuthURI, TokenURL: in.TokenURI},
		Scopes:       []string{Scope},
	}, nil
}

// ErrNoToken is returned when the source has not been authorized yet.
var ErrNoToken = errors.New("not signed in to Google Drive: run `homebackupd auth <source-id>`")

// NewClient returns an HTTP client that authenticates with the saved token
// and saves refreshed tokens back to tokenFile.
func NewClient(ctx context.Context, cfg *oauth2.Config, tokenFile string) (*http.Client, error) {
	tok, err := readToken(tokenFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoToken
	}
	if err != nil {
		return nil, err
	}
	ts := &savingTokenSource{
		base: cfg.TokenSource(ctx, tok),
		file: tokenFile,
		last: tok.AccessToken,
	}
	return oauth2.NewClient(ctx, oauth2.ReuseTokenSource(tok, ts)), nil
}

type savingTokenSource struct {
	base oauth2.TokenSource
	file string

	mu   sync.Mutex
	last string
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	t, err := s.base.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		if err := writeToken(s.file, t); err != nil {
			return nil, fmt.Errorf("save refreshed token: %w", err)
		}
	}
	return t, nil
}

func readToken(file string) (*oauth2.Token, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var t oauth2.Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	return &t, nil
}

func writeToken(file string, t *oauth2.Token) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// Authorize runs the OAuth "loopback" flow: it listens on 127.0.0.1, sends the
// user to Google's consent page via showURL, and saves the token to tokenFile.
func Authorize(ctx context.Context, cfg *oauth2.Config, tokenFile string, showURL func(string)) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	c := *cfg
	c.RedirectURL = fmt.Sprintf("http://%s/", ln.Addr())

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return err
	}
	state := hex.EncodeToString(stateBytes)
	verifier := oauth2.GenerateVerifier()

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		case q.Get("error") != "":
			res.err = fmt.Errorf("authorization denied: %s", q.Get("error"))
		default:
			res.code = q.Get("code")
		}
		if res.err != nil {
			fmt.Fprintln(w, "Authorization failed. You can close this window.")
		} else {
			fmt.Fprintln(w, "homebackup is now connected to Google Drive. You can close this window.")
		}
		select {
		case done <- res:
		default:
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	showURL(c.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier)))

	var res result
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res = <-done:
	}
	if res.err != nil {
		return res.err
	}
	tok, err := c.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("Google did not return a refresh token; remove homebackup's access at https://myaccount.google.com/permissions and try again")
	}
	return writeToken(tokenFile, tok)
}
