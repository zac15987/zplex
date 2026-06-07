// Package prefs provides persistent storage for app-managed user preferences.
//
// Unlike the config package (read-only, user-hand-edited ~/.zplex/config.toml),
// prefs owns mutable state that the application writes back at runtime. It is
// persisted to ~/.zplex/preferences.json so preferences survive daemon restarts.
package prefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// closeAction enumerates the valid values for a close preference.
// An empty string means "unset" (no remembered choice).
const (
	actionDetach = "detach"
	actionKill   = "kill"
)

// Preferences holds app-managed user preferences.
// Each close field is "detach", "kill", or "" (unset).
type Preferences struct {
	PanelClose     string `json:"panel_close"`
	WorkspaceClose string `json:"workspace_close"`
}

// Store guards in-memory preferences with a mutex and persists them to disk.
type Store struct {
	mu    sync.Mutex
	path  string
	prefs Preferences
}

// NewStore resolves ~/.zplex/preferences.json and loads any existing values.
// A missing file is not an error — the store starts with zero-value (unset)
// preferences. A malformed file returns an error so the caller can decide
// whether to fall back to an empty store.
func NewStore() (*Store, error) {
	slog.Info("prefs.NewStore: initializing preferences store")

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("unable to determine home directory: %w", err)
	}

	return NewStoreWithPath(filepath.Join(home, ".zplex", "preferences.json"))
}

// NewStoreWithPath creates a store backed by the given file path and loads any
// existing values. It is exported primarily so tests can use a temp file
// instead of the real ~/.zplex/preferences.json.
func NewStoreWithPath(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewEmptyStore returns a store with zero-value preferences, used as a fallback
// when NewStore fails (e.g. a corrupt preferences file). It resolves the file
// path on a best-effort basis so later writes can still persist; if the home
// directory cannot be determined, the path is empty and Set will fail loudly.
func NewEmptyStore() *Store {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("prefs.NewEmptyStore: unable to determine home directory",
			slog.String("error", err.Error()),
		)
		return &Store{}
	}
	return &Store{path: filepath.Join(home, ".zplex", "preferences.json")}
}

// load reads preferences from disk into memory. A missing file leaves the
// zero-value preferences in place without error.
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No preferences file yet — use zero values.
			return nil
		}
		return fmt.Errorf("reading preferences file: %w", err)
	}

	var p Preferences
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("parsing preferences file %q: %w", s.path, err)
	}

	s.prefs = p
	slog.Info("prefs.load: loaded preferences", slog.String("path", s.path))
	return nil
}

// Get returns a copy of the current preferences.
func (s *Store) Get() Preferences {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefs
}

// Set validates the given preferences, updates the in-memory state, and
// persists them to disk. If validation or persistence fails, the in-memory
// state is left unchanged.
func (s *Store) Set(p Preferences) error {
	if err := validate(p); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.save(p); err != nil {
		return err
	}

	s.prefs = p
	slog.Info("prefs.Set: preferences saved",
		slog.String("panel_close", p.PanelClose),
		slog.String("workspace_close", p.WorkspaceClose),
	)
	return nil
}

// save writes preferences atomically: encode to a temp file in the same
// directory, then rename over the target. This avoids leaving a half-written
// file if the process is interrupted mid-write.
func (s *Store) save(p Preferences) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating preferences directory: %w", err)
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding preferences: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp preferences file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		// Best-effort cleanup of the temp file on rename failure.
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming temp preferences file: %w", err)
	}

	return nil
}

// validate ensures every close field is "detach", "kill", or "".
func validate(p Preferences) error {
	if !validCloseAction(p.PanelClose) {
		return fmt.Errorf("invalid panel_close value: %q", p.PanelClose)
	}
	if !validCloseAction(p.WorkspaceClose) {
		return fmt.Errorf("invalid workspace_close value: %q", p.WorkspaceClose)
	}
	return nil
}

func validCloseAction(v string) bool {
	return v == "" || v == actionDetach || v == actionKill
}
