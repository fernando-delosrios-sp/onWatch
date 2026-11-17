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

	// Set explicit expiry dates on two pools in bucket 3 (monthly_no_cooldown on pace) to verify soonest-expiring wins.
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
			// antigravity-flash on pace (bucket 2)
			"antigravity-flash": {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.10, ElapsedShare: 0.50},
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
			"antigravity-flash":  {ProbeStatus: "ok", HasQuotaUsage: true, UsedShare: 0.15, ElapsedShare: 0.50},
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
	//    - workhorse & lightweight pick nvidia-free (bucket 1, pi)
	//    - frontier has no nvidia-free model, so it skips bucket 1 and picks antigravity-3p (bucket 2, weight 100, prefers pi over native antigravity)
	resp, err := eng.SelectRoute(context.Background(), []string{"pi", "antigravity", "cursor", "claudeAgent"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Tiers[TierWorkhorse].Pool != "nvidia-free" || resp.Tiers[TierWorkhorse].Overlay != "@preset/code-nvidia" {
		t.Fatalf("unexpected workhorse route: %+v", resp.Tiers[TierWorkhorse])
	}
	if resp.Tiers[TierFrontier].Pool != "antigravity-3p" || resp.Tiers[TierFrontier].Harness != "pi" {
		t.Fatalf("expected frontier antigravity-3p with pi preference, got %+v", resp.Tiers[TierFrontier])
	}

	// 2. Cursor-only harness filters out pi-only and antigravity routes -> frontier picks cursor
	cursorResp, err := eng.SelectRoute(context.Background(), []string{"cursor"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if cursorResp.Tiers[TierFrontier].Pool != "cursor" || cursorResp.Tiers[TierFrontier].Harness != "cursor" {
		t.Fatalf("expected cursor-only frontier route, got %+v", cursorResp.Tiers[TierFrontier])
	}

	// 3. OverridePiPreference on native antigravity frontier route flips antigravity-3p harness from pi -> antigravity
	models, _ := rs.ListModels()
	for _, m := range models {
		if m.Tier == TierFrontier && m.PoolID == "antigravity-3p" && m.Harness == "antigravity" {
			m.OverridePiPreference = true
			if _, err := rs.UpsertModel(m); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	ovrResp, err := eng.SelectRoute(context.Background(), []string{"pi", "antigravity"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if ovrResp.Tiers[TierFrontier].Harness != "antigravity" {
		t.Fatalf("expected OverridePiPreference to select antigravity harness, got %s", ovrResp.Tiers[TierFrontier].Harness)
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
