package hostcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexAt(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// SMR documents host changes instead of writing them, so the snippets must be
// complete and must not depend on the file already existing.
func TestCodexInstructionsCoverBothEdits(t *testing.T) {
	codexAt(t)
	instructions, err := InstructionsFor(Codex, "127.0.0.1:18787")
	if err != nil {
		t.Fatal(err)
	}
	if len(instructions.Parts) != 2 {
		t.Fatalf("parts=%d, want the default key and the provider block", len(instructions.Parts))
	}
	if instructions.Parts[0].Code != `model_provider = "smr"` {
		t.Fatalf("default key snippet=%q", instructions.Parts[0].Code)
	}
	block := instructions.Parts[1].Code
	if !strings.Contains(block, "[model_providers.smr]") ||
		!strings.Contains(block, `base_url = "http://127.0.0.1:18787/v1"`) ||
		!strings.Contains(block, `wire_api = "responses"`) {
		t.Fatalf("provider block is incomplete:\n%s", block)
	}
}

// The Codex top-level key and the table must stay separate snippets: pasting
// model_provider after a [table] silently changes its meaning.
func TestCodexDefaultKeyIsNotPartOfTheTable(t *testing.T) {
	codexAt(t)
	instructions, err := InstructionsFor(Codex, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(instructions.Parts[1].Code, "model_provider =") {
		t.Fatal("the provider block must not include the top-level key")
	}
}

func TestClaudeInstructionsUseEnvFragment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	instructions, err := InstructionsFor(Claude, "")
	if err != nil {
		t.Fatal(err)
	}
	if !instructions.Exists {
		t.Fatal("existing settings.json was not detected")
	}
	if len(instructions.Parts) != 1 {
		t.Fatalf("parts=%d, want one env fragment", len(instructions.Parts))
	}
	code := instructions.Parts[0].Code
	if !strings.Contains(code, `"ANTHROPIC_BASE_URL": "http://127.0.0.1:8787"`) ||
		!strings.Contains(code, `"ANTHROPIC_AUTH_TOKEN": "smr-local-placeholder"`) {
		t.Fatalf("env fragment=%s", code)
	}
	// A whole {"env": {...}} document would replace unrelated settings when
	// pasted over an existing file.
	if strings.Contains(code, `"env"`) || strings.HasPrefix(strings.TrimSpace(code), "{") {
		t.Fatalf("fragment must be the two entries only:\n%s", code)
	}
}

func TestInstructionsDoNotWriteHostFiles(t *testing.T) {
	path := codexAt(t)
	original := "model = \"gpt-5\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstructionsFor(Codex, ""); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatalf("host file was modified:\n%s", after)
	}
}

func TestInstructionsForUnknownHost(t *testing.T) {
	if _, err := InstructionsFor("nope", ""); err == nil {
		t.Fatal("expected an unknown-host error")
	}
}

func TestStripCommentAndUnquote(t *testing.T) {
	if got := stripComment(`base_url = "https://x/#frag" # note`); got != `base_url = "https://x/#frag" ` {
		t.Fatalf("stripComment=%q", got)
	}
	if got := unquote(` "value" `); got != "value" {
		t.Fatalf("unquote=%q", got)
	}
}
