package config

import (
	"os"
	"path/filepath"
	"testing"
)

// defaultTestConfig returns a Config initialized with the same layer-0 defaults
// as Load(), so the applyFileConfigFromPath logic is genuinely exercised.
func defaultTestConfig() *Config {
	return &Config{
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
}

// writeTempConfig writes content to a temporary TOML file and returns its path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeTempConfig: %v", err)
	}
	return path
}

// TestZpitDefaults_NoSection verifies that when no [zpit] section exists in the
// TOML file, the defaults Enabled==true and Bin=="zpit" are preserved.
func TestZpitDefaults_NoSection(t *testing.T) {
	path := writeTempConfig(t, `
[daemon]
port = 17732
`)
	cfg := defaultTestConfig()
	if err := applyFileConfigFromPath(cfg, path); err != nil {
		t.Fatalf("applyFileConfigFromPath: %v", err)
	}

	if !cfg.Zpit.Enabled {
		t.Errorf("Zpit.Enabled: got false, want true (missing [zpit] section must preserve default)")
	}
	if cfg.Zpit.Bin != "zpit" {
		t.Errorf("Zpit.Bin: got %q, want %q", cfg.Zpit.Bin, "zpit")
	}
}

// TestZpitEnabledFalse verifies that an explicit "enabled = false" in [zpit]
// sets Enabled to false, even though the bool zero-value is also false.
func TestZpitEnabledFalse(t *testing.T) {
	path := writeTempConfig(t, `
[zpit]
enabled = false
`)
	cfg := defaultTestConfig()
	if err := applyFileConfigFromPath(cfg, path); err != nil {
		t.Fatalf("applyFileConfigFromPath: %v", err)
	}

	if cfg.Zpit.Enabled {
		t.Errorf("Zpit.Enabled: got true, want false (explicit enabled=false must override default)")
	}
}

// TestZpitCustomValues verifies that custom bin, config, and args are parsed
// verbatim from the [zpit] TOML section.
func TestZpitCustomValues(t *testing.T) {
	path := writeTempConfig(t, `
[zpit]
bin    = "/usr/local/bin/zpit"
config = "/home/user/.zpit/config.toml"
args   = ["--broker-port", "17731", "--verbose"]
`)
	cfg := defaultTestConfig()
	if err := applyFileConfigFromPath(cfg, path); err != nil {
		t.Fatalf("applyFileConfigFromPath: %v", err)
	}

	// Enabled must still be true (not set in TOML — nil pointer → keep default).
	if !cfg.Zpit.Enabled {
		t.Errorf("Zpit.Enabled: got false, want true (key absent means keep default true)")
	}

	wantBin := "/usr/local/bin/zpit"
	if cfg.Zpit.Bin != wantBin {
		t.Errorf("Zpit.Bin: got %q, want %q", cfg.Zpit.Bin, wantBin)
	}

	wantConfig := "/home/user/.zpit/config.toml"
	if cfg.Zpit.Config != wantConfig {
		t.Errorf("Zpit.Config: got %q, want %q", cfg.Zpit.Config, wantConfig)
	}

	wantArgs := []string{"--broker-port", "17731", "--verbose"}
	if len(cfg.Zpit.Args) != len(wantArgs) {
		t.Fatalf("Zpit.Args length: got %d, want %d", len(cfg.Zpit.Args), len(wantArgs))
	}
	for i, want := range wantArgs {
		if cfg.Zpit.Args[i] != want {
			t.Errorf("Zpit.Args[%d]: got %q, want %q", i, cfg.Zpit.Args[i], want)
		}
	}
}
