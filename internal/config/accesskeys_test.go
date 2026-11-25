package config

import (
	"strings"
	"testing"
)

const validKey = "smr_lan_0123456789abcdefghijklmnopqrstuv"

var testKey = AccessKey{Name: "local", Key: validKey, CreatedAt: "2026-11-25T00:00:00Z"}

func TestSchemaAllowsPublicBindButRuntimeNeedsAKey(t *testing.T) {
	cfg := Default()
	cfg.Listen = "0.0.0.0:8787"
	if err := cfg.ValidateSchema(); err != nil {
		t.Fatalf("schema must allow a public bind so the file can be repaired: %v", err)
	}
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "access key") {
		t.Fatalf("runtime error = %v, want an access-key instruction", err)
	}
	cfg.AccessKeys = []AccessKey{testKey}
	if err := cfg.ValidateRuntime(); err != nil {
		t.Fatalf("runtime with one key: %v", err)
	}
}

func TestRuntimeKeepsTheUITOnLoopback(t *testing.T) {
	cfg := Default()
	cfg.UIListen = "0.0.0.0:8788"
	cfg.AccessKeys = []AccessKey{testKey}
	if err := cfg.ValidateSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("runtime error = %v, want a loopback error", err)
	}
}

func TestSchemaRejectsDuplicateAndMalformedKeys(t *testing.T) {
	cfg := Default()
	cfg.AccessKeys = []AccessKey{testKey, testKey}
	if err := cfg.ValidateSchema(); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate error = %v", err)
	}
	cfg.AccessKeys = []AccessKey{{Name: "phone", Key: "not-a-key"}}
	if err := cfg.ValidateSchema(); err == nil || !strings.Contains(err.Error(), "smr_lan_") {
		t.Fatalf("format error = %v", err)
	}
}

func TestAddAndRemoveAccessKey(t *testing.T) {
	cfg := Default()
	if _, ok := cfg.LocalAccessKey(); ok {
		t.Fatal("default config must not have a key")
	}
	generated, err := GenerateAccessKey()
	if err != nil {
		t.Fatal(err)
	}
	generated.Name = "local"
	if err := cfg.AddAccessKey(generated); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddAccessKey(generated); err == nil {
		t.Fatal("duplicate name was accepted")
	}
	local, ok := cfg.LocalAccessKey()
	if !ok || local.Name != "local" || !strings.HasPrefix(local.Key, AccessKeyPrefix) || len(local.Key) != 40 {
		t.Fatalf("local key = %#v", local)
	}
	if !cfg.RemoveAccessKey("local") || len(cfg.AccessKeys) != 0 {
		t.Fatal("key was not removed")
	}
	if cfg.RemoveAccessKey("missing") {
		t.Fatal("removing a missing key returned true")
	}
}

func TestCloneDoesNotShareTheAccessKeySlice(t *testing.T) {
	cfg := Default()
	cfg.AccessKeys = []AccessKey{testKey}
	copied := clone(cfg)
	copied.AccessKeys[0].Name = "changed"
	if cfg.AccessKeys[0].Name != "local" {
		t.Fatal("clone shares the access key backing array")
	}
	copied.RemoveAccessKey("local")
	if len(cfg.AccessKeys) != 1 {
		t.Fatal("removing a key from the clone changed the original")
	}
}

func TestManagerSnapshotIsIndependent(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	cfg := Default()
	cfg.AccessKeys = []AccessKey{testKey}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	manager, err := LoadManager(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot()
	snapshot.RemoveAccessKey("local")
	if len(manager.Snapshot().AccessKeys) != 1 {
		t.Fatal("Snapshot exposed the live access key slice")
	}
}
