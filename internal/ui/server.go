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
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	Change           *hostcfg.Change
	Test             *testResult
	Error            string
	Notice           string
}

type logsData struct {
	Path      string
	Logs      string
	Lang      string
	LangURLVI string
	LangURLEN string
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
	server.pages = map[string]*template.Template{"page": page, "logs": logs}
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
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /", s.authorize(s.handleIndex))
	mux.HandleFunc("POST /save", s.authorize(s.handleSave))
	mux.HandleFunc("POST /test", s.authorize(s.handleTest))
	mux.HandleFunc("POST /hosts/preview", s.authorize(s.handlePreview))
	mux.HandleFunc("POST /hosts/apply", s.authorize(s.handleApply))
	mux.HandleFunc("GET /logs", s.authorize(s.handleLogs))
	return securityHeaders(mux)
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
			http.Error(w, "Invalid setup token.", http.StatusUnauthorized)
			return
		}
		cookie, err := r.Cookie(tokenCookie)
		if err != nil || !equal(cookie.Value, s.token) {
			http.Error(w, "Setup token required. Run `smr setup` from a terminal.", http.StatusUnauthorized)
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
		data.Notice = tr(lang, "notice.saved")
	}
	if r.URL.Query().Get("applied") != "" {
		data.Notice = trf(lang, "notice.applied", r.URL.Query().Get("applied"))
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
		data.Detections = append(data.Detections, hostView{
			Detection: detection,
			Label:     profileLabel(detection.Host),
			UsesSMR:   hostUsesSMR(detection, cfg.Listen),
		})
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
	if strings.Contains(host, "testing") || strings.Contains(host, "test") {
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
	key := strings.TrimSpace(r.PostFormValue("classifier_api_key"))
	if key == "" {
		key = strings.TrimSpace(r.PostFormValue("api_key"))
	}
	baseURL := strings.TrimRight(strings.TrimSpace(r.PostFormValue("classifier_base_url")), "/")
	if baseURL == "" {
		baseURL = cfg.Classifier.BaseURL
	}
	newKey := key != "" && key != cfg.Classifier.APIKey
	if key == "" {
		key = cfg.Classifier.APIKey
	}
	data := s.baseData(lang)
	data.activateTab(r.PostFormValue("smr_tab"))
	if key == "" {
		data.Test = &testResult{Error: tr(lang, "error.no_key")}
		s.render(w, http.StatusOK, "page", data)
		return
	}
	client := classifier.New(baseURL, key, 15*time.Second, 0)
	account, err := client.Account(r.Context())
	if err != nil {
		data.Test = &testResult{Error: publicClassifierError(lang, err)}
	} else {
		saved := false
		if newKey {
			// A key that just answered with a balance is known-good, so keep it
			// instead of making the user paste the secret a second time.
			cfg.Classifier.APIKey = key
			cfg.Classifier.BaseURL = baseURL
			if maximum, ok := parseInt(r.PostFormValue("max_chars")); ok && maximum > 0 {
				cfg.Classifier.MaxChars = int(maximum)
			}
			if tier := strings.TrimSpace(r.PostFormValue("fallback_tier")); config.IsTier(tier) {
				cfg.Classifier.FallbackTier = tier
			}
			if updateErr := s.manager.Update(cfg); updateErr != nil {
				data.Test = &testResult{Error: trf(lang, "error.test_save", updateErr.Error())}
				s.render(w, http.StatusOK, "page", data)
				return
			}
			saved = true
		}
		data.Test = &testResult{
			OK:              true,
			BalanceVND:      account.BalanceVND,
			MaxStateChars:   account.MaxStateChars,
			CacheTTLSeconds: account.CacheTTLSeconds,
			Saved:           saved,
		}
	}
	s.render(w, http.StatusOK, "page", data)
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(w, r) {
		return
	}
	lang := s.language(w, r)
	host := strings.TrimSpace(r.PostFormValue("host"))
	cfg := s.manager.Snapshot()
	change, err := hostcfg.PreviewFor(host, s.dataDir, cfg.Listen)
	data := s.baseData(lang)
	data.activateTab(r.PostFormValue("smr_tab"))
	if err != nil {
		data.Error = trf(lang, "error.preview", err.Error())
		s.render(w, http.StatusBadRequest, "page", data)
		return
	}
	data.Change = &change
	s.render(w, http.StatusOK, "page", data)
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(w, r) {
		return
	}
	lang := s.language(w, r)
	host := strings.TrimSpace(r.PostFormValue("host"))
	change := hostcfg.Change{Host: host, ExpectedHash: strings.TrimSpace(r.PostFormValue("expected"))}
	cfg := s.manager.Snapshot()
	if err := hostcfg.ApplyFor(change, s.dataDir, cfg.Listen); err != nil {
		data := s.baseData(lang)
		data.activateTab(r.PostFormValue("smr_tab"))
		data.Error = trf(lang, "error.apply", err.Error())
		s.render(w, http.StatusConflict, "page", data)
		return
	}
	s.logger.Info("host config updated", "host", host)
	http.Redirect(w, r, "/?applied="+url.QueryEscape(host)+"&lang="+lang, http.StatusSeeOther)
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

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self' data:; script-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
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
