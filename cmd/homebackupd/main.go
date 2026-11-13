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
	"syscall"
	"time"

	"github.com/LeoHChen/homebackup/internal/api"
	"github.com/LeoHChen/homebackup/internal/config"
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
		Status: func() api.Status {
			s := api.Status{Version: version, Started: started, Root: st.Root()}
			for _, src := range cfg.Sources {
				s.Sources = append(s.Sources, api.SourceStatus{ID: src.ID, Type: src.Type})
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

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "root", cfg.Root, "version", version)
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
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
