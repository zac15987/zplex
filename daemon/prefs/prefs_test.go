package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// newTestStore builds a Store rooted at a temp file so tests never touch the
// real ~/.zplex/preferences.json.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "preferences.json")
	s := &Store{path: path}
	if err := s.load(); err != nil {
		t.Fatalf("initial load failed: %v", err)
	}
	return s
}

func TestLoadMissingFileReturnsZeroValue(t *testing.T) {
	s := newTestStore(t)
	got := s.Get()
	if got.PanelClose != "" || got.WorkspaceClose != "" {
		t.Errorf("expected zero-value preferences, got %+v", got)
	}
}

func TestSetGetRoundTrip(t *testing.T) {
	s := newTestStore(t)

	want := Preferences{PanelClose: "kill", WorkspaceClose: "detach"}
	if err := s.Set(want); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	if got := s.Get(); got != want {
		t.Errorf("Get after Set: got %+v, want %+v", got, want)
	}

	// A fresh store reading the same file must see the persisted values.
	reloaded := &Store{path: s.path}
	if err := reloaded.load(); err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if got := reloaded.Get(); got != want {
		t.Errorf("reloaded preferences: got %+v, want %+v", got, want)
	}
}

func TestSetRejectsInvalidValues(t *testing.T) {
	s := newTestStore(t)

	cases := []Preferences{
		{PanelClose: "foo"},
		{WorkspaceClose: "explode"},
		{PanelClose: "kill", WorkspaceClose: "nope"},
	}
	for _, c := range cases {
		if err := s.Set(c); err == nil {
			t.Errorf("expected error for %+v, got nil", c)
		}
	}

	// State must be unchanged after rejected writes.
	if got := s.Get(); got != (Preferences{}) {
		t.Errorf("state changed after invalid Set: %+v", got)
	}
}

func TestSetAcceptsEmptyAsUnset(t *testing.T) {
	s := newTestStore(t)
	if err := s.Set(Preferences{PanelClose: "", WorkspaceClose: ""}); err != nil {
		t.Errorf("empty values should be valid, got error: %v", err)
	}
}

func TestSaveWritesValidJSON(t *testing.T) {
	s := newTestStore(t)
	want := Preferences{PanelClose: "detach", WorkspaceClose: "kill"}
	if err := s.Set(want); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}

	var got Preferences
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
	if got != want {
		t.Errorf("persisted JSON: got %+v, want %+v", got, want)
	}

	// No leftover temp file after a successful write.
	if _, err := os.Stat(s.path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file should not remain after save, stat err: %v", err)
	}
}
