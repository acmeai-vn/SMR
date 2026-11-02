package ui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acmeai-vn/SMR/internal/config"
	"github.com/acmeai-vn/SMR/internal/hostcfg"
	"github.com/acmeai-vn/SMR/internal/proxy"
)

func testUI(t *testing.T) (*Server, *config.Manager, *httptest.Server, *http.Client, string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	manager, err := config.LoadManager(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := manager.Snapshot()
	cfg.Profiles["codex"].Tiers["easy"] = config.Tier{BaseURL: "https://provider.test/v1", Model: "small", APIKey: "secret"}
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}
	server, err := New(manager, func() proxy.Stats { return proxy.Stats{Requests: 2} }, t.TempDir(), "setup-token", "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	return server, manager, httpServer, client, configPath
}

func TestRequiresTokenAndSetsCookie(t *testing.T) {
	_, _, server, client, _ := testUI(t)
	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	resp, err = client.Get(server.URL + "/?token=wrong")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d", resp.StatusCode)
	}
	resp, err = client.Get(server.URL + "/?token=setup-token")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Profile claude") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestSaveConfigPreservesBlankKeys(t *testing.T) {
	server, manager, httpServer, client, configPath := testUI(t)
	authenticate(t, client, httpServer.URL)
	form := url.Values{
		"csrf":                {server.csrf},
		"classifier_base_url": {"https://api.test"},
		"max_chars":           {"9000"},
		"fallback_tier":       {"hard"},
		"codex_easy_base_url": {"https://provider.test/v1"},
		"codex_easy_model":    {"small-v2"},
		"codex_easy_api_key":  {""},
	}
	resp, err := client.PostForm(httpServer.URL+"/save", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	cfg := manager.Snapshot()
	if cfg.Classifier.BaseURL != "https://api.test" || cfg.Classifier.FallbackTier != "hard" {
		t.Fatalf("classifier not saved: %#v", cfg.Classifier)
	}
	if cfg.Profiles["codex"].Tiers["easy"].Model != "small-v2" {
		t.Fatalf("tier not saved: %#v", cfg.Profiles["codex"].Tiers["easy"])
	}
	if cfg.Profiles["codex"].Tiers["easy"].APIKey != "secret" {
		t.Fatal("blank key field must preserve the stored key")
	}
	body, _ := os.ReadFile(configPath)
	if !strings.Contains(string(body), "small-v2") {
		t.Fatal("config was not written")
	}
}

func TestSaveRejectsMissingCSRF(t *testing.T) {
	_, _, httpServer, client, _ := testUI(t)
	authenticate(t, client, httpServer.URL)
	resp, err := client.PostForm(httpServer.URL+"/save", url.Values{"classifier_base_url": {"https://api.test"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestPreviewAndApplyHostDiff(t *testing.T) {
	server, _, httpServer, client, _ := testUI(t)
	authenticate(t, client, httpServer.URL)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	path := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(path, []byte("model = \"gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := client.PostForm(httpServer.URL+"/hosts/preview", url.Values{
		"csrf": {server.csrf},
		"host": {hostcfg.Codex},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Diff cho codex") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	change, err := hostcfg.Preview(hostcfg.Codex, server.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.PostForm(httpServer.URL+"/hosts/apply", url.Values{
		"csrf":     {server.csrf},
		"host":     {hostcfg.Codex},
		"expected": {change.ExpectedHash},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status=%d", resp.StatusCode)
	}
	updated, _ := os.ReadFile(path)
	if !strings.Contains(string(updated), "[model_providers.smr]") {
		t.Fatalf("host config not updated:\n%s", updated)
	}
}

func TestLogsPage(t *testing.T) {
	_, _, httpServer, client, _ := testUI(t)
	authenticate(t, client, httpServer.URL)
	resp, err := client.Get(httpServer.URL + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Log gần đây") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func authenticate(t *testing.T, client *http.Client, serverURL string) {
	t.Helper()
	resp, err := client.Get(serverURL + "/?token=setup-token")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticate status=%d", resp.StatusCode)
	}
}
