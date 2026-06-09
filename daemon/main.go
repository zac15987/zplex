package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zac15987/zplex/daemon/config"
	"github.com/zac15987/zplex/daemon/prefs"
	"github.com/zac15987/zplex/daemon/server"
	"github.com/zac15987/zplex/daemon/session"
)

func main() {
	// Load config (calls flag.Parse() internally).
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Create SessionManager with config-driven defaults.
	mgr := session.NewSessionManager(cfg.DefaultShell, cfg.BufferSize)

	// Load persisted user preferences. A corrupt/unreadable file must not
	// block daemon startup — fall back to an empty in-memory store.
	prefsStore, err := prefs.NewStore()
	if err != nil {
		slog.Warn("failed to load preferences, starting with empty store",
			slog.String("error", err.Error()),
		)
		prefsStore = prefs.NewEmptyStore()
	}

	// Build the HTTP/WebSocket server wired to the session manager.
	srv := server.NewServer(mgr, prefsStore, cfg.Zpit)

	// Auto-launch the fixed zpit cockpit session when enabled. A failure here
	// (e.g. zpit binary not on PATH) must not bring down the daemon.
	if cfg.Zpit.Enabled {
		if _, err := srv.LaunchZpit(); err != nil {
			slog.Warn("zpit auto-launch failed", slog.String("error", err.Error()))
		} else {
			slog.Info("zpit fixed session auto-launched")
		}
	}

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: srv.Handler(),
	}

	// Start the HTTP server in a background goroutine.
	go func() {
		slog.Info("zplex daemon listening",
			slog.String("addr", httpServer.Addr),
		)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	slog.Info("zplex daemon started",
		slog.Int("port", cfg.Port),
		slog.String("shell", cfg.DefaultShell),
		slog.Int("buffer_size", cfg.BufferSize),
	)

	// Block until SIGINT or SIGTERM. On Windows SIGTERM is not delivered by
	// the OS, but os.Interrupt covers Ctrl+C. We include syscall.SIGTERM
	// for Unix compatibility.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	sig := <-sigCh
	slog.Info("received shutdown signal", slog.String("signal", sig.String()))

	// Graceful shutdown: stop accepting new connections (5s timeout),
	// then close all PTY sessions.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP server shutdown error", slog.String("error", err.Error()))
	}
	slog.Info("HTTP server stopped")

	mgr.Shutdown()
	slog.Info("zplex daemon stopped")
}
