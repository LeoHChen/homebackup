// Command homebackupd is the homebackup server daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/LeoHChen/homebackup/internal/api"
	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/sources"
	"github.com/LeoHChen/homebackup/internal/sources/gdrive"
	"github.com/LeoHChen/homebackup/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "homebackupd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "homebackup.json", "path to the configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Root)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	cat, err := catalog.Open(filepath.Join(st.StateDir(), "catalog.db"))
	if err != nil {
		return fmt.Errorf("open catalog: %w", err)
	}
	defer cat.Close()

	switch args := flag.Args(); {
	case len(args) == 0:
	case args[0] == "auth" && len(args) == 2:
		return runAuth(cfg, st.StateDir(), args[1])
	case args[0] == "pair" && len(args) == 1:
		code, err := cat.NewPairingCode(context.Background(), time.Now(), 10*time.Minute)
		if err != nil {
			return err
		}
		fmt.Printf("Pairing code: %s %s (valid for 10 minutes)\n", code[:3], code[3:])
		fmt.Println("Enter it in the homebackup app on your iPhone or Mac.")
		return nil
	default:
		return fmt.Errorf("usage: homebackupd [-config file] [auth <source-id> | pair]")
	}

	eng := engine.New(st, cat, cfg.Retention, log)
	for _, sc := range cfg.Sources {
		src, err := sources.New(context.Background(), sc, st.StateDir(), log)
		switch {
		case errors.Is(err, sources.ErrNotImplemented):
			log.Warn("source type not implemented yet, skipping", "id", sc.ID, "type", sc.Type)
			continue
		case errors.Is(err, gdrive.ErrNoToken):
			log.Warn("source not signed in, skipping; run: homebackupd -config <file> auth "+sc.ID, "id", sc.ID)
			continue
		case err != nil:
			return fmt.Errorf("source %s: %w", sc.ID, err)
		}
		eng.Register(src, sc.Type, time.Duration(sc.Schedule))
	}

	// Paired devices authenticate with their own tokens. HOMEBACKUP_TOKEN is an
	// optional admin token for scripts and curl.
	tokens := api.Authenticators{api.DeviceTokens{Catalog: cat}}
	if t := os.Getenv("HOMEBACKUP_TOKEN"); t != "" {
		tokens = append(tokens, api.StaticTokens{t})
	}

	started := time.Now().UTC()
	srv := &api.Server{
		Version: version,
		Tokens:  tokens,
		Log:     log,
		Engine:  eng,
		Status: func() api.Status {
			s := api.Status{Version: version, Started: started, Root: st.Root()}
			infos, err := eng.Sources(context.Background())
			if err != nil {
				log.Error("status", "err", err)
			}
			for _, in := range infos {
				s.Sources = append(s.Sources, api.SourceStatus{
					ID: in.ID, Type: in.Type, LastSync: in.State.LastSync, LastErr: in.State.LastError,
				})
			}
			return s
		},
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	engDone := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(engDone)
	}()
	defer func() { <-engDone }()

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "root", cfg.Root, "version", version)
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		stop() // stop the engine too
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
	return nil
}

// runAuth signs a source in to its cloud account and saves the token.
func runAuth(cfg config.Config, stateDir, id string) error {
	for _, sc := range cfg.Sources {
		if sc.ID != id {
			continue
		}
		if sc.Type != "gdrive" {
			return fmt.Errorf("source %s (%s) does not need sign-in", id, sc.Type)
		}
		creds, tokenFile, err := sources.GDriveFiles(sc, stateDir)
		if err != nil {
			return err
		}
		oc, err := gdrive.LoadOAuthConfig(creds)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 10*time.Minute)
		defer cancelTimeout()
		err = gdrive.Authorize(ctx, oc, tokenFile, func(u string) {
			fmt.Println("Open this link in a browser on this computer and sign in to Google:")
			fmt.Println()
			fmt.Println("  " + u)
			fmt.Println()
			openBrowser(u)
		})
		if err != nil {
			return err
		}
		fmt.Println("Signed in. Token saved to", tokenFile)
		return nil
	}
	return fmt.Errorf("no source with id %q in config", id)
}

// openBrowser tries to open u in the default browser; failures are ignored
// because the link is also printed.
func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}
