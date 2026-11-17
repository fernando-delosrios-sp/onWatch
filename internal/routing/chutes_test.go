package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChutesClientFetchSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer chute-secret-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users/me/subscription_usage":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"used_usd":       1.75,
				"limit_usd":      5.0,
				"monthly_usage":  35.0,
				"monthly_limit":  100.0,
				"monthly_resets_at": "2026-12-01T00:00:00Z",
			})
		case "/users/me/quotas":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case "/users/me/quota_usage/me":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"used": 0})
		case "/idp/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"sub": "user-42"})
		case "/users/user-42":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "user-42"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewChutesClient(srv.URL, srv.Client())
	snap, err := client.FetchSnapshot(context.Background(), "chute-secret-123", 5.0, now)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	if snap.UsedUSD != 1.75 || snap.LimitUSD != 5.0 || snap.UsedPercent != 35.0 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.ResetsAt == nil || snap.ResetsAt.Year() != 2026 || snap.ResetsAt.Month() != 12 {
		t.Fatalf("unexpected resets_at: %v", snap.ResetsAt)
	}
}

func TestOpenRouterPaygClientAndGoogleMonitoring(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/key" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-or-payg" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"label":           "t3-code-main",
				"usage":           2.4,
				"usage_monthly":   2.4,
				"limit":           5.0,
				"limit_remaining": 2.6,
			},
		})
	}))
	defer orSrv.Close()

	orClient := NewOpenRouterKeyClient(orSrv.URL, orSrv.Client())
	orSnap, err := orClient.FetchPAYGSnapshot(context.Background(), "sk-or-payg", 5.0, now)
	if err != nil {
		t.Fatalf("FetchPAYGSnapshot: %v", err)
	}
	if orSnap.UsedUSD != 2.4 || orSnap.LimitUSD != 5.0 || orSnap.UsedPercent != 48.0 {
		t.Fatalf("unexpected openrouter snap: %+v", orSnap)
	}

	gSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ya29.test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"timeSeries": []map[string]interface{}{
				{"points": []map[string]interface{}{{"value": map[string]interface{}{"int64Value": "42"}}}},
				{"points": []map[string]interface{}{{"value": map[string]interface{}{"int64Value": "8"}}}},
			},
		})
	}))
	defer gSrv.Close()

	gClient := NewGoogleMonitoringClient(gSrv.URL, gSrv.Client())
	gSnap, err := gClient.FetchRequestMetrics(context.Background(), "my-proj", "ya29.test", now)
	if err != nil {
		t.Fatalf("FetchRequestMetrics: %v", err)
	}
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(gSnap.ExtraJSON), &extra); err != nil {
		t.Fatal(err)
	}
	if extra["requests_24h"] != float64(50) {
		t.Fatalf("expected requests_24h=50, got %v", extra["requests_24h"])
	}
}

func TestPollerSettingsMasking(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	poller := NewPoller(rs, nil, time.Minute)
	ck := "cpk_1234567890abcdef"
	gp := "gen-lang-client-01"
	if err := poller.UpdateProbeSettings(&ck, nil, &gp); err != nil {
		t.Fatal(err)
	}
	ps := poller.GetProbeSettings()
	if !ps.ChutesAPIKeyConfigured || ps.ChutesAPIKeyMasked != "cpk_...cdef" {
		t.Fatalf("unexpected masked chutes key: %+v", ps)
	}
	if ps.GoogleCloudProject != gp {
		t.Fatalf("expected google project %q, got %q", gp, ps.GoogleCloudProject)
	}
}
