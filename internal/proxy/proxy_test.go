package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acmeai-vn/SMR/internal/config"
)

type upstreamCall struct {
	path   string
	body   map[string]any
	header http.Header
}

func testServer(t *testing.T, classifierHandler, upstreamHandler http.HandlerFunc) (*Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	classifierCalls := &atomic.Int32{}
	upstreamCalls := &atomic.Int32{}
	classifierServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		classifierCalls.Add(1)
		classifierHandler(w, r)
	}))
	t.Cleanup(classifierServer.Close)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		upstreamHandler(w, r)
	}))
	t.Cleanup(upstreamServer.Close)

	cfg := config.Default()
	cfg.Classifier.BaseURL = classifierServer.URL
	cfg.Classifier.APIKey = "acme_sk_test"
	cfg.Profiles["codex"].Tiers["easy"] = config.Tier{BaseURL: upstreamServer.URL + "/v1", APIKey: "codex-easy", Model: "small"}
	cfg.Profiles["codex"].Tiers["lower_medium"] = config.Tier{BaseURL: upstreamServer.URL + "/v1", APIKey: "codex-lm", Model: "medium-low"}
	cfg.Profiles["codex"].Tiers["upper_medium"] = config.Tier{BaseURL: upstreamServer.URL + "/v1", APIKey: "codex-um", Model: "fallback-model"}
	cfg.Profiles["codex"].Tiers["hard"] = config.Tier{BaseURL: upstreamServer.URL + "/v1", APIKey: "codex-hard", Model: "large"}
	cfg.Profiles["claude"].Tiers["easy"] = config.Tier{BaseURL: upstreamServer.URL, APIKey: "claude-easy", Model: "claude-small"}
	cfg.Profiles["claude"].Tiers["lower_medium"] = config.Tier{BaseURL: upstreamServer.URL, APIKey: "claude-lm", Model: "claude-mid"}
	cfg.Profiles["claude"].Tiers["upper_medium"] = config.Tier{BaseURL: upstreamServer.URL, APIKey: "claude-um", Model: "claude-fallback"}
	cfg.Profiles["claude"].Tiers["hard"] = config.Tier{BaseURL: upstreamServer.URL, APIKey: "claude-hard", Model: "claude-large"}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	manager, err := config.LoadManager(path)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(manager, logger), classifierCalls, upstreamCalls
}

func classifierOK(tier, cache string, charged int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tier": tier, "cache": cache, "charged_vnd": charged, "balance_vnd": 50000,
		})
	}
}

func TestResponsesRoutesAndReplacesModel(t *testing.T) {
	var got upstreamCall
	server, classifierCalls, upstreamCalls := testServer(t,
		classifierOK("easy", "miss", 2),
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &got.body)
			got.path = r.URL.Path
			got.header = r.Header.Clone()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: ok\n\n"))
		})
	body := `{"model":"smart","stream":true,"tools":[{"type":"function","name":"shell"}],"input":[{"role":"user","content":[{"type":"input_text","text":"fix the failing test"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if classifierCalls.Load() != 1 || upstreamCalls.Load() != 1 {
		t.Fatalf("classifier=%d upstream=%d", classifierCalls.Load(), upstreamCalls.Load())
	}
	if got.path != "/v1/responses" {
		t.Fatalf("upstream path=%q", got.path)
	}
	if got.body["model"] != "small" {
		t.Fatalf("model=%v", got.body["model"])
	}
	if got.body["stream"] != true || got.body["tools"] == nil {
		t.Fatalf("stream/tools not preserved: %#v", got.body)
	}
	if got.header.Get("Authorization") != "Bearer codex-easy" {
		t.Fatalf("authorization=%q", got.header.Get("Authorization"))
	}
	if rec.Header().Get("X-SMR-Tier") != "easy" || rec.Header().Get("X-SMR-Charged-VND") != "2" {
		t.Fatalf("diagnostic headers missing: %#v", rec.Header())
	}
}

func TestMessagesUsesXAPIKeyAndPreservesBeta(t *testing.T) {
	var got upstreamCall
	server, _, _ := testServer(t,
		classifierOK("hard", "hit", 3),
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &got.body)
			got.path = r.URL.Path
			got.header = r.Header.Clone()
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
	body := `{"model":"smart","max_tokens":100,"messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Anthropic-Beta", "tools-2024")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got.path != "/v1/messages" || got.body["model"] != "claude-large" {
		t.Fatalf("path=%q model=%v", got.path, got.body["model"])
	}
	if got.header.Get("x-api-key") != "claude-hard" || got.header.Get("Authorization") != "" {
		t.Fatalf("headers=%#v", got.header)
	}
	if got.header.Get("Anthropic-Beta") != "tools-2024" || got.header.Get("Anthropic-Version") != "2023-06-01" {
		t.Fatalf("anthropic headers=%#v", got.header)
	}
	if rec.Header().Get("X-SMR-Cache") != "hit" {
		t.Fatalf("cache header=%q", rec.Header().Get("X-SMR-Cache"))
	}
}

func TestPaymentRequiredStopsBeforeModel(t *testing.T) {
	server, _, upstreamCalls := testServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"code":"insufficient_balance","topup_url":"https://llm.acmeai.vn/topup"}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("model must not be called")
		})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"hello"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired || upstreamCalls.Load() != 0 {
		t.Fatalf("status=%d upstream=%d", rec.Code, upstreamCalls.Load())
	}
	if !strings.Contains(rec.Body.String(), "topup") {
		t.Fatalf("missing top-up url: %s", rec.Body.String())
	}
}

func TestUnauthorizedDoesNotFallback(t *testing.T) {
	server, _, upstreamCalls := testServer(t,
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		func(w http.ResponseWriter, r *http.Request) { t.Fatal("model must not be called") })
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"hello"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || upstreamCalls.Load() != 0 {
		t.Fatalf("status=%d upstream=%d", rec.Code, upstreamCalls.Load())
	}
}

func TestTransientClassifierUsesFallbackTier(t *testing.T) {
	var gotModel any
	server, classifierCalls, upstreamCalls := testServer(t,
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			gotModel = payload["model"]
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"hello"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotModel != "fallback-model" || rec.Header().Get("X-SMR-Classifier") != "fallback" {
		t.Fatalf("model=%v headers=%v", gotModel, rec.Header())
	}
	if classifierCalls.Load() != 2 || upstreamCalls.Load() != 1 {
		t.Fatalf("classifier=%d upstream=%d", classifierCalls.Load(), upstreamCalls.Load())
	}
}

func TestEmptyStateSkipsClassifierAndUsesFallback(t *testing.T) {
	var gotModel any
	server, classifierCalls, _ := testServer(t, classifierOK("easy", "miss", 1), func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		gotModel = payload["model"]
		_, _ = w.Write([]byte(`{}`))
	})
	body := `{"input":[{"role":"user","content":[{"type":"tool_result","content":"only output"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if classifierCalls.Load() != 0 || gotModel != "fallback-model" {
		t.Fatalf("classifier=%d model=%v", classifierCalls.Load(), gotModel)
	}
	if rec.Header().Get("X-SMR-Charged-VND") != "0" {
		t.Fatalf("expected zero charge, got %q", rec.Header().Get("X-SMR-Charged-VND"))
	}
}

func TestCountTokensIsFreeAndUsesClaudeEasy(t *testing.T) {
	var gotModel any
	var gotPath string
	server, classifierCalls, _ := testServer(t, classifierOK("hard", "miss", 5), func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		gotModel = payload["model"]
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if classifierCalls.Load() != 0 || gotModel != "claude-small" || gotPath != "/v1/messages/count_tokens" {
		t.Fatalf("classifier=%d model=%v path=%q", classifierCalls.Load(), gotModel, gotPath)
	}
	if rec.Header().Get("X-SMR-Charged-VND") != "0" || rec.Header().Get("X-SMR-Classifier") != "skipped" {
		t.Fatalf("headers=%v", rec.Header())
	}
}

func TestModelsIncludesSmartAlias(t *testing.T) {
	server, _, _ := testServer(t, classifierOK("easy", "miss", 1), func(w http.ResponseWriter, r *http.Request) {})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, model := range payload.Data {
		if model.ID == "smart" {
			found = true
		}
	}
	if !found {
		t.Fatalf("smart alias missing: %s", rec.Body.String())
	}
}
