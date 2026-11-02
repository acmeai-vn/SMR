package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsPublicBind(t *testing.T) {
	cfg := Default()
	cfg.Listen = "0.0.0.0:8787"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback error, got %v", err)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.Classifier.APIKey = "acme_sk_test"
	cfg.Profiles["codex"].Tiers["easy"] = Tier{BaseURL: "https://example.test/v1", APIKey: "provider", Model: "small"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions are too broad: %o", info.Mode().Perm())
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Profiles["codex"].Tiers["easy"].Model != "small" {
		t.Fatalf("model not round-tripped: %#v", loaded.Profiles["codex"].Tiers)
	}
}

func TestEnvOverridesAPIKey(t *testing.T) {
	t.Setenv("ACMEAI_API_KEY", "from_env")
	loaded, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Classifier.APIKey != "from_env" {
		t.Fatalf("got %q", loaded.Classifier.APIKey)
	}
}

func TestEnvOverridesClassifierBaseURL(t *testing.T) {
	t.Setenv("ACMEAI_BASE_URL", "https://api-testing.acmeai.vn/")
	loaded, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Classifier.BaseURL != "https://api-testing.acmeai.vn" {
		t.Fatalf("got %q", loaded.Classifier.BaseURL)
	}
}

func TestTestingExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.testing.example.yaml"))
	if err != nil {
		t.Fatalf("config.testing.example.yaml is invalid: %v", err)
	}
	if cfg.Classifier.BaseURL != "https://api-testing.acmeai.vn" {
		t.Fatalf("classifier base URL = %q", cfg.Classifier.BaseURL)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml is invalid: %v", err)
	}
	for _, profile := range []string{"codex", "claude"} {
		for _, tier := range Tiers {
			if cfg.Profiles[profile].Tiers[tier].Model == "" {
				t.Fatalf("config.example.yaml is missing %s/%s", profile, tier)
			}
		}
	}
}

// Saving unrelated settings must not bake an environment override into the file.
func TestUpdateDoesNotPersistEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	manager, err := LoadManager(path)
	if err != nil {
		t.Fatalf("LoadManager: %v", err)
	}
	t.Setenv("ACMEAI_API_KEY", "env-key")
	t.Setenv("ACMEAI_BASE_URL", "https://api-testing.acmeai.vn")

	cfg := manager.Snapshot()
	cfg.Classifier.TimeoutMS = 9000
	if err := manager.Update(cfg); err != nil {
		t.Fatalf("Update: %v", err)
	}

	saved, err := Load(path)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if saved.Classifier.APIKey != "env-key" {
		t.Fatalf("runtime API key = %q", saved.Classifier.APIKey)
	}
	if saved.Classifier.TimeoutMS != 9000 {
		t.Fatalf("saved timeout = %d", saved.Classifier.TimeoutMS)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if strings.Contains(string(raw), "env-key") {
		t.Fatal("environment API key was written to the config file")
	}
	if strings.Contains(string(raw), "api-testing.acmeai.vn") {
		t.Fatal("environment base URL was written to the config file")
	}
	if !strings.Contains(string(raw), "timeout_ms: 9000") {
		t.Fatalf("edited field missing from file:\n%s", raw)
	}
}

// The file keeps what it had when the environment supplies the live value, and
// keeps a value the user actually typed.
func TestUpdateKeepsFileValueBehindEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	seed := Default()
	seed.Classifier.BaseURL = "https://api-testing.acmeai.vn"
	if err := Save(path, seed); err != nil {
		t.Fatal(err)
	}
	manager, err := LoadManager(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACMEAI_BASE_URL", "https://api-testing.acmeai.vn")

	cfg := manager.Snapshot()
	cfg.Classifier.MaxChars = 12000
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "https://api-testing.acmeai.vn") {
		t.Fatalf("unrelated save rewrote the stored base URL:\n%s", raw)
	}

	cfg = manager.Snapshot()
	cfg.Classifier.BaseURL = "https://api.acmeai.vn"
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "https://api.acmeai.vn") {
		t.Fatalf("typed base URL was not saved:\n%s", raw)
	}
	if got := manager.Snapshot().Classifier.BaseURL; got != "https://api-testing.acmeai.vn" {
		t.Fatalf("runtime must keep the environment value, got %q", got)
	}
}
