package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zac15987/zplex/daemon/config"
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

	// Graceful shutdown — close every PTY session.
	mgr.Shutdown()
	slog.Info("zplex daemon stopped")
}
