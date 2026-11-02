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
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="tab-claude"`) || !strings.Contains(string(body), `id="step-3"`) {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestProfileTabsAndStepNavigation(t *testing.T) {
	_, _, server, client, _ := testUI(t)
	authenticate(t, client, server.URL)

	body := getBody(t, client, server.URL+"/")
	if !strings.Contains(body, `id="tab-codex"`) || !strings.Contains(body, `id="tab-claude"`) {
		t.Fatalf("missing profile tabs:\n%s", body)
	}
	if strings.Count(body, " checked>") != 1 || !strings.Contains(body, `id="tab-codex" value="codex" checked>`) {
		t.Fatalf("expected exactly one checked tab, codex first:\n%s", body)
	}
	for _, step := range []string{"step-1", "step-2", "step-3", "step-4"} {
		if !strings.Contains(body, `id="`+step+`"`) {
			t.Fatalf("missing %s", step)
		}
	}

	body = getBody(t, client, server.URL+"/?tab=claude")
	if !strings.Contains(body, `id="tab-claude" value="claude" checked>`) {
		t.Fatalf("claude tab not selected:\n%s", body)
	}
	if strings.Contains(body, `id="tab-codex" value="codex" checked>`) {
		t.Fatalf("codex tab should no longer be selected:\n%s", body)
	}
}

func getBody(t *testing.T, client *http.Client, target string) string {
	t.Helper()
	resp, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	return string(body)
}

func TestDashboardLinkFollowsEnvironment(t *testing.T) {
	cases := map[string]string{
		"https://api-testing.acmeai.vn": "https://llm-testing.acmeai.vn",
		"https://api.acmeai.vn":         "https://llm.acmeai.vn",
		"":                              "https://llm.acmeai.vn",
		"https://api.example.com":       "https://llm.acmeai.vn",
	}
	for baseURL, want := range cases {
		if got, _ := dashboardFor(baseURL); got != want {
			t.Fatalf("dashboardFor(%q)=%q, want %q", baseURL, got, want)
		}
	}
	if _, label := dashboardFor("https://api-testing.acmeai.vn"); label != "Testing" {
		t.Fatalf("testing label=%q", label)
	}
	if _, label := dashboardFor("https://api.acmeai.vn"); label != "" {
		t.Fatalf("production must carry no environment label, got %q", label)
	}
}

func TestPageLinksToDashboardWithoutKey(t *testing.T) {
	_, _, server, client, _ := testUI(t)
	authenticate(t, client, server.URL)

	body := getBody(t, client, server.URL+"/")
	if !strings.Contains(body, `href="https://llm.acmeai.vn"`) {
		t.Fatalf("missing dashboard link:\n%s", body)
	}
	if !strings.Contains(body, `target="_blank" rel="noopener noreferrer"`) {
		t.Fatalf("dashboard link must open in a new tab")
	}
	if !strings.Contains(body, "Nạp tiền") || !strings.Contains(body, "Tạo API key") {
		t.Fatalf("signup steps missing")
	}
	if strings.Contains(body, "pill-env") {
		t.Fatalf("production page must not show an environment badge:\n%s", body)
	}
}

func TestLockedPageExplainsHowToGetToken(t *testing.T) {
	_, _, server, _, _ := testUI(t)

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if strings.Contains(body, "Setup token required") {
		t.Fatalf("bare error text still rendered:\n%s", body)
	}
	for _, want := range []string{"Cần mã truy cập", "setup", `data-copy=`, "/static/copy.js"} {
		if !strings.Contains(body, want) {
			t.Fatalf("locked page missing %q:\n%s", want, body)
		}
	}
	// The code block and the copy button must both carry the command.
	if count := strings.Count(body, "setup"); count < 2 {
		t.Fatalf("command appears %d time(s), want the code block and the copy button:\n%s", count, body)
	}
	if strings.Contains(body, "Mã truy cập không đúng") {
		t.Fatalf("missing-token page must not claim the token is invalid")
	}
}

func TestLockedPageFollowsLanguage(t *testing.T) {
	_, _, server, _, _ := testUI(t)

	resp, err := http.Get(server.URL + "/?lang=en")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, `<html lang="en">`) || !strings.Contains(body, "Access token required") {
		t.Fatalf("English locked page missing:\n%s", body)
	}
	if strings.Contains(body, "Cần mã truy cập") {
		t.Fatalf("Vietnamese copy leaked into the English page")
	}
}

func TestInvalidTokenExplainsExpiry(t *testing.T) {
	_, _, server, _, _ := testUI(t)

	resp, err := http.Get(server.URL + "/?token=wrong")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if !strings.Contains(body, "Mã truy cập không đúng hoặc đã hết hiệu lực") {
		t.Fatalf("invalid-token hint missing:\n%s", body)
	}
}

func TestCopyScriptIsServedUnderCSP(t *testing.T) {
	_, _, server, _, _ := testUI(t)

	resp, err := http.Get(server.URL + "/static/copy.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("copy.js status=%d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP blocks the copy script: %q", csp)
	}
}

func TestTestingPageLabelsDashboard(t *testing.T) {
	_, manager, server, client, _ := testUI(t)
	authenticate(t, client, server.URL)

	cfg := manager.Snapshot()
	cfg.Classifier.BaseURL = "https://api-testing.acmeai.vn"
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}

	body := getBody(t, client, server.URL+"/")
	if !strings.Contains(body, `href="https://llm-testing.acmeai.vn"`) {
		t.Fatalf("missing testing dashboard link:\n%s", body)
	}
	if !strings.Contains(body, `class="pill-env">Testing</span>`) {
		t.Fatalf("missing Testing badge:\n%s", body)
	}
}

func TestSavedKeyDashboardLinkOmitsProductionLabel(t *testing.T) {
	_, manager, server, client, _ := testUI(t)
	authenticate(t, client, server.URL)

	cfg := manager.Snapshot()
	cfg.Classifier.APIKey = "test-key"
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}

	body := getBody(t, client, server.URL+"/")
	if !strings.Contains(body, `>Mở dashboard ↗</a>`) {
		t.Fatalf("production link should read like the button:\n%s", body)
	}
	if strings.Contains(body, "Production dashboard") {
		t.Fatalf("production link must not mention Production:\n%s", body)
	}

	cfg = manager.Snapshot()
	cfg.Classifier.BaseURL = "https://api-testing.acmeai.vn"
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}
	body = getBody(t, client, server.URL+"/")
	if !strings.Contains(body, `>Testing dashboard ↗</a>`) {
		t.Fatalf("testing link should keep the environment label:\n%s", body)
	}
}

func TestDefaultLanguageIsVietnamese(t *testing.T) {
	_, _, server, client, _ := testUI(t)
	authenticate(t, client, server.URL)

	body := getBody(t, client, server.URL+"/")
	if !strings.Contains(body, `<html lang="vi">`) {
		t.Fatalf("expected Vietnamese by default:\n%s", body)
	}
	if !strings.Contains(body, "Kết nối ACME AI") || !strings.Contains(body, ">VI</a>") {
		t.Fatalf("Vietnamese copy or language switcher missing")
	}
}

func TestLanguageSwitchIsRemembered(t *testing.T) {
	_, _, server, client, _ := testUI(t)
	authenticate(t, client, server.URL)

	body := getBody(t, client, server.URL+"/?lang=en")
	if !strings.Contains(body, `<html lang="en">`) || !strings.Contains(body, "Connect to ACME AI") {
		t.Fatalf("English page missing:\n%s", body)
	}
	if strings.Contains(body, "Kết nối ACME AI") {
		t.Fatalf("Vietnamese copy leaked into the English page")
	}

	// The choice is stored in a cookie, so a later URL without ?lang stays English.
	body = getBody(t, client, server.URL+"/")
	if !strings.Contains(body, `<html lang="en">`) || !strings.Contains(body, "Models per tier") {
		t.Fatalf("language cookie not remembered:\n%s", body)
	}

	body = getBody(t, client, server.URL+"/?lang=vi")
	if !strings.Contains(body, `<html lang="vi">`) {
		t.Fatalf("switching back to Vietnamese failed")
	}
}

func TestTokenRedirectKeepsLanguage(t *testing.T) {
	_, _, server, client, _ := testUI(t)
	resp, err := client.Get(server.URL + "/?token=setup-token&lang=en")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Request.URL.String(); !strings.Contains(got, "lang=en") {
		t.Fatalf("redirect dropped the language: %s", got)
	}
	body := getBody(t, client, server.URL+"/")
	if !strings.Contains(body, `<html lang="en">`) {
		t.Fatalf("language not remembered after token redirect:\n%s", body)
	}
}

func TestEnglishPageHasSameStructure(t *testing.T) {
	_, _, server, client, _ := testUI(t)
	authenticate(t, client, server.URL)

	body := getBody(t, client, server.URL+"/?lang=en")
	for _, want := range []string{`id="step-1"`, `id="step-2"`, `id="step-3"`, `id="step-4"`, `id="tab-codex"`, `id="tab-claude"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("English page is missing %s", want)
		}
	}
	if !strings.Contains(body, "Save configuration") || !strings.Contains(body, "Open dashboard") {
		t.Fatalf("English actions missing")
	}
}

func TestTranslateNoteCodes(t *testing.T) {
	if got := translateNote(langEN, "codex.env_key:OPENAI_API_KEY"); !strings.Contains(got, "OPENAI_API_KEY") {
		t.Fatalf("argument lost: %q", got)
	}
	if got := translateNote(langVI, "codex.no_config"); strings.HasPrefix(got, "note.") {
		t.Fatalf("note code not translated: %q", got)
	}
	if got := translateNote(langVI, "future.note:value"); got != "future.note:value" {
		t.Fatalf("unknown note should pass through, got %q", got)
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
