package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","classifier":{"configured":true,"last_ok":true}}`))
	}))
	defer server.Close()
	health, err := HealthCheck(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" || !health.Classifier.Configured || !health.Classifier.LastOK {
		t.Fatalf("unexpected health: %#v", health)
	}
}
