// Package config provides configuration loading for the zplex daemon.
// It reads from TOML files, environment variables, and CLI flags with
// a well-defined priority: defaults < config file < env vars < CLI flags.
package config

import (
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
)

// Default values for daemon configuration.
const (
	DefaultPort       = 17732
	DefaultShell      = "pwsh"
	DefaultBufferSize = 102400
)

// Default values for zpit integration configuration.
const (
	DefaultZpitBin = "zpit"
)

// ZpitConfig holds configuration for the zpit integration.
type ZpitConfig struct {
	Enabled bool
	Bin     string
	Config  string
	Args    []string
}

// Config holds the runtime configuration for the zplex daemon.
type Config struct {
	Port         int
	DefaultShell string
	BufferSize   int
	Zpit         ZpitConfig
}

// fileConfig mirrors the TOML file structure with [daemon] and [zpit] sections.
type fileConfig struct {
	Daemon daemonConfig   `toml:"daemon"`
	Zpit   zpitFileConfig `toml:"zpit"`
}

// daemonConfig maps the fields inside the [daemon] TOML section.
type daemonConfig struct {
	Port         int    `toml:"port"`
	DefaultShell string `toml:"default_shell"`
	BufferSize   int    `toml:"buffer_size"`
}

// zpitFileConfig maps the fields inside the [zpit] TOML section.
// Enabled is a *bool to distinguish "not set" (nil → keep default true)
// from explicit "enabled = false" (false).
type zpitFileConfig struct {
	Enabled *bool    `toml:"enabled"`
	Bin     string   `toml:"bin"`
	Config  string   `toml:"config"`
	Args    []string `toml:"args"`
}

// Load builds a Config by layering sources in priority order:
// hardcoded defaults < ~/.zplex/config.toml < environment variables < CLI flags.
func Load() (*Config, error) {
	slog.Info("config.Load: starting configuration load")

	// Layer 0: hardcoded defaults.
	cfg := &Config{
		Port:         DefaultPort,
		DefaultShell: DefaultShell,
		BufferSize:   DefaultBufferSize,
		Zpit: ZpitConfig{
			Enabled: true,
			Bin:     DefaultZpitBin,
			Config:  "",
			Args:    []string{},
		},
	}

	// Layer 1: TOML config file.
	if err := applyFileConfig(cfg); err != nil {
		return nil, err
	}

	// Layer 2: environment variables.
	applyEnvConfig(cfg)

	// Layer 3: CLI flags (highest priority).
	applyCLIFlags(cfg)

	slog.Info("config.Load: configuration loaded",
		slog.Int("port", cfg.Port),
		slog.String("default_shell", cfg.DefaultShell),
		slog.Int("buffer_size", cfg.BufferSize),
		slog.Bool("zpit_enabled", cfg.Zpit.Enabled),
		slog.String("zpit_bin", cfg.Zpit.Bin),
	)
	return cfg, nil
}

// applyFileConfig reads ~/.zplex/config.toml and applies any values found.
// If the file does not exist, it silently returns without error.
func applyFileConfig(cfg *Config) error {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("config: unable to determine home directory, skipping config file",
			slog.String("error", err.Error()),
		)
		return nil
	}

	path := filepath.Join(home, ".zplex", "config.toml")
	return applyFileConfigFromPath(cfg, path)
}

// applyFileConfigFromPath reads the TOML config at the given path and applies
// any values found into cfg. If the file does not exist, it silently returns
// without error. This helper is separate from applyFileConfig to allow unit tests
// to supply a temp-file path without touching the user's home directory.
func applyFileConfigFromPath(cfg *Config, path string) error {
	var fc fileConfig
	_, err := toml.DecodeFile(path, &fc)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No config file — use defaults without logging an error.
			return nil
		}
		return err
	}

	slog.Info("config: loaded config file", slog.String("path", path))

	if fc.Daemon.Port != 0 {
		cfg.Port = fc.Daemon.Port
	}
	if fc.Daemon.DefaultShell != "" {
		cfg.DefaultShell = fc.Daemon.DefaultShell
	}
	if fc.Daemon.BufferSize != 0 {
		cfg.BufferSize = fc.Daemon.BufferSize
	}

	if fc.Zpit.Enabled != nil {
		cfg.Zpit.Enabled = *fc.Zpit.Enabled
	}
	if fc.Zpit.Bin != "" {
		cfg.Zpit.Bin = fc.Zpit.Bin
	}
	if fc.Zpit.Config != "" {
		cfg.Zpit.Config = fc.Zpit.Config
	}
	if len(fc.Zpit.Args) > 0 {
		cfg.Zpit.Args = fc.Zpit.Args
	}

	return nil
}

// applyEnvConfig overrides config values with ZPLEX_PORT and ZPLEX_SHELL
// environment variables when they are set.
func applyEnvConfig(cfg *Config) {
	if portStr := os.Getenv("ZPLEX_PORT"); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			slog.Warn("config: invalid ZPLEX_PORT value, ignoring",
				slog.String("value", portStr),
				slog.String("error", err.Error()),
			)
		} else {
			cfg.Port = port
		}
	}

	if shell := os.Getenv("ZPLEX_SHELL"); shell != "" {
		cfg.DefaultShell = shell
	}
}

// applyCLIFlags parses --port and --shell flags from the command line.
// CLI flags take highest priority and override all other sources.
func applyCLIFlags(cfg *Config) {
	port := flag.Int("port", cfg.Port, "daemon listen port")
	shell := flag.String("shell", cfg.DefaultShell, "default shell for new sessions")

	flag.Parse()

	cfg.Port = *port
	cfg.DefaultShell = *shell
}
