package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/smr-release.json" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(Manifest{
			Schema:  1,
			Version: "v0.1.0",
			Assets: map[string]Asset{
				"linux-amd64": {File: "smr.tar.gz", SHA256: "abc"},
			},
		})
	}))
	defer server.Close()
	t.Setenv("SMR_UPDATE_BASE_URL", server.URL)
	manifest, err := Fetch(context.Background(), "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "v0.1.0" || manifest.Assets["linux-amd64"].File != "smr.tar.gz" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
}

func TestFetchRejectsWrongVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":1,"version":"v0.1.0","assets":{}}`))
	}))
	defer server.Close()
	t.Setenv("SMR_UPDATE_BASE_URL", server.URL)
	if _, err := Fetch(context.Background(), "v0.2.0"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected version mismatch, got %v", err)
	}
}

func TestExtractTarBinary(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "release.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho smr\n")
	if err := tw.WriteHeader(&tar.Header{Name: "smr", Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	file.Close()
	extracted, err := extractBinary(archive, dir, "linux")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(extracted)
	if !bytes.Equal(got, body) {
		t.Fatalf("content=%q", got)
	}
}

func TestExtractZipBinary(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "release.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	entry, err := zw.Create("smr.exe")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("MZ test binary")
	if _, err := entry.Write(body); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	file.Close()
	extracted, err := extractBinary(archive, dir, "windows")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(extracted)
	if !bytes.Equal(got, body) {
		t.Fatalf("content=%q", got)
	}
}

func TestFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if sum != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("sha=%s", sum)
	}
}
