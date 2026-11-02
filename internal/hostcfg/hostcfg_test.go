package hostcfg

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCodexPreviewApplyRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "model = \"gpt-5\"\n\n[model_providers.other]\nbase_url = \"https://other.test/v1\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	change, err := Preview(Codex, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(change.After, "[model_providers.smr]") || !strings.Contains(change.Diff, "+ [model_providers.smr]") {
		t.Fatalf("unexpected change: %s", change.Diff)
	}
	if err := Apply(change, dataDir); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "[model_providers.smr]") {
		t.Fatal("provider block not written")
	}
	if _, err := RestoreLatest(Codex, dataDir); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != original {
		t.Fatalf("restore mismatch:\n%s", restored)
	}
}

func TestCodexUpdatesManagedBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := codexMarkerStart + "\n[model_providers.smr]\nbase_url = \"old\"\n" + codexMarkerEnd + "\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	change, err := Preview(Codex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(change.After, codexMarkerStart) != 1 || strings.Contains(change.After, `base_url = "old"`) {
		t.Fatalf("managed block not replaced:\n%s", change.After)
	}
}

func TestPreviewForUsesConfiguredListener(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	change, err := PreviewFor(Codex, t.TempDir(), "127.0.0.1:18787")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(change.After, `base_url = "http://127.0.0.1:18787/v1"`) {
		t.Fatalf("custom listener missing from proposed config:\n%s", change.After)
	}
}

func TestClaudePreviewPreservesOtherSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := `{"theme":"dark","env":{"KEEP":"yes","ANTHROPIC_BASE_URL":"https://old.test"}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	change, err := Preview(Claude, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(change.After, `"KEEP": "yes"`) || !strings.Contains(change.After, `"theme": "dark"`) {
		t.Fatalf("unrelated settings lost:\n%s", change.After)
	}
	if !strings.Contains(change.After, `"ANTHROPIC_BASE_URL": "http://127.0.0.1:8787"`) {
		t.Fatalf("base URL not updated:\n%s", change.After)
	}
}

func TestClaudeInvalidJSONIsNotOverwritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(Claude, t.TempDir()); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestApplyRejectsStalePreview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model = \"one\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	change, err := Preview(Codex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model = \"two\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(change, t.TempDir()); err == nil || !strings.Contains(err.Error(), "changed after preview") {
		t.Fatalf("expected stale error, got %v", err)
	}
}

func TestRestoreDeletesFileThatDidNotExist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	dataDir := t.TempDir()
	change, err := Preview(Codex, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(change, dataDir); err != nil {
		t.Fatal(err)
	}
	path, _ := CodexPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreLatest(Codex, dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed, got %v", err)
	}
}

func TestDiffShowsChanges(t *testing.T) {
	diff := LineDiff("a\nb\n", "a\nc\n")
	if !strings.Contains(diff, "- b") || !strings.Contains(diff, "+ c") {
		t.Fatalf("bad diff:\n%s", diff)
	}
}

func TestLineKind(t *testing.T) {
	if runtime.GOOS == "" {
		t.Fatal("unreachable")
	}
	if lineKind("a", "a") != "same" || lineKind("", "b") != "new" || lineKind("a", "b") != "update" {
		t.Fatal("unexpected kind")
	}
}
