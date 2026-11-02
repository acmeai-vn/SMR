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
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/acmeai-vn/SMR/internal/classifier"
	"github.com/acmeai-vn/SMR/internal/config"
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
	Name    string
	Profile config.Profile
	Tiers   []tierView
}

type tierView struct {
	Name           string
	BaseURL        string
	Model          string
	KeyPlaceholder string
}

type testResult struct {
	OK              bool
	Error           string
	BalanceVND      int64
	MaxStateChars   int
	CacheTTLSeconds int64
}

type pageData struct {
	Title             string
	Version           string
	CSRF              string
	ConfigPath        string
	APIKeyPlaceholder string
	Config            config.Config
	Profiles          []profileView
	Tiers             []string
	Stats             proxy.Stats
	Detections        []hostcfg.Detection
	Change            *hostcfg.Change
	Test              *testResult
	Error             string
	Notice            string
}

type logsData struct {
	Path string
	Logs string
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
		"lower": strings.ToLower,
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
				http.Redirect(w, r, "/", http.StatusSeeOther)
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
	data := s.baseData()
	switch r.URL.Query().Get("saved") {
	case "1":
		data.Notice = "Đã lưu cấu hình. Daemon dùng cấu hình mới cho request tiếp theo."
	}
	if r.URL.Query().Get("applied") != "" {
		data.Notice = "Đã cập nhật cấu hình " + r.URL.Query().Get("applied") + ". Khởi động lại host để nhận thay đổi."
	}
	s.render(w, http.StatusOK, "page", data)
}

func (s *Server) baseData() pageData {
	cfg := s.manager.Snapshot()
	data := pageData{
		Title:             "SMR Setup",
		Version:           s.version,
		CSRF:              s.csrf,
		ConfigPath:        s.manager.Path(),
		APIKeyPlaceholder: "Chưa có key",
		Config:            cfg,
		Tiers:             config.Tiers,
		Stats:             s.stats(),
		Detections:        hostcfg.Detect(),
	}
	if cfg.Classifier.APIKey != "" {
		data.APIKeyPlaceholder = "Đã lưu key · để trống nếu không đổi"
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile := cfg.Profiles[name]
		view := profileView{Name: name, Profile: profile}
		for _, tier := range config.Tiers {
			entry := profile.Tiers[tier]
			placeholder := "Chưa có"
			if entry.APIKey != "" {
				placeholder = "Đã lưu · để trống nếu không đổi"
			}
			view.Tiers = append(view.Tiers, tierView{
				Name: tier, BaseURL: entry.BaseURL, Model: entry.Model, KeyPlaceholder: placeholder,
			})
		}
		data.Profiles = append(data.Profiles, view)
	}
	return data
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(w, r) {
		return
	}
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
		data := s.baseData()
		data.Error = "Không lưu được cấu hình: " + err.Error()
		s.render(w, http.StatusBadRequest, "page", data)
		return
	}
	http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(w, r) {
		return
	}
	cfg := s.manager.Snapshot()
	key := strings.TrimSpace(r.PostFormValue("api_key"))
	if key == "" {
		key = cfg.Classifier.APIKey
	}
	data := s.baseData()
	if key == "" {
		data.Test = &testResult{Error: "Chưa có API key. Nhập key hoặc lưu key trước."}
		s.render(w, http.StatusOK, "page", data)
		return
	}
	client := classifier.New(cfg.Classifier.BaseURL, key, 15*time.Second, 0)
	account, err := client.Account(r.Context())
	if err != nil {
		data.Test = &testResult{Error: publicClassifierError(err)}
	} else {
		data.Test = &testResult{
			OK:              true,
			BalanceVND:      account.BalanceVND,
			MaxStateChars:   account.MaxStateChars,
			CacheTTLSeconds: account.CacheTTLSeconds,
		}
	}
	s.render(w, http.StatusOK, "page", data)
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(w, r) {
		return
	}
	host := strings.TrimSpace(r.PostFormValue("host"))
	change, err := hostcfg.Preview(host, s.dataDir)
	data := s.baseData()
	if err != nil {
		data.Error = "Không tạo được diff: " + err.Error()
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
	host := strings.TrimSpace(r.PostFormValue("host"))
	change := hostcfg.Change{Host: host, ExpectedHash: strings.TrimSpace(r.PostFormValue("expected"))}
	if err := hostcfg.Apply(change, s.dataDir); err != nil {
		data := s.baseData()
		data.Error = "Không ghi được cấu hình host: " + err.Error()
		s.render(w, http.StatusConflict, "page", data)
		return
	}
	s.logger.Info("host config updated", "host", host)
	http.Redirect(w, r, "/?applied="+host, http.StatusSeeOther)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.dataDir, LogFile)
	body, err := logtail.Tail(path, 200)
	if err != nil {
		body = "Chưa có log tại " + path + "\n"
	}
	s.render(w, http.StatusOK, "logs", logsData{Path: path, Logs: body})
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

func publicClassifierError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "invalid or revoked") {
		return "API key không hợp lệ hoặc đã bị thu hồi."
	}
	if strings.Contains(err.Error(), "Not enough") {
		return "Key hợp lệ nhưng tài khoản chưa có số dư."
	}
	return "Không kết nối được api.acmeai.vn. Kiểm tra mạng và base URL."
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
