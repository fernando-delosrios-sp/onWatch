package web

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/config"
	"github.com/onllm-dev/onwatch/v2/internal/routing"
)

func TestOpenRouterAccountCreditsFallback(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		limit                               *float64
		account                             *api.OpenRouterAccountCredits
		routingCap                          float64
		wantSource                          string
		wantUsage, wantLimit, wantRemaining float64
		wantCap                             float64
		wantPercent                         float64
	}{
		{"account credits when no limit", nil, &api.OpenRouterAccountCredits{TotalCredits: 100, TotalUsage: 25}, 0, "account", 25, 100, 75, 0, 100},
		{"account credits prioritized over key limit", floatPointerOpenRouter(50), &api.OpenRouterAccountCredits{TotalCredits: 100, TotalUsage: 25}, 0, "account", 25, 100, 75, 50, 100},
		{"account credits with routing fallback cap", nil, &api.OpenRouterAccountCredits{TotalCredits: 30, TotalUsage: 26}, 5, "account", 26, 30, 4, 5, 80},
		{"account credits with routing $5 cap overrides key limit", floatPointerOpenRouter(15), &api.OpenRouterAccountCredits{TotalCredits: 30, TotalUsage: 18.32}, 5, "account", 18.32, 30, 11.68, 5, 100},
		{"key limit when account credits nil", floatPointerOpenRouter(50), nil, 0, "", 10, 50, 40, 50, 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s, _ := newSharedMemoryMenubarHandler(t, &config.Config{OpenRouterAPIKey: "test"})
			if tc.routingCap > 0 {
				rs, err := routing.NewStore(s.DB())
				if err != nil {
					t.Fatal(err)
				}
				p, err := rs.GetPool("openrouter-payg")
				if err != nil {
					t.Fatal(err)
				}
				p.CapUSD = tc.routingCap
				if err := rs.UpsertPool(*p); err != nil {
					t.Fatal(err)
				}
			}
			limitRem := floatPointerOpenRouter(40)
			if tc.account != nil {
				// do not inject a LimitRemaining that would shadow account remaining for cap-available calc in account tests
				limitRem = nil
			}
			_, err := s.InsertOpenRouterSnapshot(&api.OpenRouterSnapshot{CapturedAt: time.Now().UTC(), Usage: 10, Limit: tc.limit, LimitRemaining: limitRem, AccountCredits: tc.account})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.Current(rec, httptest.NewRequest(http.MethodGet, "/api/current?provider=openrouter", nil))
			var payload struct {
				Credits struct {
					Source           string  `json:"source"`
					Usage            float64 `json:"usage"`
					Limit            float64 `json:"limit"`
					Remaining        float64 `json:"remaining"`
					Cap              float64 `json:"cap"`
					Percent          float64 `json:"percent"`
					RemainingPercent float64 `json:"remainingPercent"`
				} `json:"credits"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			got := payload.Credits
			// "percent" is now utilization (high bad); the available/good value is in remainingPercent (or cardPercent)
			good := got.RemainingPercent
			if good == 0 {
				good = got.Percent // fallback if not set
			}
			if got.Source != tc.wantSource || got.Usage != tc.wantUsage || got.Limit != tc.wantLimit || math.Abs(got.Remaining-tc.wantRemaining) > 0.01 || math.Abs(got.Cap-tc.wantCap) > 0.01 || math.Abs(good-tc.wantPercent) > 0.01 {
				t.Fatalf("got %+v want cap=%.2f good percent=%.1f (util %.1f)", got, tc.wantCap, tc.wantPercent, got.Percent)
			}
		})
	}
}

func TestChutesCreditsCapAndAvailableSemantics(t *testing.T) {
	for _, tc := range []struct {
		name           string
		routingCap     float64
		snapLimit      float64
		snapUsed       float64
		snapBalance    float64
		wantLimit      float64
		wantUsed       float64
		wantAvailPct   float64
		wantStatus     string
	}{
		{
			name:         "routing $5 cap with $4.9635 balance",
			routingCap:   5.0,
			snapBalance:  4.9635,
			wantLimit:    5.0,
			wantUsed:     0.0365,
			wantAvailPct: 99.27,
			wantStatus:   "healthy",
		},
		{
			name:         "routing $5 cap with balance exceeding cap reports 100%",
			routingCap:   5.0,
			snapBalance:  11.94,
			wantLimit:    5.0,
			wantUsed:     0.0,
			wantAvailPct: 100.0,
			wantStatus:   "healthy",
		},
		{
			name:         "routing $5 cap critically low balance",
			routingCap:   5.0,
			snapBalance:  0.20,
			wantLimit:    5.0,
			wantUsed:     4.80,
			wantAvailPct: 4.0,
			wantStatus:   "critical",
		},
		{
			name:         "uncapped with positive balance reports 100%",
			routingCap:   0,
			snapBalance:  4.9635,
			wantLimit:    0,
			wantUsed:     0,
			wantAvailPct: 100.0,
			wantStatus:   "healthy",
		},
		{
			name:         "uncapped with zero balance reports 0%",
			routingCap:   0,
			snapBalance:  0,
			wantLimit:    0,
			wantUsed:     0,
			wantAvailPct: 0.0,
			wantStatus:   "critical",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s, _ := newSharedMemoryMenubarHandler(t, &config.Config{ChutesAPIKey: "cpk_test"})
			rs, err := routing.NewStore(s.DB())
			if err != nil {
				t.Fatal(err)
			}
			p, err := rs.GetPool("chutes")
			if err != nil {
				t.Fatal(err)
			}
			p.CapUSD = tc.routingCap
			if err := rs.UpsertPool(*p); err != nil {
				t.Fatal(err)
			}
			_, err = s.InsertChutesSnapshot(&api.ChutesSnapshot{
				CapturedAt: time.Now().UTC(),
				UsedUSD:    tc.snapUsed,
				LimitUSD:   tc.snapLimit,
				BalanceUSD: tc.snapBalance,
				ModelCount: 19,
			})
			if err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			h.Current(rec, httptest.NewRequest(http.MethodGet, "/api/current?provider=chutes", nil))
			var payload struct {
				Credits struct {
					UsedUSD          float64 `json:"usedUsd"`
					LimitUSD         float64 `json:"limitUsd"`
					Percent          float64 `json:"percent"`
					RemainingPercent float64 `json:"remainingPercent"`
					BalanceUSD       float64 `json:"balanceUsd"`
					Status           string  `json:"status"`
				} `json:"credits"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			got := payload.Credits
			if math.Abs(got.LimitUSD-tc.wantLimit) > 0.001 ||
				math.Abs(got.UsedUSD-tc.wantUsed) > 0.001 ||
				math.Abs(got.RemainingPercent-tc.wantAvailPct) > 0.02 ||
				got.Status != tc.wantStatus {
				t.Fatalf("got %+v, want limit=%.2f used=%.4f avail=%.2f status=%s", got, tc.wantLimit, tc.wantUsed, tc.wantAvailPct, tc.wantStatus)
			}
		})
	}
}

func floatPointerOpenRouter(v float64) *float64 { return &v }
