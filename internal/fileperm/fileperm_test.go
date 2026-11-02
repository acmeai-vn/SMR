package fileperm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestrictKeepsFileReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restrict(path); err != nil {
		t.Fatalf("Restrict() = %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after Restrict(): %v", err)
	}
	if string(body) != "test" {
		t.Fatalf("body = %q", body)
	}
}
