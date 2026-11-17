package routing

import (
	"context"
	"testing"
	"time"
)

func TestFeedbackCooldownFreeGatingAndGoogleSpendTracking(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	probes := mapProbeReader{
		readings: map[string]ProbeReading{
			"nvidia-free":      {ProbeStatus: "ok"},
			"google-ai-studio": {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0, ElapsedShare: 0.55},
		},
	}
	eng := NewEngine(rs, probes)

	// 1. Two startup failures on nvidia-free should gate the free pool
	for i := 0; i < 2; i++ {
		_, _, err := eng.ApplyFeedback(context.Background(), FeedbackInput{
			Pool:    "nvidia-free",
			Harness: "pi",
			Model:   "nvidia/nemotron-3-ultra-550b-a55b:free",
			Event:   "startup_failure",
			Outcome: "error",
			Error:   "connection refused",
			Issue:   "ENG-12",
		}, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}

	resp, err := eng.SelectRoute(context.Background(), []string{"pi"}, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if resp.TierSelections[TierWorkhorse].Pool == "nvidia-free" {
		t.Fatalf("expected nvidia-free to be gated after consecutive startup failures")
	}

	// 2. Clear cooldown -> nvidia-free returns to #1
	if _, err := eng.ClearCooldown("nvidia-free", now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	resp2, err := eng.SelectRoute(context.Background(), []string{"pi"}, now.Add(11*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if resp2.TierSelections[TierWorkhorse].Pool != "nvidia-free" {
		t.Fatalf("expected nvidia-free restored after ClearCooldown, got %s", resp2.TierSelections[TierWorkhorse].Pool)
	}

	// 3. Token-estimated + explicit cost_usd feedback accumulates real spend on google-ai-studio ($15 cap)
	// google/gemini-4-carbon has InputCostPer1M=1.25, OutputCostPer1M=10.0
	_, _, err = eng.ApplyFeedback(context.Background(), FeedbackInput{
		Pool:             "google-ai-studio",
		Model:            "google/gemini-4-carbon",
		Outcome:          "ok",
		PromptTokens:     1_000_000,
		CompletionTokens: 500_000,
		TTFTMs:           420,
	}, now.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	pGoogle, _ := rs.GetPool("google-ai-studio")
	// 1M * 1.25 + 0.5M * 10.0 = 6.25
	if pGoogle.SpentUSD < 6.24 || pGoogle.SpentUSD > 6.26 {
		t.Fatalf("expected google-ai-studio spent ~6.25, got %v", pGoogle.SpentUSD)
	}

	// Exhaust remaining $15 voucher via explicit cost_usd
	_, _, err = eng.ApplyFeedback(context.Background(), FeedbackInput{
		Pool:    "google-ai-studio",
		Outcome: "ok",
		CostUSD: 9.0,
	}, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	engReal := NewEngine(rs, NewSQLiteProbeReader(rs.db))
	ranked, err := engReal.RankPools(context.Background(), now.Add(31*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range ranked {
		if st.Pool.ID == "google-ai-studio" {
			if st.Eligible {
				t.Fatalf("expected google-ai-studio ineligible after $15 voucher cap reached (spent=%.2f)", st.EffectiveSpentUSD)
			}
		}
	}

	// 4. 429 cooldown on chutes
	_, h429, err := eng.ApplyFeedback(context.Background(), FeedbackInput{
		Pool:       "chutes",
		StatusCode: 429,
		Outcome:    "rate_limit",
		Error:      "too many requests",
	}, now.Add(40*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if h429.CooldownUntil == nil || !h429.CooldownUntil.After(now.Add(40*time.Second)) {
		t.Fatalf("expected 429 to set future cooldown")
	}

	// 5. /bad demotion on antigravity-3p
	_, hBad, err := eng.ApplyFeedback(context.Background(), FeedbackInput{
		Pool:    "antigravity-3p",
		Event:   "/bad",
		Outcome: "bad",
		Note:    "hallucinated imports",
	}, now.Add(50*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if hBad.HealthScore >= 70 || hBad.CooldownUntil == nil {
		t.Fatalf("expected /bad demotion to reduce health score and set cooldown, got %+v", hBad)
	}
}
