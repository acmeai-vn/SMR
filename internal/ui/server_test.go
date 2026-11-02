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
		// A substring match used to send these to the testing dashboard.
		"https://api.contest.example": "https://llm.acmeai.vn",
		"https://api.latest.example":  "https://llm.acmeai.vn",
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

// Enter in any step 2 field submits the first submit button in tree order.
// That must be the hidden save button, not the step 1 connection test.
func TestEnterInFormSavesInsteadOfTesting(t *testing.T) {
	_, _, httpServer, client, _ := testUI(t)
	authenticate(t, client, httpServer.URL)
	body := getBody(t, client, httpServer.URL+"/")

	save := strings.Index(body, `class="default-submit"`)
	test := strings.Index(body, `formaction="/test"`)
	if save < 0 || test < 0 {
		t.Fatalf("missing submit buttons (save=%d test=%d)", save, test)
	}
	if save > test {
		t.Fatal("the connection-test button comes first; Enter would run /test instead of saving")
	}
}

// Step 3 documents the edit instead of writing host files. A duplicate
// model_provider or ANTHROPIC_API_KEY breaks the agent in ways SMR cannot see.
func TestStepThreeShowsInstructionsAndNeverWritesHostFiles(t *testing.T) {
	server, _, httpServer, client, _ := testUI(t)
	authenticate(t, client, httpServer.URL)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	path := filepath.Join(codexHome, "config.toml")
	original := "model = \"gpt-5\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	body := getBody(t, client, httpServer.URL+"/")
	for _, want := range []string{
		"SMR không ghi vào file cấu hình",
		`model_provider = &#34;smr&#34;`,
		`[model_providers.smr]`,
		`base_url = &#34;http://127.0.0.1:8787/v1&#34;`,
		`data-copy=`,
		"/static/copy.js",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("step 3 is missing %q:\n%s", want, body)
		}
	}

	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Fatalf("setup page changed the host file:\n%s", after)
	}

	for _, route := range []string{"/hosts/preview", "/hosts/apply"} {
		resp, err := client.PostForm(httpServer.URL+route, url.Values{"csrf": {server.csrf}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s still answers with %d", route, resp.StatusCode)
		}
	}
}

// The /test form posts the URL the user typed. It used to save that URL only
// when the key was new, so a successful check against testing snapped back to
// production and the next save wrote the old URL again.
func TestTestConnectionPersistsNewBaseURL(t *testing.T) {
	classifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"balance_vnd":25000,"max_state_chars":8000,"cache_ttl_seconds":600}`)
	}))
	defer classifier.Close()

	server, manager, httpServer, client, configPath := testUI(t)
	authenticate(t, client, httpServer.URL)
	cfg := manager.Snapshot()
	cfg.Classifier.APIKey = "acme_sk_saved"
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}

	resp, err := client.PostForm(httpServer.URL+"/test", url.Values{
		"csrf":                {server.csrf},
		"classifier_base_url": {classifier.URL},
		"max_chars":           {"9000"},
		"fallback_tier":       {"hard"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	cfg = manager.Snapshot()
	if cfg.Classifier.BaseURL != classifier.URL {
		t.Fatalf("tested base URL was dropped: %q", cfg.Classifier.BaseURL)
	}
	if cfg.Classifier.MaxChars != 9000 || cfg.Classifier.FallbackTier != "hard" {
		t.Fatalf("tested settings were dropped: %#v", cfg.Classifier)
	}
	persisted, _ := os.ReadFile(configPath)
	if !strings.Contains(string(persisted), classifier.URL) {
		t.Fatalf("base URL not written to the config file:\n%s", persisted)
	}
}

func TestTestConnectionEchoesSubmittedValuesOnFailure(t *testing.T) {
	classifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer classifier.Close()

	server, _, httpServer, client, _ := testUI(t)
	authenticate(t, client, httpServer.URL)
	resp, err := client.PostForm(httpServer.URL+"/test", url.Values{
		"csrf":                {server.csrf},
		"classifier_api_key":  {"acme_sk_wrong"},
		"classifier_base_url": {classifier.URL},
		"max_chars":           {"9000"},
		"fallback_tier":       {"hard"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	for _, want := range []string{classifier.URL, `value="9000"`, `value="hard" selected`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("failed check lost the submitted value %q:\n%s", want, body)
		}
	}
}

// The page is built from a snapshot taken before the save, so a successful
// check said "saved" while still showing "no key" and the previous
// environment's dashboard link. The render must happen after Update.
func TestTestConnectionPageReflectsTheSavedKey(t *testing.T) {
	classifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"balance_vnd":42000,"max_state_chars":12000,"cache_ttl_seconds":300}`)
	}))
	defer classifier.Close()

	server, manager, httpServer, client, _ := testUI(t)
	authenticate(t, client, httpServer.URL)
	// Start on testing so the dashboard link has to move with the new URL.
	cfg := manager.Snapshot()
	cfg.Classifier.BaseURL = "https://api-testing.acmeai.vn"
	cfg.Classifier.APIKey = ""
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}

	resp, err := client.PostForm(httpServer.URL+"/test", url.Values{
		"csrf":                {server.csrf},
		"classifier_base_url": {classifier.URL},
		"classifier_api_key":  {"acme_sk_new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Đã có key") {
		t.Fatalf("saved key still renders as missing:\n%s", body)
	}
	if strings.Contains(body, "Chưa có key? Tạo tài khoản") {
		t.Fatalf("onboarding callout still shows after the key was saved:\n%s", body)
	}
	// The local test server maps to the production dashboard, so the link must
	// no longer point at testing even though that was the saved environment.
	if !strings.Contains(body, "https://llm.acmeai.vn") {
		t.Fatalf("dashboard link did not follow the tested base URL:\n%s", body)
	}
	if strings.Contains(body, "https://llm-testing.acmeai.vn") {
		t.Fatalf("page still shows the previous environment's dashboard:\n%s", body)
	}
}

// Example: ACMEAI_BASE_URL is set. The file must still keep what the user
// typed, the daemon must keep using the variable, and the page must say so.
func TestEnvOverrideKeepsFileAndSaysSo(t *testing.T) {
	_, manager, httpServer, client, configPath := testUI(t)
	authenticate(t, client, httpServer.URL)
	t.Setenv("ACMEAI_BASE_URL", "https://api-testing.acmeai.vn")

	cfg := manager.Snapshot()
	cfg.Classifier.BaseURL = "https://api.acmeai.vn"
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().Classifier.BaseURL; got != "https://api-testing.acmeai.vn" {
		t.Fatalf("runtime must keep the environment value, got %q", got)
	}
	persisted, _ := os.ReadFile(configPath)
	if !strings.Contains(string(persisted), "https://api.acmeai.vn") {
		t.Fatalf("file must keep the saved value:\n%s", persisted)
	}

	body := getBody(t, client, httpServer.URL+"/?saved=1")
	if !strings.Contains(body, "biến môi trường") {
		t.Fatalf("page does not explain the environment override:\n%s", body)
	}
	if strings.Contains(body, "Daemon dùng cấu hình mới cho request tiếp theo.") {
		t.Fatal("page claims the saved value is live while an env var overrides it")
	}
}

// A fifth profile used to render a clickable tab with no panel ever shown.
func TestTabsCSSFollowsProfileCount(t *testing.T) {
	_, manager, httpServer, client, _ := testUI(t)
	authenticate(t, client, httpServer.URL)
	cfg := manager.Snapshot()
	for _, name := range []string{"extra1", "extra2", "extra3"} {
		cfg.Profiles[name] = config.Profile{API: "responses", Auth: "bearer", Tiers: cfg.Profiles["codex"].Tiers}
	}
	if err := manager.Update(cfg); err != nil {
		t.Fatal(err)
	}

	resp, err := client.Get(httpServer.URL + "/static/tabs.css")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tabs.css status=%d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "nth-of-type(5):checked ~ .tab-panels > .tab-panel:nth-child(5)") {
		t.Fatalf("tabs.css does not cover the fifth profile:\n%s", body)
	}
}

// PowerShell needs the call operator before a quoted path; `"C:\...\smr.exe"
// setup` fails with "Unexpected token 'setup'".
func TestSetupCommandQuotesPathsPerShell(t *testing.T) {
	windowsPath := `C:\Users\me\AppData\Local\AcmeAI\SMR\smr.exe`
	if got := setupCommandFor("windows", windowsPath, false); got != `& "C:\Users\me\AppData\Local\AcmeAI\SMR\smr.exe" setup` {
		t.Fatalf("windows command=%q", got)
	}
	if got := setupCommandFor("linux", "/home/me/.local/bin/smr", false); got != `"/home/me/.local/bin/smr" setup` {
		t.Fatalf("posix command=%q", got)
	}
	if got := setupCommandFor("windows", windowsPath, true); got != "smr setup" {
		t.Fatalf("PATH command=%q", got)
	}
	if got := setupCommandFor("windows", "", false); got != "smr setup" {
		t.Fatalf("fallback command=%q", got)
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
