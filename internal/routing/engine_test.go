package routing

import (
	"context"
	"testing"
	"time"
)

type mapProbeReader struct {
	readings map[string]ProbeReading
}

func (m mapProbeReader) ReadProbe(_ context.Context, p Pool, _ time.Time) ProbeReading {
	if r, ok := m.readings[p.ID]; ok {
		return r
	}
	return ProbeReading{ProbeStatus: "ok"}
}

func TestWaterfallSixBucketsAndSoonestExpiry(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	// Set explicit expiry dates on two pools in bucket 3 (allowance on pace) to verify soonest-expiring wins.
	expSoon := now.Add(5 * 24 * time.Hour)
	expLater := now.Add(15 * 24 * time.Hour)

	pGoogle, _ := rs.GetPool("google-ai-studio")
	pGoogle.ExpiresAt = &expSoon
	pGoogle.Weight = 50 // lower weight, but soonest expiry must still win inside same bucket!
	_ = rs.UpsertPool(*pGoogle)

	pCursor, _ := rs.GetPool("cursor")
	pCursor.ExpiresAt = &expLater
	pCursor.Weight = 95
	_ = rs.UpsertPool(*pCursor)

	probes := mapProbeReader{
		readings: map[string]ProbeReading{
			"nvidia-free": {ProbeStatus: "ok"},
			// antigravity-3p on pace (bucket 2)
			"antigravity-3p": {
				ProbeStatus:   "ok",
				HasQuotaUsage: true,
				UsedShare:     0.20,
				ElapsedShare:  0.50,
				Windows:       []QuotaWindow{{Name: "3p-weekly", UsedShare: 0.20, ElapsedShare: 0.50}},
			},
			// antigravity-gemini ahead of pace (bucket 4)
			"antigravity-gemini": {
				ProbeStatus:   "ok",
				HasQuotaUsage: true,
				UsedShare:     0.85,
				ElapsedShare:  0.40,
				Windows:       []QuotaWindow{{Name: "gemini-weekly", UsedShare: 0.85, ElapsedShare: 0.40}},
			},
			// xai ahead of pace (bucket 4)
			"xai": {
				ProbeStatus:   "ok",
				HasQuotaUsage: true,
				UsedShare:     0.90,
				ElapsedShare:  0.50,
				Windows:       []QuotaWindow{{Name: "credits", UsedShare: 0.90, ElapsedShare: 0.50}},
			},
			// claude & codex on pace (bucket 2)
			"claude": {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.10, ElapsedShare: 0.50},
			"codex":  {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.10, ElapsedShare: 0.50},
			// antigravity-argon on pace (bucket 2)
			"antigravity-argon": {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.10, ElapsedShare: 0.50},
			// cursor on pace (bucket 3, later expiry)
			"cursor": {ProbeStatus: "ok", HasQuotaUsage: true, HasSpendData: true, SpentUSD: 100, LimitUSD: 500, UsedShare: 0.20, ElapsedShare: 0.55},
			// google-ai-studio on pace (bucket 3, soonest expiry)
			"google-ai-studio": {ProbeStatus: "ok", HasQuotaUsage: true, HasSpendData: true, SpentUSD: 2, LimitUSD: 15, UsedShare: 0.13, ElapsedShare: 0.55},
			// chutes & openrouter-payg (bucket 6)
			"chutes":          {ProbeStatus: "ok", HasQuotaUsage: true, HasSpendData: true, SpentUSD: 1, LimitUSD: 5, UsedShare: 0.20, ElapsedShare: 0.55},
			"openrouter-payg": {ProbeStatus: "ok", HasQuotaUsage: true, HasSpendData: true, SpentUSD: 1, LimitUSD: 5, UsedShare: 0.20, ElapsedShare: 0.55},
		},
	}

	eng := NewEngine(rs, probes)
	ranked, err := eng.RankPools(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}

	// Verify monotonic bucket ordering 1 -> 2 -> 3 -> 4 -> 6
	prevBucket := 0
	pos := map[string]int{}
	for i, st := range ranked {
		if !st.Eligible {
			t.Fatalf("expected all pools eligible, got %s ineligible: %s", st.Pool.ID, st.IneligibleReason)
		}
		if st.WaterfallBucket < prevBucket {
			t.Fatalf("bucket order violated at %s: %d < %d", st.Pool.ID, st.WaterfallBucket, prevBucket)
		}
		prevBucket = st.WaterfallBucket
		pos[st.Pool.ID] = i
	}

	if pos["nvidia-free"] != 0 {
		t.Fatalf("expected nvidia-free rank #1, got %d", pos["nvidia-free"]+1)
	}
	if pos["google-ai-studio"] >= pos["cursor"] {
		t.Fatalf("expected google-ai-studio (soonest expiry) before cursor, got %d vs %d", pos["google-ai-studio"], pos["cursor"])
	}
	if pos["cursor"] >= pos["antigravity-gemini"] {
		t.Fatalf("expected bucket 3 (cursor) before bucket 4 (antigravity-gemini)")
	}
	if pos["antigravity-gemini"] >= pos["chutes"] {
		t.Fatalf("expected bucket 4 (antigravity-gemini) before bucket 6 (chutes)")
	}
}

func TestSelectRoutePerTierPiPreferenceAndOverride(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	probes := mapProbeReader{
		readings: map[string]ProbeReading{
			"nvidia-free":        {ProbeStatus: "ok"},
			"antigravity-3p":     {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.10, ElapsedShare: 0.50},
			"antigravity-gemini": {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.15, ElapsedShare: 0.50},
			"antigravity-argon":  {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.15, ElapsedShare: 0.50},
			"xai":                {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.15, ElapsedShare: 0.50},
			"claude":             {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.15, ElapsedShare: 0.50},
			"codex":              {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.15, ElapsedShare: 0.50},
			"cursor":             {ProbeStatus: "ok", HasQuotaUsage: true, HasSpendData: true, SpentUSD: 100, LimitUSD: 500, UsedShare: 0.20, ElapsedShare: 0.55},
			"google-ai-studio":   {ProbeStatus: "ok", HasQuotaUsage: true, HasSpendData: true, SpentUSD: 1, LimitUSD: 15, UsedShare: 0.06, ElapsedShare: 0.55},
			"chutes":             {ProbeStatus: "ok", HasQuotaUsage: true, HasSpendData: true, SpentUSD: 0, LimitUSD: 5, UsedShare: 0, ElapsedShare: 0.55},
			"openrouter-payg":    {ProbeStatus: "ok", HasQuotaUsage: true, HasSpendData: true, SpentUSD: 0, LimitUSD: 5, UsedShare: 0, ElapsedShare: 0.55},
		},
	}

	eng := NewEngine(rs, probes)

	// 1. Default pi + native harnesses:
	//    - lightweight picks nvidia-free (bucket 1, pi)
	//    - workhorse picks antigravity-3p (bucket 2 subscription_on_pace, weight 100, claude-sonnet-5-5)
	//    - frontier skips bucket 1 and picks antigravity-3p (bucket 2, weight 100, native antigravity)
	resp, err := eng.SelectRoute(context.Background(), []string{"pi", "antigravity", "cursor", "claudeAgent"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if resp.TierSelections[TierLightweight].Pool != "nvidia-free" || resp.TierSelections[TierLightweight].Overlay != "" {
		t.Fatalf("unexpected lightweight route: %+v", resp.TierSelections[TierLightweight])
	}
	if resp.TierSelections[TierWorkhorse].Pool != "antigravity-3p" || resp.TierSelections[TierWorkhorse].Harness != "antigravity" {
		t.Fatalf("unexpected workhorse route: %+v", resp.TierSelections[TierWorkhorse])
	}
	if resp.TierSelections[TierFrontier].Pool != "antigravity-3p" || resp.TierSelections[TierFrontier].Harness != "antigravity" {
		t.Fatalf("expected frontier antigravity-3p with native antigravity harness, got %+v", resp.TierSelections[TierFrontier])
	}
	if len(resp.Tiers[TierFrontier]) != 2 || resp.Tiers[TierFrontier][0].Status != "ready" {
		t.Fatalf("expected 2 ready frontier candidates, got %+v", resp.Tiers[TierFrontier])
	}

	// 1b. Pi-only harness: skips antigravity-* pools (which are not available to pi) and picks xai (bucket 2 on-pace)
	piResp, err := eng.SelectRoute(context.Background(), []string{"pi"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if piResp.TierSelections[TierFrontier].Pool != "xai" || piResp.TierSelections[TierFrontier].Harness != "pi" {
		t.Fatalf("expected pi-only frontier route on xai (subscription on-pace), got %+v", piResp.TierSelections[TierFrontier])
	}

	// 2. Cursor-only harness filters out pi-only and antigravity routes -> frontier picks cursor
	cursorResp, err := eng.SelectRoute(context.Background(), []string{"cursor"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if cursorResp.TierSelections[TierFrontier].Pool != "cursor" || cursorResp.TierSelections[TierFrontier].Harness != "cursor" {
		t.Fatalf("expected cursor-only frontier route, got %+v", cursorResp.TierSelections[TierFrontier])
	}
}

func TestSelectRouteStickyPoolExcludeAndTier(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	probes := mapProbeReader{
		readings: map[string]ProbeReading{
			"nvidia-free":      {ProbeStatus: "ok"},
			"google-ai-studio": {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.10, ElapsedShare: 0.50},
			"xai":              {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.90, ElapsedShare: 0.40},
		},
	}
	eng := NewEngine(rs, probes)

	// StickyPool honors an eligible pool and echoes StickyPool; excludes omit matching @preset or model IDs.
	resp, err := eng.SelectRouteWithOptions(context.Background(), RouteOptions{
		Harnesses:  []string{"pi"},
		Tier:       TierWorkhorse,
		StickyPool: "google-ai-studio",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StickyPool != "google-ai-studio" {
		t.Fatalf("expected stickyPool google-ai-studio, got %q", resp.StickyPool)
	}
	if len(resp.Tiers) != 1 || len(resp.Tiers[TierWorkhorse]) < 2 {
		t.Fatalf("expected workhorse candidates for google-ai-studio, got %+v", resp.Tiers)
	}
	if resp.Tiers[TierWorkhorse][0].ID != "openrouter/google/gemini-4-hydrogen" {
		t.Fatalf("expected openrouter/google/gemini-4-hydrogen first candidate, got %+v", resp.Tiers[TierWorkhorse][0])
	}

	// Excluding openrouter/nvidia/nemotron-3-ultra-550b-a55b:free skips nvidia-free on workhorse.
	exclResp, err := eng.SelectRouteWithOptions(context.Background(), RouteOptions{
		Harnesses: []string{"pi"},
		Exclude:   []string{"openrouter/nvidia/nemotron-3-ultra-550b-a55b:free"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if exclResp.TierSelections[TierWorkhorse].Pool == "nvidia-free" {
		t.Fatalf("expected nvidia-free excluded when openrouter/nvidia/nemotron-3-ultra-550b-a55b:free is excluded")
	}

	// Ahead-of-pace sticky pool is still eligible and gets status="ahead".
	aheadResp, err := eng.SelectRouteWithOptions(context.Background(), RouteOptions{
		Harnesses:  []string{"pi"},
		Tier:       TierFrontier,
		StickyPool: "xai",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if aheadResp.Tiers[TierFrontier][0].Status != "ahead" {
		t.Fatalf("expected status ahead for xai bucket 4, got %q", aheadResp.Tiers[TierFrontier][0].Status)
	}
}

func TestReserveEnforcement(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	pCur, _ := rs.GetPool("cursor")
	pCur.CapUSD = 500
	pCur.ReserveUSD = 120
	_ = rs.UpsertPool(*pCur)

	eng := NewEngine(rs, mapProbeReader{
		readings: map[string]ProbeReading{
			// 390 spent + 120 reserve > 500 cap => reserve reached!
			"cursor": {ProbeStatus: "ok", HasQuotaUsage: true, HasSpendData: true, SpentUSD: 390, LimitUSD: 500, UsedShare: 0.78, ElapsedShare: 0.55},
		},
	})
	ranked, err := eng.RankPools(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range ranked {
		if st.Pool.ID == "cursor" {
			if st.Eligible {
				t.Fatalf("expected cursor ineligible when reserve reached")
			}
			return
		}
	}
	t.Fatal("cursor pool not found")
}

func TestExcludeFreeModelsAndPools(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	// Re-enable a free model inside google-ai-studio to verify ExcludeFree strips per-model free routes too.
	_, err := rs.UpsertModel(RouteModel{
		Tier:     TierLightweight,
		PoolID:   "google-ai-studio",
		Harness:  "pi",
		Provider: "google-ai-studio",
		Overlay:  "@preset/code-google",
		ModelID:  "google/gemma-4-31b-it:free",
		Billing:  "free",
		Priority: 90,
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	probes := mapProbeReader{
		readings: map[string]ProbeReading{
			"nvidia-free":      {ProbeStatus: "ok"},
			"xai":              {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.10, ElapsedShare: 0.50},
			"google-ai-studio": {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.10, ElapsedShare: 0.50},
		},
	}
	eng := NewEngine(rs, probes)

	// Without ExcludeFree, nvidia-free wins lightweight & workhorse.
	defaultResp, err := eng.SelectRouteWithOptions(context.Background(), RouteOptions{
		Harnesses: []string{"pi"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if defaultResp.TierSelections[TierLightweight].Pool != "nvidia-free" {
		t.Fatalf("expected nvidia-free without ExcludeFree, got %s", defaultResp.TierSelections[TierLightweight].Pool)
	}

	// With ExcludeFree, nvidia-free and free models are omitted across all tiers (even if stickyPool="nvidia-free").
	paidResp, err := eng.SelectRouteWithOptions(context.Background(), RouteOptions{
		Harnesses:   []string{"pi"},
		StickyPool:  "nvidia-free",
		ExcludeFree: true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if paidResp.StickyPool == "nvidia-free" {
		t.Fatalf("expected nvidia-free sticky pool not honored when ExcludeFree is true")
	}
	for _, tier := range AllTiers {
		sel := paidResp.TierSelections[tier]
		if sel.Pool == "nvidia-free" || sel.Category == CategoryFree {
			t.Fatalf("tier %s picked free pool %s when ExcludeFree is true", tier, sel.Pool)
		}
		for _, c := range paidResp.Tiers[tier] {
			if c.Billing == "free" || c.Overlay == "@preset/code-nvidia" {
				t.Fatalf("tier %s returned free candidate %+v", tier, c)
			}
			for _, u := range c.UnderlyingModels {
				if u == "google/gemma-4-31b-it:free" {
					t.Fatalf("tier %s retained free underlying model %s", tier, u)
				}
			}
		}
		for _, fb := range sel.Fallbacks {
			if fb.Pool == "nvidia-free" || fb.Category == CategoryFree {
				t.Fatalf("tier %s fallback included free pool %s", tier, fb.Pool)
			}
			for _, c := range fb.Candidates {
				if c.Billing == "free" {
					t.Fatalf("tier %s fallback included free candidate %+v", tier, c)
				}
			}
		}
	}
}

func TestInnerWindowPacing(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	// Test: Antigravity has weekly window on pace (20% used, 50% elapsed) but 5h inner window ahead of pace (90% used, 20% elapsed).
	// Engine must detect inner window pacing and mark OnPace = false, moving pool from bucket 2 to bucket 4.
	pAgy, _ := rs.GetPool("antigravity-gemini")
	status := EvaluatePoolStatus(*pAgy, PoolHealth{HealthScore: 100}, ProbeReading{
		ProbeStatus:   "ok",
		HasQuotaUsage: true,
		UsedShare:     0.20,
		ElapsedShare:  0.50,
		Windows: []QuotaWindow{
			{Name: "gemini-5h", UsedShare: 0.90, ElapsedShare: 0.20, WindowHours: 5},
			{Name: "gemini-weekly", UsedShare: 0.20, ElapsedShare: 0.50, WindowHours: 168},
		},
	}, now)

	if status.OnPace {
		t.Fatalf("expected pool to be ahead of pace due to 5h inner window, but got OnPace=true")
	}
	if status.WaterfallBucket != 4 {
		t.Fatalf("expected bucket 4 (subscription_ahead), got %d (%s)", status.WaterfallBucket, status.BucketLabel)
	}
}

