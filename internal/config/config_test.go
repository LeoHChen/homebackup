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
