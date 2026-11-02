// Package proxy implements SMR's local routing endpoint. It forwards the
// original request body to a provider endpoint after changing only the model.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/acmeai-vn/SMR/internal/classifier"
	"github.com/acmeai-vn/SMR/internal/config"
	"github.com/acmeai-vn/SMR/internal/state"
)

const maxRequestBytes = 32 << 20

type Stats struct {
	StartedAt          time.Time `json:"started_at"`
	Requests           int64     `json:"requests"`
	Forwarded          int64     `json:"forwarded"`
	ClassifierOK       int64     `json:"classifier_ok"`
	ClassifierFallback int64     `json:"classifier_fallback"`
	ClassifierErrors   int64     `json:"classifier_errors"`
	CacheHits          int64     `json:"cache_hits"`
	UpstreamErrors     int64     `json:"upstream_errors"`
	LastTier           string    `json:"last_tier,omitempty"`
	LastChargedVND     int64     `json:"last_charged_vnd"`
	LastBalanceVND     int64     `json:"last_balance_vnd"`
	LastClassifierAt   string    `json:"last_classifier_at,omitempty"`
	LastClassifierOK   bool      `json:"last_classifier_ok"`
	LastError          string    `json:"last_error,omitempty"`
}

type counters struct {
	requests           atomic.Int64
	forwarded          atomic.Int64
	classifierOK       atomic.Int64
	classifierFallback atomic.Int64
	classifierErrors   atomic.Int64
	cacheHits          atomic.Int64
	upstreamErrors     atomic.Int64

	lastTier         atomic.Value
	lastCharged      atomic.Int64
	lastBalance      atomic.Int64
	lastClassifierAt atomic.Value
	lastClassifierOK atomic.Bool
	lastError        atomic.Value
}

type Server struct {
	manager *config.Manager
	logger  *slog.Logger
	client  *http.Client
	stats   counters
	started time.Time
}

func New(manager *config.Manager, logger *slog.Logger) *Server {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	return &Server{
		manager: manager,
		logger:  logger,
		client:  &http.Client{Transport: transport},
		started: time.Now().UTC(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /stats", s.handleStats)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /models", s.handleModels)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("POST /v1/messages", s.handleMessages)
	mux.HandleFunc("POST /v1/messages/count_tokens", s.handleCountTokens)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	return noStore(mux)
}

func (s *Server) Snapshot() Stats {
	stats := Stats{
		StartedAt:          s.started,
		Requests:           s.stats.requests.Load(),
		Forwarded:          s.stats.forwarded.Load(),
		ClassifierOK:       s.stats.classifierOK.Load(),
		ClassifierFallback: s.stats.classifierFallback.Load(),
		ClassifierErrors:   s.stats.classifierErrors.Load(),
		CacheHits:          s.stats.cacheHits.Load(),
		UpstreamErrors:     s.stats.upstreamErrors.Load(),
		LastChargedVND:     s.stats.lastCharged.Load(),
		LastBalanceVND:     s.stats.lastBalance.Load(),
		LastClassifierOK:   s.stats.lastClassifierOK.Load(),
	}
	if value, ok := s.stats.lastTier.Load().(string); ok {
		stats.LastTier = value
	}
	if value, ok := s.stats.lastClassifierAt.Load().(string); ok {
		stats.LastClassifierAt = value
	}
	if value, ok := s.stats.lastError.Load().(string); ok {
		stats.LastError = value
	}
	return stats
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := s.manager.Snapshot()
	stats := s.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": "2",
		"classifier": map[string]any{
			"base_url":   cfg.Classifier.BaseURL,
			"configured": cfg.Classifier.APIKey != "",
			"last_ok":    stats.LastClassifierOK,
			"last_at":    stats.LastClassifierAt,
		},
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Snapshot())
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.manager.Snapshot()
	profileName := strings.TrimSpace(r.URL.Query().Get("profile"))
	if profileName == "" {
		profileName = "codex"
	}
	profile, ok := cfg.Profiles[profileName]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown_profile", "Unknown profile.")
		return
	}
	seen := map[string]bool{"smart": true}
	models := []map[string]string{{"id": "smart", "object": "model", "owned_by": "acmeai"}}
	for _, tier := range config.Tiers {
		model := strings.TrimSpace(profile.Tiers[tier].Model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		models = append(models, map[string]string{"id": model, "object": "model", "owned_by": "smr"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	s.route(w, r, "codex", "/responses")
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	s.route(w, r, "claude", "/v1/messages")
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	cfg := s.manager.Snapshot()
	profile := cfg.ChatCompletionsProfile
	if profile == "" {
		profile = "codex"
	}
	s.route(w, r, profile, "/v1/chat/completions")
}

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	s.stats.requests.Add(1)
	cfg := s.manager.Snapshot()
	profile, ok := cfg.Profiles["claude"]
	if !ok || strings.TrimSpace(profile.Tiers["easy"].BaseURL) == "" {
		writeError(w, http.StatusNotImplemented, "count_tokens_unavailable", "Claude easy tier is not configured.")
		return
	}
	tier := profile.Tiers["easy"]
	body, payload, err := readJSONBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	payload["model"] = tier.Model
	body, err = json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode_failed", "Could not encode the upstream request.")
		return
	}
	s.forwardBody(w, r, profile, tier, "/v1/messages/count_tokens", headers{
		tier:       "easy",
		cache:      "miss",
		chargedVND: 0,
		balanceVND: 0,
		classifier: "skipped",
	}, body)
}

type headers struct {
	tier       string
	cache      string
	chargedVND int64
	balanceVND int64
	classifier string
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, profileName, upstreamPath string) {
	s.stats.requests.Add(1)
	cfg := s.manager.Snapshot()
	profile, ok := cfg.Profiles[profileName]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown_profile", "Profile is not configured.")
		return
	}

	body, payload, err := readJSONBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	stateText := state.Truncate(state.Extract(profile.API, payload), cfg.Classifier.MaxChars)

	diagnostics := headers{tier: cfg.Classifier.FallbackTier, cache: "miss", classifier: "fallback"}
	if stateText == "" {
		s.stats.classifierFallback.Add(1)
		s.logger.Info("empty state; using fallback tier", "host", profileName, "tier", cfg.Classifier.FallbackTier)
	} else {
		client := classifier.New(cfg.Classifier.BaseURL, cfg.Classifier.APIKey,
			time.Duration(cfg.Classifier.TimeoutMS)*time.Millisecond, cfg.Classifier.Retry)
		timeout := time.Duration(cfg.Classifier.TimeoutMS) * time.Millisecond
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		result, err := client.Classify(ctx, stateText, classifierHost(profileName))
		cancel()
		if err != nil {
			tier, ok := s.handleClassifierError(w, err, cfg)
			if !ok {
				return
			}
			diagnostics.tier = tier
			diagnostics.classifier = "fallback"
			s.stats.classifierFallback.Add(1)
		} else {
			if !config.IsTier(result.Tier) {
				writeError(w, http.StatusBadGateway, "invalid_tier", "Classifier returned an unknown tier.")
				return
			}
			diagnostics.tier = result.Tier
			diagnostics.cache = result.Cache
			diagnostics.chargedVND = result.ChargedVND
			diagnostics.balanceVND = result.BalanceVND
			diagnostics.classifier = "ok"
			s.stats.classifierOK.Add(1)
			s.stats.lastClassifierOK.Store(true)
			s.stats.lastClassifierAt.Store(time.Now().UTC().Format(time.RFC3339))
			s.stats.lastTier.Store(result.Tier)
			s.stats.lastCharged.Store(result.ChargedVND)
			s.stats.lastBalance.Store(result.BalanceVND)
			if result.Cache == "hit" {
				s.stats.cacheHits.Add(1)
			}
		}
	}

	tier, ok := profile.Tiers[diagnostics.tier]
	if !ok || strings.TrimSpace(tier.BaseURL) == "" || strings.TrimSpace(tier.Model) == "" {
		writeError(w, http.StatusServiceUnavailable, "tier_not_configured", "The selected tier has no provider model configured.")
		return
	}
	payload["model"] = tier.Model
	body, err = json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode_failed", "Could not encode the upstream request.")
		return
	}
	s.forwardBody(w, r, profile, tier, upstreamPath, diagnostics, body)
}

func (s *Server) handleClassifierError(w http.ResponseWriter, err error, cfg config.Config) (string, bool) {
	s.stats.classifierErrors.Add(1)
	s.stats.lastClassifierOK.Store(false)
	s.stats.lastClassifierAt.Store(time.Now().UTC().Format(time.RFC3339))
	s.stats.lastError.Store(safeError(err))

	switch {
	case errors.Is(err, classifier.ErrPaymentRequired):
		var upstream *classifier.Error
		if errors.As(err, &upstream) && len(upstream.Body) > 0 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-SMR-Classifier", "error")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write(upstream.Body)
		} else {
			writeSMRHeaders(w, headers{cache: "miss", classifier: "error"})
			writeError(w, http.StatusPaymentRequired, "insufficient_balance",
				"Not enough ACME AI balance. Top up at https://llm.acmeai.vn/topup")
		}
		return "", false
	case errors.Is(err, classifier.ErrUnauthorized):
		writeSMRHeaders(w, headers{cache: "miss", classifier: "error"})
		writeError(w, http.StatusBadGateway, "classifier_unauthorized",
			"ACME AI API key is invalid or revoked. Update it in smr setup.")
		return "", false
	default:
		if cfg.Classifier.OnTransientError == "fallback" {
			return cfg.Classifier.FallbackTier, true
		}
		writeSMRHeaders(w, headers{cache: "miss", classifier: "error"})
		writeError(w, http.StatusBadGateway, "classifier_unavailable",
			"ACME AI classifier is temporarily unavailable.")
		return "", false
	}
}

func (s *Server) forwardBody(w http.ResponseWriter, r *http.Request, profile config.Profile, tier config.Tier, path string, diagnostic headers, body []byte) {
	target, err := joinURL(tier.BaseURL, path)
	if err != nil {
		s.stats.upstreamErrors.Add(1)
		writeSMRHeaders(w, diagnostic)
		writeError(w, http.StatusBadGateway, "invalid_provider_url", "Provider base URL is invalid.")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request_failed", "Could not build the provider request.")
		return
	}
	copyForwardHeaders(req.Header, r.Header)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Del("Authorization")
	req.Header.Del("X-Api-Key")
	switch profile.Auth {
	case "x-api-key":
		req.Header.Set("x-api-key", tier.APIKey)
	default:
		req.Header.Set("Authorization", "Bearer "+tier.APIKey)
	}
	if profile.API == "messages" {
		if req.Header.Get("Anthropic-Version") == "" && profile.AnthropicVersion != "" {
			req.Header.Set("Anthropic-Version", profile.AnthropicVersion)
		}
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.stats.upstreamErrors.Add(1)
		s.stats.lastError.Store(safeError(err))
		writeSMRHeaders(w, diagnostic)
		writeError(w, http.StatusBadGateway, "provider_unreachable", "Could not reach the selected provider.")
		return
	}
	defer resp.Body.Close()
	s.stats.forwarded.Add(1)
	copyResponseHeaders(w.Header(), resp.Header)
	writeSMRHeaders(w, diagnostic)
	w.WriteHeader(resp.StatusCode)
	copyStream(w, resp.Body)
}

func joinURL(base, path string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "", fmt.Errorf("empty base URL")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid base URL")
	}
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path, nil
}

func readJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, map[string]any, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("Request body is too large or unreadable.")
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, nil, fmt.Errorf("Request body must be a JSON object.")
	}
	return body, payload, nil
}

func classifierHost(profile string) string {
	switch profile {
	case "codex":
		return "codex"
	case "claude":
		return "claude"
	default:
		return "other"
	}
}

func writeSMRHeaders(w http.ResponseWriter, diagnostic headers) {
	if diagnostic.tier != "" {
		w.Header().Set("X-SMR-Tier", diagnostic.tier)
	}
	if diagnostic.cache != "" {
		w.Header().Set("X-SMR-Cache", diagnostic.cache)
	}
	if diagnostic.classifier != "" {
		w.Header().Set("X-SMR-Classifier", diagnostic.classifier)
	}
	w.Header().Set("X-SMR-Charged-VND", fmt.Sprintf("%d", diagnostic.chargedVND))
	w.Header().Set("X-SMR-Balance-VND", fmt.Sprintf("%d", diagnostic.balanceVND))
}

func copyForwardHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHop(key) || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHop(key) || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isHopByHop(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func copyStream(w http.ResponseWriter, body io.Reader) {
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		n, err := body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
