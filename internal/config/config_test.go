package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "homebackup.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	p := writeFile(t, `{"root": "/data", "sources": [{"id": "gdrive", "type": "gdrive", "schedule": "1h"}]}`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8420" {
		t.Errorf("Listen = %q, want :8420", cfg.Listen)
	}
	if cfg.Retention.TrashDays != 30 || cfg.Retention.MaxVersions != 100 {
		t.Errorf("retention defaults not applied: %+v", cfg.Retention)
	}
	if time.Duration(cfg.Sources[0].Schedule) != time.Hour {
		t.Errorf("schedule = %v, want 1h", time.Duration(cfg.Sources[0].Schedule))
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	tests := map[string]struct {
		body string
		want string
	}{
		"missing root":   {`{}`, "root is required"},
		"unknown type":   {`{"root":"/d","sources":[{"id":"x","type":"dropbox"}]}`, "unknown type"},
		"duplicate id":   {`{"root":"/d","sources":[{"id":"a","type":"gdrive"},{"id":"a","type":"gdrive"}]}`, "duplicate id"},
		"unknown field":  {`{"root":"/d","bogus":1}`, "unknown field"},
		"bad duration":   {`{"root":"/d","sources":[{"id":"a","type":"gdrive","schedule":"soon"}]}`, "invalid duration"},
		"negative trash": {`{"root":"/d","retention":{"trashDays":-1}}`, "must not be negative"},
		"bad tls mode":   {`{"root":"/d","tls":{"mode":"magic"}}`, "unknown tls mode"},
		"files no cert":  {`{"root":"/d","tls":{"mode":"files"}}`, "needs certFile"},
		"bad listen":     {`{"root":"/d","listen":"8420"}`, "listen"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeFile(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestTLSAndBonjourDefaults(t *testing.T) {
	no := false
	for _, tc := range []struct {
		cfg     Config
		tls     string
		bonjour bool
	}{
		{Config{Listen: "127.0.0.1:8420"}, "off", false},
		{Config{Listen: "localhost:8420"}, "off", false},
		{Config{Listen: "[::1]:8420"}, "off", false},
		{Config{Listen: ":8420"}, "self-signed", true},
		{Config{Listen: "192.168.1.5:8420"}, "self-signed", true},
		{Config{Listen: ":8420", TLS: TLS{Mode: "off"}, Bonjour: &no}, "off", false},
		{Config{Listen: "127.0.0.1:8420", TLS: TLS{Mode: "files"}}, "files", false},
	} {
		if got := tc.cfg.TLSMode(); got != tc.tls {
			t.Errorf("%s: TLSMode = %s, want %s", tc.cfg.Listen, got, tc.tls)
		}
		if got := tc.cfg.BonjourEnabled(); got != tc.bonjour {
			t.Errorf("%s: BonjourEnabled = %v, want %v", tc.cfg.Listen, got, tc.bonjour)
		}
	}
}
