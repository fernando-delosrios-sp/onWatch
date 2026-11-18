package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChutesClient_FetchUsageAndModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cpk_test_123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users/me/subscription_usage":
			_, _ = w.Write([]byte(`{"monthly_usage_usd": 1.25, "monthly_limit_usd": 5.0, "monthly_used_pct": 25.0, "resets_at": "2026-12-01T00:00:00Z"}`))
		case "/users/me/quotas":
			_, _ = w.Write([]byte(`{"quotas": [{"used": 1.25, "limit": 5.0}]}`))
		case "/users/me/quota_usage/me":
			_, _ = w.Write([]byte(`{"used_usd": 1.25, "limit_usd": 5.0}`))
		case "/idp/userinfo":
			_, _ = w.Write([]byte(`{"sub": "user-123"}`))
		case "/users/user-123":
			_, _ = w.Write([]byte(`{"balance": 3.75}`))
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data": [{"id": "deepseek-ai/DeepSeek-V3", "owned_by": "deepseek-ai", "context_length": 131072, "max_output_length": 16384}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewChutesClient("cpk_test_123", nil, WithChutesBaseURL(srv.URL), WithChutesModelsURL(srv.URL+"/v1/models"))
	snap, err := client.FetchUsage(context.Background())
	if err != nil {
		t.Fatalf("FetchUsage failed: %v", err)
	}
	if snap.UsedUSD != 1.25 || snap.LimitUSD != 5.0 || snap.UsedPercent != 25.0 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.ResetsAt == nil {
		t.Fatalf("expected ResetsAt to be parsed")
	}

	models, err := client.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels failed: %v", err)
	}
	if len(models) != 1 || models[0].ID != "deepseek-ai/DeepSeek-V3" || models[0].ContextWindow != 131072 {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestChutesClient_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := NewChutesClient("bad-key", nil, WithChutesBaseURL(srv.URL))
	if _, err := client.FetchUsage(context.Background()); err == nil {
		t.Fatalf("expected error on unauthorized")
	}
}
