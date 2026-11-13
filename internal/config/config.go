// Package config loads the homebackupd configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Duration is a time.Duration that unmarshals from strings like "1h" or "30m".
type Duration time.Duration

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"1h\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Retention controls how long deleted files and old versions are kept.
type Retention struct {
	TrashDays   int `json:"trashDays"`
	VersionDays int `json:"versionDays"`
	MaxVersions int `json:"maxVersions"`
}

// Source configures one backup source.
type Source struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Schedule Duration          `json:"schedule"`
	Options  map[string]string `json:"options,omitempty"`
}

// Config is the top-level configuration.
type Config struct {
	Listen    string    `json:"listen"`
	Root      string    `json:"root"`
	TLS       TLS       `json:"tls"`
	Bonjour   *bool     `json:"bonjour,omitempty"`
	Retention Retention `json:"retention"`
	Sources   []Source  `json:"sources"`
}

// TLS configures HTTPS.
type TLS struct {
	// Mode is "auto" (default: HTTPS with a self-signed certificate unless
	// listening only on loopback), "self-signed", "files", or "off".
	Mode     string `json:"mode,omitempty"`
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
}

// LoopbackOnly reports whether Listen accepts connections only from this machine.
func (c Config) LoopbackOnly() bool {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// TLSMode resolves "auto" to "self-signed" or "off".
func (c Config) TLSMode() string {
	switch c.TLS.Mode {
	case "", "auto":
		if c.LoopbackOnly() {
			return "off"
		}
		return "self-signed"
	}
	return c.TLS.Mode
}

// BonjourEnabled reports whether to advertise on the local network
// (default: on unless listening only on loopback).
func (c Config) BonjourEnabled() bool {
	if c.Bonjour != nil {
		return *c.Bonjour
	}
	return !c.LoopbackOnly()
}

// KnownSourceTypes lists the source types the server understands.
var KnownSourceTypes = map[string]bool{
	"gdrive":          true,
	"gphotos-takeout": true,
	"device-upload":   true,
	"drive":           true,
	"icloud-local":    true,
}

// Default returns a configuration with default values filled in.
func Default() Config {
	return Config{
		Listen: ":8420",
		Retention: Retention{
			TrashDays:   30,
			VersionDays: 30,
			MaxVersions: 100,
		},
	}
}

// Load reads and validates a configuration file.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()

	cfg := Default()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks the configuration for errors.
func (c Config) Validate() error {
	var errs []error
	if c.Root == "" {
		errs = append(errs, errors.New("root is required"))
	}
	switch c.TLS.Mode {
	case "", "auto", "self-signed", "off":
	case "files":
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			errs = append(errs, errors.New("tls mode \"files\" needs certFile and keyFile"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown tls mode %q", c.TLS.Mode))
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("listen: %w", err))
	}
	if c.Retention.TrashDays < 0 || c.Retention.VersionDays < 0 || c.Retention.MaxVersions < 0 {
		errs = append(errs, errors.New("retention values must not be negative"))
	}
	seen := map[string]bool{}
	for i, s := range c.Sources {
		if s.ID == "" {
			errs = append(errs, fmt.Errorf("sources[%d]: id is required", i))
		} else if seen[s.ID] {
			errs = append(errs, fmt.Errorf("sources[%d]: duplicate id %q", i, s.ID))
		}
		seen[s.ID] = true
		if !KnownSourceTypes[s.Type] {
			errs = append(errs, fmt.Errorf("sources[%d]: unknown type %q", i, s.Type))
		}
	}
	return errors.Join(errs...)
}
