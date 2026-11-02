package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acmeai-vn/SMR/internal/config"
	"github.com/acmeai-vn/SMR/internal/service"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func tokenDigest(t *testing.T, dataDir string) [32]byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dataDir, "ui-token"))
	if err != nil {
		t.Fatalf("read ui-token: %v", err)
	}
	return sha256.Sum256(body)
}

// A second start - `smr update`, or `smr setup` racing a slow boot - must fail
// before it rewrites ui-token. Otherwise the running daemon rejects the token
// the page just printed and every visit returns 401 until a restart.
func TestRunServeSecondInstanceKeepsToken(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.yaml")
	dataDir := filepath.Join(home, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SMR_CONFIG", configPath)
	t.Setenv("SMR_DATA_DIR", dataDir)

	cfg := config.Default()
	cfg.Listen = freeAddr(t)
	cfg.UIListen = freeAddr(t)
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, []string{"serve"}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("first daemon did not stop")
		}
	})

	if err := waitForHealth(context.Background(), "http://"+cfg.Listen, 10*time.Second); err != nil {
		t.Fatalf("daemon never became healthy: %v", err)
	}
	first := tokenDigest(t, dataDir)

	if err := runServe(context.Background(), []string{"serve"}); !errors.Is(err, service.ErrAlreadyRunning) {
		t.Fatalf("second start error = %v, want ErrAlreadyRunning", err)
	}
	if second := tokenDigest(t, dataDir); second != first {
		t.Fatalf("second start replaced ui-token: %x -> %x", first, second)
	}
	if _, running := readDaemonPID(t, dataDir); !running {
		t.Fatal("first daemon lost its lock")
	}
}

func readDaemonPID(t *testing.T, dataDir string) (string, bool) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dataDir, "daemon.pid"))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// The token is written only after both listeners bind, so a failed start never
// invalidates the token of the daemon that is actually serving.
func TestRunServeWritesNoTokenWhenAPortIsBusy(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.yaml")
	dataDir := filepath.Join(home, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SMR_CONFIG", configPath)
	t.Setenv("SMR_DATA_DIR", dataDir)

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	cfg := config.Default()
	cfg.Listen = freeAddr(t)
	cfg.UIListen = blocker.Addr().String()
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if err := runServe(context.Background(), []string{"serve"}); err == nil {
		t.Fatal("expected a bind error")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ui-token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ui-token was written before the ports bound: %v", err)
	}
}
