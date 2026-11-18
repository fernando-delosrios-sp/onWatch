package store

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

func TestNewProvidersStores(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:) failed: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Second)

	// Chutes
	if _, err := s.InsertChutesSnapshot(&api.ChutesSnapshot{
		CapturedAt: now, UsedUSD: 1.5, LimitUSD: 5.0, UsedPercent: 30, BalanceUSD: 3.5, ModelCount: 12,
	}); err != nil {
		t.Fatalf("InsertChutesSnapshot: %v", err)
	}
	ch, err := s.QueryLatestChutes()
	if err != nil || ch == nil || ch.UsedUSD != 1.5 || ch.ModelCount != 12 {
		t.Fatalf("QueryLatestChutes unexpected: %+v, err=%v", ch, err)
	}
	chList, err := s.QueryChutesRange(now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || len(chList) != 1 {
		t.Fatalf("QueryChutesRange unexpected: %d, err=%v", len(chList), err)
	}

	// NVIDIA
	if _, err := s.InsertNvidiaSnapshot(&api.NvidiaSnapshot{
		CapturedAt: now, ModelCount: 45, RateLimitRemaining: 38, RateLimitLimit: 40, KeyValid: true,
	}); err != nil {
		t.Fatalf("InsertNvidiaSnapshot: %v", err)
	}
	nv, err := s.QueryLatestNvidia()
	if err != nil || nv == nil || nv.ModelCount != 45 || !nv.KeyValid {
		t.Fatalf("QueryLatestNvidia unexpected: %+v, err=%v", nv, err)
	}

	// Google AI Studio
	if _, err := s.InsertGoogleAIStudioSnapshot(&api.GoogleAIStudioSnapshot{
		CapturedAt: now, Project: "proj-1", Requests24h: 100, CapUSD: 15, SpentUSD: 2.5, ModelCount: 18, KeyValid: true,
	}); err != nil {
		t.Fatalf("InsertGoogleAIStudioSnapshot: %v", err)
	}
	ga, err := s.QueryLatestGoogleAIStudio()
	if err != nil || ga == nil || ga.Project != "proj-1" || ga.Requests24h != 100 || ga.CapUSD != 15 {
		t.Fatalf("QueryLatestGoogleAIStudio unexpected: %+v, err=%v", ga, err)
	}
}
