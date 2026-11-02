package classifier

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifySuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing bearer key")
		}
		if r.Header.Get("Idempotency-Key") == "" {
			t.Fatal("missing idempotency key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"r1","tier":"easy","cache":"miss","charged_vnd":1,"balance_vnd":99,"input_tokens":10}`))
	}))
	defer server.Close()
	client := New(server.URL, "test-key", 2*time.Second, 1)
	result, err := client.Classify(context.Background(), "hello", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if result.Tier != "easy" || result.ChargedVND != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestClassifyRetriesTransientWithSameIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	var firstKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if firstKey == "" {
			firstKey = r.Header.Get("Idempotency-Key")
		} else if r.Header.Get("Idempotency-Key") != firstKey {
			t.Fatal("idempotency key changed across retry")
		}
		if calls.Load() == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"tier":"hard","cache":"miss","charged_vnd":2}`))
	}))
	defer server.Close()
	client := New(server.URL, "test-key", 2*time.Second, 1)
	result, err := client.Classify(context.Background(), "hello", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if result.Tier != "hard" || calls.Load() != 2 {
		t.Fatalf("tier=%q calls=%d", result.Tier, calls.Load())
	}
}

func TestClassifyPaymentRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"code":"insufficient_balance","topup_url":"https://llm.acmeai.vn/topup"}}`))
	}))
	defer server.Close()
	client := New(server.URL, "test-key", 2*time.Second, 0)
	_, err := client.Classify(context.Background(), "hello", "codex")
	if !errors.Is(err, ErrPaymentRequired) {
		t.Fatalf("expected payment error, got %v", err)
	}
}

func TestClassifyUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := New(server.URL, "test-key", 2*time.Second, 1)
	_, err := client.Classify(context.Background(), "hello", "codex")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestClassifyUnknownTierIsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tier":"impossible","cache":"miss"}`))
	}))
	defer server.Close()
	client := New(server.URL, "test-key", 2*time.Second, 0)
	_, err := client.Classify(context.Background(), "hello", "codex")
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("expected transient, got %v", err)
	}
}
