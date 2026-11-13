// Package sources builds source implementations from configuration.
package sources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/source"
	"github.com/LeoHChen/homebackup/internal/sources/device"
	"github.com/LeoHChen/homebackup/internal/sources/gdrive"
	"github.com/LeoHChen/homebackup/internal/sources/takeout"
)

// ErrNotImplemented is returned for source types that are planned but not built yet.
var ErrNotImplemented = errors.New("source type not implemented")

// GDriveFiles returns the credentials and token file paths for a gdrive source.
func GDriveFiles(c config.Source, stateDir string) (credentials, token string, err error) {
	credentials = c.Options["credentials"]
	if credentials == "" {
		return "", "", fmt.Errorf("source %s: options.credentials (OAuth client JSON file) is required", c.ID)
	}
	token = c.Options["token"]
	if token == "" {
		token = filepath.Join(stateDir, c.ID+"-token.json")
	}
	return credentials, token, nil
}

// New returns the source for a configuration entry. stateDir holds per-source
// state such as OAuth tokens.
func New(ctx context.Context, c config.Source, stateDir string, log *slog.Logger) (source.Source, error) {
	switch c.Type {
	case "gdrive":
		creds, tokenFile, err := GDriveFiles(c, stateDir)
		if err != nil {
			return nil, err
		}
		cfg, err := gdrive.LoadOAuthConfig(creds)
		if err != nil {
			return nil, err
		}
		client, err := gdrive.NewClient(ctx, cfg, tokenFile)
		if err != nil {
			return nil, err
		}
		return gdrive.New(gdrive.Options{
			ID:        c.ID,
			Client:    client,
			StateFile: filepath.Join(stateDir, c.ID+"-folders.json"),
			Log:       log,
		})
	case "gphotos-takeout":
		return takeout.New(c.ID, c.Options["inbox"], log)
	case "device-upload", "drive":
		// Both receive files through the API rather than pulling them.
		return device.New(c.ID), nil
	case "icloud-local":
		return nil, ErrNotImplemented
	default:
		return nil, fmt.Errorf("unknown source type %q", c.Type)
	}
}
