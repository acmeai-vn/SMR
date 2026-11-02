package logtail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smr.log")
	body := "one\ntwo\nthree\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Tail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != "two\nthree\n" {
		t.Fatalf("got %q", got)
	}
}

func TestTailMissingFile(t *testing.T) {
	if _, err := Tail(filepath.Join(t.TempDir(), "missing"), 10); err == nil || !strings.Contains(err.Error(), "no such file") && !strings.Contains(err.Error(), "cannot find") {
		t.Fatalf("unexpected error: %v", err)
	}
}
