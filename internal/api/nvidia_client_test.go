package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNvidiaClient_FetchUsageAndModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer nvapi-test-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining-Requests", "39")
		w.Header().Set("X-RateLimit-Limit-Requests", "40")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "nvidia/llama-3.1-nemotron-70b-instruct", "object": "model", "owned_by": "nvidia"},
				{"id": "deepseek-ai/deepseek-r1", "object": "model", "owned_by": "deepseek-ai"}
			]
		}`))
	}))
	defer srv.Close()

	client := NewNvidiaClient("nvapi-test-123", nil, WithNvidiaBaseURL(srv.URL))
	snap, err := client.FetchUsage(context.Background())
	if err != nil {
		t.Fatalf("FetchUsage failed: %v", err)
	}
	if snap.ModelCount != 2 || snap.RateLimitRemaining != 39 || snap.RateLimitLimit != 40 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	models, err := client.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels failed: %v", err)
	}
	if len(models) != 2 || models[0].Provider != "nvidia" {
		t.Fatalf("unexpected models: %+v", models)
	}
}
