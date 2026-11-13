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
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LeoHChen/homebackup/internal/api"
	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/config"
	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/sources"
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

	eng := engine.New(st, cat, cfg.Retention, log)
	for _, sc := range cfg.Sources {
		src, err := sources.New(sc)
		if errors.Is(err, sources.ErrNotImplemented) {
			log.Warn("source type not implemented yet, skipping", "id", sc.ID, "type", sc.Type)
			continue
		}
		if err != nil {
			return fmt.Errorf("source %s: %w", sc.ID, err)
		}
		eng.Register(src, sc.Type, time.Duration(sc.Schedule))
	}

	// Until device pairing lands (step 2), a single token can be supplied via env.
	var tokens api.StaticTokens
	if t := os.Getenv("HOMEBACKUP_TOKEN"); t != "" {
		tokens = append(tokens, t)
	} else {
		log.Warn("HOMEBACKUP_TOKEN not set: authenticated endpoints will reject all requests")
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
