// Command audiobookrenamer runs the audiobook library manager: a web app that
// scans a library folder, matches books to online metadata, and renames files
// and folders in place to a configurable layout. It has no download features.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
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
	"audiobookrenamer/internal/selfupdate"
	"audiobookrenamer/internal/worker"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// shutdownTimeout bounds the graceful HTTP shutdown on both the signal path and
// the self-update restart path.
const shutdownTimeout = 15 * time.Second

func main() {
	u, err := run()
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
	// A non-nil updater means run() shut the server down for a self-update.
	// run()'s deferred cleanup (worker drain, DB close) has fully completed by
	// now, so this is a clean handoff. If it fails there is no listener left, so
	// exit non-zero and let the service manager (Restart=always) take over
	// rather than linger as a dead process.
	if u != nil {
		slog.Info("self-update: restarting into new binary", "path", u.ExecPath())
		if err := u.Exec(); err != nil {
			slog.Error("self-update: restart handoff failed", "err", err)
			os.Exit(1)
		}
	}
}

// run starts the server and blocks until it should stop. It returns a non-nil
// *selfupdate.Updater only when it stopped to hand off to a freshly installed
// binary; main then calls Exec after run's deferred cleanup has finished.
func run() (*selfupdate.Updater, error) {
	configPath := flag.String("config", envOr("ABR_CONFIG_FILE", ""), "path to JSON config file (optional)")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil, nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return nil, err
	}
	setupLogging(cfg.LogLevel)
	slog.Info("starting", "version", version, "addr", cfg.Addr, "config_dir", cfg.ConfigDir)

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		return nil, err
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

	apiSrv, err := api.New(cfg, database, wm, mm, version)
	if err != nil {
		return nil, err
	}
	selfupdate.Register(wm, apiSrv.Updater)

	if !apiSrv.AuthEnabled() && !bindsLoopback(cfg.Addr) {
		slog.Warn("authentication is disabled and the server is not bound to a loopback address; "+
			"the API, including self-update, is reachable without authentication", "addr", cfg.Addr)
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

	// End long-lived handlers (SSE) before srv.Shutdown so it does not wait them
	// out, then shut the server down within shutdownTimeout.
	drainAndShutdown := func() error {
		apiSrv.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}

	select {
	case err := <-errCh:
		return nil, err

	case <-ctx.Done():
		slog.Info("shutting down")
		return nil, drainAndShutdown()

	case <-apiSrv.Updater.RestartRequested():
		slog.Info("self-update: draining before restart")
		if err := drainAndShutdown(); err != nil {
			// The listener is already closed by Shutdown even on timeout, so the
			// replacement can still bind. Log and proceed with the handoff.
			slog.Error("self-update: graceful shutdown did not complete, restarting anyway", "err", err)
		}
		return apiSrv.Updater, nil
	}
}

// bindsLoopback reports whether addr (an http.Server Addr like ":8674",
// "127.0.0.1:8674", or "[::1]:8674") binds only a loopback interface. An empty
// host binds every interface and is not loopback.
func bindsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "":
		return false
	case "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
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
