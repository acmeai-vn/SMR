// Package ui serves the loopback-only setup page. The page can change only the
// local SMR config and host config, never ACME AI server state.
package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/acmeai-vn/SMR/internal/classifier"
	"github.com/acmeai-vn/SMR/internal/config"
	"github.com/acmeai-vn/SMR/internal/fileperm"
	"github.com/acmeai-vn/SMR/internal/hostcfg"
	"github.com/acmeai-vn/SMR/internal/logtail"
	"github.com/acmeai-vn/SMR/internal/proxy"
)

//go:embed templates/*.html static/*
var embedded embed.FS

const (
	tokenCookie = "smr_ui_token"
	tokenFile   = "ui-token"
	LogFile     = "smr.log"
)

type Server struct {
	manager *config.Manager
	stats   func() proxy.Stats
	dataDir string
	token   string
	csrf    string
	version string
	logger  *slog.Logger
	pages   map[string]*template.Template
	static  http.Handler
}

type profileView struct {
	Name       string
	Label      string
	Profile    config.Profile
	Tiers      []tierView
	Active     bool
	Configured int
	Total      int
}

type tierView struct {
	Name           string
	Label          string
	BaseURL        string
	Model          string
	KeyPlaceholder string
}

// hostView adds the presenter-only fields the setup page needs to a detected
// host configuration.
type hostView struct {
	hostcfg.Detection
	Label   string
	UsesSMR bool
	// Parts are the copy-ready blocks the user pastes into the host file. SMR
	// never writes that file: a duplicate key breaks the agent silently.
	Parts []hostcfg.Part
}

// stepView drives the step navigation at the top of the page.
type stepView struct {
	N     int
	Title string
	Done  bool
}

type testResult struct {
	OK              bool
	Error           string
	BalanceVND      int64
	MaxStateChars   int
	CacheTTLSeconds int64
	// Saved reports that the tested key was new and is now stored. Testing a
	// valid key is the moment the user expects it to be kept.
	Saved bool
	// EnvOverride reports that ACMEAI_API_KEY or ACMEAI_BASE_URL is winning at
	// runtime, so a successful save will not change what the daemon uses.
	EnvOverride bool
}

type pageData struct {
	Title             string
	Version           string
	CSRF              string
	Lang              string
	LangURLVI         string
	LangURLEN         string
	ConfigPath        string
	APIKeyPlaceholder string
	Config            config.Config
	Profiles          []profileView
	ActiveTab         string
	Steps             []stepView
	Verified          bool
	// DashboardURL is where a new user signs up, tops up and creates the
	// classifier key. EnvironmentLabel names it so the page never sends a
	// testing user to production or the other way round.
	DashboardURL     string
	EnvironmentLabel string
	Tiers            []string
	Stats            proxy.Stats
	Detections       []hostView
	// EnvKeyOverride and EnvBaseURLOverride make the page say that an
	// environment variable, not the saved file, supplies the live value.
	EnvKeyOverride     bool
	EnvBaseURLOverride bool
	Test               *testResult
	Error              string
	Notice             string
}

type logsData struct {
	Path      string
	Logs      string
	Lang      string
	LangURLVI string
	LangURLEN string
}

// lockedData renders the page shown when someone opens the setup UI without
// the token, for example by typing http://127.0.0.1:8788 into the address bar.
type lockedData struct {
	Lang      string
	LangURLVI string
	LangURLEN string
	Version   string
	TokenPath string
	UIHost    string
	Command   string
	Invalid   bool
}

func New(manager *config.Manager, stats func() proxy.Stats, dataDir, token, version string, logger *slog.Logger) (*Server, error) {
	csrf, err := GenerateToken()
	if err != nil {
		return nil, err
	}
	server := &Server{
		manager: manager,
		stats:   stats,
		dataDir: dataDir,
		token:   token,
		csrf:    csrf,
		version: version,
		logger:  logger,
	}
	functions := template.FuncMap{
		"lower":         strings.ToLower,
		"t":             tr,
		"tf":            trf,
		"tierLabel":     tierLabel,
		"translateNote": translateNote,
	}
	page, err := template.New("page.html").Funcs(functions).ParseFS(embedded, "templates/page.html")
	if err != nil {
		return nil, err
	}
	logs, err := template.New("logs.html").Funcs(functions).ParseFS(embedded, "templates/logs.html")
	if err != nil {
		return nil, err
	}
	locked, err := template.New("locked.html").Funcs(functions).ParseFS(embedded, "templates/locked.html")
	if err != nil {
		return nil, err
	}
	server.pages = map[string]*template.Template{"page": page, "logs": logs, "locked": locked}
	staticFS, err := fs.Sub(embedded, "static")
	if err != nil {
		return nil, err
	}
	server.static = http.FileServer(http.FS(staticFS))
	return server, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", s.static))
	mux.HandleFunc("GET /static/tabs.css", s.handleTabsCSS)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /", s.authorize(s.handleIndex))
	mux.HandleFunc("POST /save", s.authorize(s.handleSave))
	mux.HandleFunc("POST /test", s.authorize(s.handleTest))
	mux.HandleFunc("GET /logs", s.authorize(s.handleLogs))
	return securityHeaders(mux)
}

// handleTabsCSS emits one rule per configured profile. The number of profiles
// is not fixed, and the old four hardcoded rules left a fifth tab clickable
// with no panel ever shown.
func (s *Server) handleTabsCSS(w http.ResponseWriter, r *http.Request) {
	names := profileNames(s.manager.Snapshot().Profiles)
	var active, visible []string
	for index := 1; index <= len(names); index++ {
		active = append(active, fmt.Sprintf(".tabs > .tab-radio:nth-of-type(%d):checked ~ .tabbar > .tab:nth-child(%d)", index, index))
		visible = append(visible, fmt.Sprintf(".tabs > .tab-radio:nth-of-type(%d):checked ~ .tab-panels > .tab-panel:nth-child(%d)", index, index))
	}
	var css strings.Builder
	css.WriteString("/* Generated for the profiles in this config. */\n")
	if len(names) > 0 {
		css.WriteString(strings.Join(active, ",\n") + " {\n  background: var(--paper);\n  border-color: var(--line);\n  color: var(--ink);\n  box-shadow: inset 0 2px 0 var(--accent);\n}\n")
		css.WriteString(strings.Join(visible, ",\n") + " { display: block; }\n")
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, css.String())
}

func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if candidate := r.URL.Query().Get("token"); candidate != "" {
			if equal(candidate, s.token) {
				http.SetCookie(w, &http.Cookie{
					Name:     tokenCookie,
					Value:    s.token,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				// Keep the language the user asked for. Dropping it here made
				// ?token=...&lang=en land on a Vietnamese page.
				target := "/"
				if lang, ok := parseLanguage(r.URL.Query().Get("lang")); ok {
					target = "/?lang=" + lang
				}
				http.Redirect(w, r, target, http.StatusSeeOther)
				return
			}
			s.renderLocked(w, r, true)
			return
		}
		cookie, err := r.Cookie(tokenCookie)
		if err != nil || !equal(cookie.Value, s.token) {
			s.renderLocked(w, r, false)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	lang := s.language(w, r)
	data := s.baseData(lang)
	if tab := r.URL.Query().Get("tab"); tab != "" {
		data.activateTab(tab)
	}
	switch r.URL.Query().Get("saved") {
	case "1":
		if data.EnvKeyOverride || data.EnvBaseURLOverride {
			data.Notice = tr(lang, "notice.saved_env")
		} else {
			data.Notice = tr(lang, "notice.saved")
		}
	}
	s.render(w, http.StatusOK, "page", data)
}

// language resolves the page language from ?lang=, then the saved cookie, and
// defaults to Vietnamese.
func (s *Server) language(w http.ResponseWriter, r *http.Request) string {
	if value, ok := parseLanguage(r.URL.Query().Get("lang")); ok {
		http.SetCookie(w, &http.Cookie{
			Name:     langCookie,
			Value:    value,
			Path:     "/",
			MaxAge:   365 * 24 * 60 * 60,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		return value
	}
	if cookie, err := r.Cookie(langCookie); err == nil {
		if value, ok := parseLanguage(cookie.Value); ok {
			return value
		}
	}
	return langVI
}

func (s *Server) baseData(lang string) pageData {
	cfg := s.manager.Snapshot()
	data := pageData{
		Title:             "SMR Setup",
		Version:           s.version,
		CSRF:              s.csrf,
		Lang:              lang,
		ConfigPath:        s.manager.Path(),
		APIKeyPlaceholder: tr(lang, "placeholder.no_key"),
		Config:            cfg,
		Tiers:             config.Tiers,
		Stats:             s.stats(),
	}
	if cfg.Classifier.APIKey != "" {
		data.APIKeyPlaceholder = tr(lang, "placeholder.key_saved")
	}
	data.DashboardURL, data.EnvironmentLabel = dashboardFor(cfg.Classifier.BaseURL)
	data.EnvKeyOverride, data.EnvBaseURLOverride = config.EnvOverrides()
	names := profileNames(cfg.Profiles)
	for _, name := range names {
		profile := cfg.Profiles[name]
		view := profileView{Name: name, Label: profileLabel(name), Profile: profile, Total: len(config.Tiers)}
		for _, tier := range config.Tiers {
			entry := profile.Tiers[tier]
			placeholder := tr(lang, "placeholder.none")
			if entry.APIKey != "" {
				placeholder = tr(lang, "placeholder.saved")
			}
			if entry.BaseURL != "" && entry.Model != "" {
				view.Configured++
			}
			view.Tiers = append(view.Tiers, tierView{
				Name: tier, Label: tierLabel(lang, tier), BaseURL: entry.BaseURL, Model: entry.Model, KeyPlaceholder: placeholder,
			})
		}
		data.Profiles = append(data.Profiles, view)
	}
	data.activateTab("")
	for _, detection := range hostcfg.Detect() {
		view := hostView{
			Detection: detection,
			Label:     profileLabel(detection.Host),
			UsesSMR:   hostUsesSMR(detection, cfg.Listen),
		}
		instructions, err := hostcfg.InstructionsFor(detection.Host, cfg.Listen)
		if err != nil {
			// Leaving step 3 empty looks like SMR has nothing to add; say why.
			if data.Error == "" {
				data.Error = trf(lang, "error.host_instructions", detection.Host, err.Error())
			}
		} else {
			view.Parts = instructions.Parts
		}
		data.Detections = append(data.Detections, view)
	}
	data.Steps = buildSteps(cfg, data.Detections, data.Stats, lang)
	data.Verified = data.Stats.Forwarded > 0
	data.setLanguageURLs()
	return data
}

// activateTab marks exactly one profile tab as selected. An unknown or empty
// name falls back to the first tab so the page always shows a panel.
func (d *pageData) activateTab(name string) {
	if len(d.Profiles) == 0 {
		return
	}
	selected := 0
	for index := range d.Profiles {
		if d.Profiles[index].Name == name {
			selected = index
			break
		}
	}
	for index := range d.Profiles {
		d.Profiles[index].Active = index == selected
	}
	d.ActiveTab = d.Profiles[selected].Name
	d.setLanguageURLs()
}

// setLanguageURLs keeps the selected tab when the user switches language.
func (d *pageData) setLanguageURLs() {
	base := "/?"
	if d.ActiveTab != "" {
		base += "tab=" + url.QueryEscape(d.ActiveTab) + "&"
	}
	d.LangURLVI = base + "lang=" + langVI
	d.LangURLEN = base + "lang=" + langEN
}

// profileNames keeps Codex and Claude Code in a fixed order regardless of map
// iteration, then appends any other profile alphabetically.
func profileNames(profiles map[string]config.Profile) []string {
	names := make([]string, 0, len(profiles))
	for _, name := range []string{hostcfg.Codex, hostcfg.Claude} {
		if _, ok := profiles[name]; ok {
			names = append(names, name)
		}
	}
	extra := make([]string, 0, len(profiles))
	for name := range profiles {
		if name != hostcfg.Codex && name != hostcfg.Claude {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(names, extra...)
}

func profileLabel(name string) string {
	switch name {
	case hostcfg.Codex:
		return "Codex"
	case hostcfg.Claude:
		return "Claude Code"
	default:
		if name == "" {
			return name
		}
		return strings.ToUpper(name[:1]) + name[1:]
	}
}

// dashboardFor maps the classifier API host to the matching dashboard. A user
// on testing must sign up, top up and create the key on the testing dashboard,
// because keys and balances are not shared between environments.
func dashboardFor(baseURL string) (string, string) {
	host := ""
	if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil {
		host = strings.ToLower(parsed.Hostname())
	}
	// Match exact hosts, not a substring: `api.contest.example` or
	// `api.latest.example` is not a testing environment.
	if host == "api-testing.acmeai.vn" || host == "llm-testing.acmeai.vn" ||
		strings.HasSuffix(host, "-testing.acmeai.vn") {
		return "https://llm-testing.acmeai.vn", "Testing"
	}
	// Production needs no suffix: the badge would only add noise on the
	// default environment.
	return "https://llm.acmeai.vn", ""
}

// hostUsesSMR reports whether a host already points at this SMR instance.
func hostUsesSMR(detection hostcfg.Detection, listen string) bool {
	if detection.Provider == "smr" {
		return true
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" || detection.BaseURL == "" {
		return false
	}
	return strings.Contains(detection.BaseURL, "127.0.0.1:"+port)
}

func buildSteps(cfg config.Config, detections []hostView, stats proxy.Stats, lang string) []stepView {
	classified := cfg.Classifier.APIKey != ""
	readyProfile := false
	for _, profile := range cfg.Profiles {
		complete := true
		for _, tier := range config.Tiers {
			entry := profile.Tiers[tier]
			if entry.BaseURL == "" || entry.Model == "" {
				complete = false
				break
			}
		}
		if complete {
			readyProfile = true
			break
		}
	}
	hostReady := false
	for _, detection := range detections {
		if detection.UsesSMR {
			hostReady = true
			break
		}
	}
	return []stepView{
		{N: 1, Title: tr(lang, "steps.connect"), Done: classified},
		{N: 2, Title: tr(lang, "steps.models"), Done: readyProfile},
		{N: 3, Title: tr(lang, "steps.install"), Done: hostReady},
		{N: 4, Title: tr(lang, "steps.verify"), Done: stats.Forwarded > 0},
	}
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(w, r) {
		return
	}
	lang := s.language(w, r)
	cfg := s.manager.Snapshot()
	cfg.Classifier.BaseURL = strings.TrimRight(strings.TrimSpace(r.PostFormValue("classifier_base_url")), "/")
	if key := strings.TrimSpace(r.PostFormValue("classifier_api_key")); key != "" {
		cfg.Classifier.APIKey = key
	}
	if maximum, ok := parseInt(r.PostFormValue("max_chars")); ok && maximum > 0 {
		cfg.Classifier.MaxChars = int(maximum)
	}
	if tier := strings.TrimSpace(r.PostFormValue("fallback_tier")); config.IsTier(tier) {
		cfg.Classifier.FallbackTier = tier
	}
	for name, profile := range cfg.Profiles {
		for _, tier := range config.Tiers {
			entry := profile.Tiers[tier]
			baseURL := strings.TrimRight(strings.TrimSpace(r.PostFormValue(name+"_"+tier+"_base_url")), "/")
			model := strings.TrimSpace(r.PostFormValue(name + "_" + tier + "_model"))
			apiKey := strings.TrimSpace(r.PostFormValue(name + "_" + tier + "_api_key"))
			entry.BaseURL = baseURL
			entry.Model = model
			if baseURL == "" && model == "" {
				entry.APIKey = ""
			} else if apiKey != "" {
				entry.APIKey = apiKey
			}
			profile.Tiers[tier] = entry
		}
		cfg.Profiles[name] = profile
	}
	if err := s.manager.Update(cfg); err != nil {
		data := s.baseData(lang)
		data.activateTab(r.PostFormValue("smr_tab"))
		data.Error = trf(lang, "error.save", err.Error())
		s.render(w, http.StatusBadRequest, "page", data)
		return
	}
	redirect := "/?saved=1"
	if tab := strings.TrimSpace(r.PostFormValue("smr_tab")); tab != "" {
		redirect += "&tab=" + url.QueryEscape(tab)
	}
	redirect += "&lang=" + lang
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(w, r) {
		return
	}
	lang := s.language(w, r)
	cfg := s.manager.Snapshot()
	formKey := strings.TrimSpace(r.PostFormValue("classifier_api_key"))
	if formKey == "" {
		formKey = strings.TrimSpace(r.PostFormValue("api_key"))
	}
	baseURL := strings.TrimRight(strings.TrimSpace(r.PostFormValue("classifier_base_url")), "/")
	if baseURL == "" {
		baseURL = cfg.Classifier.BaseURL
	}
	key := formKey
	if key == "" {
		key = cfg.Classifier.APIKey
	}
	tab := r.PostFormValue("smr_tab")
	maxChars := cfg.Classifier.MaxChars
	if maximum, ok := parseInt(r.PostFormValue("max_chars")); ok && maximum > 0 {
		maxChars = int(maximum)
	}
	fallback := cfg.Classifier.FallbackTier
	if tier := strings.TrimSpace(r.PostFormValue("fallback_tier")); config.IsTier(tier) {
		fallback = tier
	}
	overrideKey, overrideBaseURL := config.EnvOverrides()
	// A failed check echoes what the user typed. The saved config still holds
	// the previous environment, so rendering it unchanged would silently reset
	// the form and make a temporary error look like a change of mind.
	fail := func(result *testResult) {
		data := s.baseData(lang)
		data.activateTab(tab)
		data.Config.Classifier.BaseURL = baseURL
		data.Config.Classifier.MaxChars = maxChars
		data.Config.Classifier.FallbackTier = fallback
		data.Test = result
		s.render(w, http.StatusOK, "page", data)
	}
	if key == "" {
		fail(&testResult{Error: tr(lang, "error.no_key")})
		return
	}
	client := classifier.New(baseURL, key, 15*time.Second, 0)
	account, err := client.Account(r.Context())
	if err != nil {
		fail(&testResult{Error: publicClassifierError(lang, err), EnvOverride: overrideKey || overrideBaseURL})
		return
	}
	// Anything that just answered with a balance is known-good, so keep it
	// instead of making the user paste the secret or the URL again.
	saved := false
	if formKey != "" && formKey != cfg.Classifier.APIKey {
		cfg.Classifier.APIKey = key
		saved = true
	}
	if baseURL != cfg.Classifier.BaseURL {
		cfg.Classifier.BaseURL = baseURL
		saved = true
	}
	if maxChars != cfg.Classifier.MaxChars {
		cfg.Classifier.MaxChars = maxChars
		saved = true
	}
	if fallback != cfg.Classifier.FallbackTier {
		cfg.Classifier.FallbackTier = fallback
		saved = true
	}
	if saved {
		if updateErr := s.manager.Update(cfg); updateErr != nil {
			fail(&testResult{Error: trf(lang, "error.test_save", updateErr.Error())})
			return
		}
	}
	// Build the page after the save. The key pill, the onboarding callout and
	// the dashboard link all come from the config, so a snapshot taken before
	// Update would say "saved" while still showing "no key" and the previous
	// environment's dashboard.
	data := s.baseData(lang)
	data.activateTab(tab)
	data.Test = &testResult{
		OK:              true,
		BalanceVND:      account.BalanceVND,
		MaxStateChars:   account.MaxStateChars,
		CacheTTLSeconds: account.CacheTTLSeconds,
		Saved:           saved,
		EnvOverride:     overrideKey || overrideBaseURL,
	}
	s.render(w, http.StatusOK, "page", data)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	lang := s.language(w, r)
	path := filepath.Join(s.dataDir, LogFile)
	body, err := logtail.Tail(path, 200)
	if err != nil {
		body = trf(lang, "logs.empty", path)
	}
	s.render(w, http.StatusOK, "logs", logsData{
		Path:      path,
		Logs:      body,
		Lang:      lang,
		LangURLVI: "/logs?lang=" + langVI,
		LangURLEN: "/logs?lang=" + langEN,
	})
}

func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form.", http.StatusBadRequest)
		return false
	}
	if !equal(r.PostFormValue("csrf"), s.csrf) {
		http.Error(w, "CSRF token invalid.", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) render(w http.ResponseWriter, status int, page string, value any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.pages[page].Execute(w, value); err != nil {
		s.logger.Error("render setup page", "error", err)
	}
}

// renderLocked explains how to authenticate instead of returning a bare error.
// Opening http://127.0.0.1:8788 by hand used to print a one-line 401 that gave
// no hint about where to run the command.
func (s *Server) renderLocked(w http.ResponseWriter, r *http.Request, invalid bool) {
	lang := s.language(w, r)
	s.render(w, http.StatusUnauthorized, "locked", lockedData{
		Lang:      lang,
		LangURLVI: "/?lang=" + langVI,
		LangURLEN: "/?lang=" + langEN,
		Version:   s.version,
		TokenPath: filepath.Join(s.dataDir, tokenFile),
		UIHost:    r.Host,
		Command:   setupCommand(),
		Invalid:   invalid,
	})
}

// setupCommand prefers the short form, but users who installed SMR in a shell
// that has not reloaded PATH would only see "smr is not recognized". Showing the
// absolute path keeps the instruction copy-pasteable.
func setupCommand() string {
	_, err := exec.LookPath("smr")
	exe, exeErr := os.Executable()
	if exeErr != nil {
		exe = ""
	}
	return setupCommandFor(runtime.GOOS, exe, err == nil)
}

// setupCommandFor builds the copy-pasteable command. PowerShell does not run a
// quoted path as a command (it needs the call operator), so `"C:\...\smr.exe"
// setup` fails with "Unexpected token 'setup'".
func setupCommandFor(goos, exe string, onPath bool) string {
	if onPath || exe == "" {
		return "smr setup"
	}
	if goos == "windows" {
		return `& "` + exe + `" setup`
	}
	return `"` + exe + `" setup`
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self' data:; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func equal(left, right string) bool {
	if left == "" || right == "" || len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func parseInt(value string) (int64, bool) {
	var result int64
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 12 {
		return 0, false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
		result = result*10 + int64(char-'0')
	}
	return result, true
}

func publicClassifierError(lang string, err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "invalid or revoked") {
		return tr(lang, "error.invalid_key")
	}
	if strings.Contains(err.Error(), "Not enough") {
		return tr(lang, "error.no_balance")
	}
	return tr(lang, "error.network")
}

// GenerateToken creates a URL-safe 256-bit secret.
func GenerateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func WriteTokenFile(dataDir, token string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, tokenFile)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := fileperm.Restrict(path); err != nil {
		return "", err
	}
	return path, nil
}

func ReadTokenFile(dataDir string) (string, error) {
	body, err := os.ReadFile(filepath.Join(dataDir, tokenFile))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("UI token file is empty")
	}
	return token, nil
}
