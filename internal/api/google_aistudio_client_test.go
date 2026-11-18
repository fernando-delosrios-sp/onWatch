package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGoogleAIStudioClient_FetchUsageAndModels(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "AIzaTestKey" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"models": [
				{
					"name": "models/gemini-2.5-pro",
					"displayName": "Gemini 2.5 Pro",
					"description": "Reasoning model",
					"inputTokenLimit": 1048576,
					"outputTokenLimit": 65536,
					"supportedGenerationMethods": ["generateContent", "countTokens"]
				},
				{
					"name": "models/embedding-001",
					"displayName": "Embedding 001",
					"supportedGenerationMethods": ["embedContent"]
				}
			]
		}`))
	}))
	defer apiSrv.Close()

	monSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gcp-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"timeSeries": [{"points": [{"value": {"int64Value": "42"}}]}]}`))
	}))
	defer monSrv.Close()

	client := NewGoogleAIStudioClient(
		"AIzaTestKey",
		"my-gcp-project",
		15.0,
		nil,
		WithGoogleAIStudioBaseURL(apiSrv.URL),
		WithGoogleAIStudioMonitoringURL(monSrv.URL),
		WithGoogleAIStudioAccessToken("gcp-token"),
	)

	snap, err := client.FetchUsage(context.Background())
	if err != nil {
		t.Fatalf("FetchUsage failed: %v", err)
	}
	if !snap.KeyValid || snap.ModelCount != 2 || snap.Requests24h != 42 || snap.CapUSD != 15.0 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	models, err := client.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels failed: %v", err)
	}
	if len(models) != 2 || models[0].ID != "gemini-2.5-pro" || models[0].ContextWindow != 1048576 {
		t.Fatalf("unexpected models: %+v", models)
	}
}
