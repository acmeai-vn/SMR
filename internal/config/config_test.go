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
