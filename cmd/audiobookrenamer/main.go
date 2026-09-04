// Command audiobookrenamer runs the audiobook library manager: a web app that
// scans a library folder, matches books to online metadata, and renames files
// and folders in place to a configurable layout. It has no download features.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"audiobookrenamer/internal/api"
	"audiobookrenamer/internal/config"
	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/matcher"
	"audiobookrenamer/internal/metadata"
	"audiobookrenamer/internal/organize"
	"audiobookrenamer/internal/scanner"
	"audiobookrenamer/internal/worker"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", envOr("ABR_CONFIG_FILE", ""), "path to JSON config file (optional)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	setupLogging(cfg.LogLevel)
	slog.Info("starting", "version", version, "addr", cfg.Addr, "config_dir", cfg.ConfigDir)

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer database.Close()

	wm := worker.New(database, 2)
	defer wm.Shutdown()
	scanner.Register(wm, database)

	registry := metadata.NewRegistry(database)
	mm := matcher.New(database, registry, metadata.NewClient(database))
	matcher.Register(wm, mm)
	tagBackupDir := filepath.Join(cfg.ConfigDir, "tagbackups")
	organize.Register(wm, organize.NewService(database, tagBackupDir))

	apiSrv, err := api.New(cfg, database, wm, mm)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		// End long-lived handlers (SSE) first so Shutdown doesn't block on them.
		apiSrv.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func setupLogging(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}
