package tracker

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

// ChutesTracker manages reset cycle detection and usage calculation for Chutes.
type ChutesTracker struct {
	store         *store.Store
	logger        *slog.Logger
	lastUsedUSD   float64
	hasLastValues bool
	onReset       func(quotaName string)
}

// ChutesSummary contains computed usage statistics for Chutes.
type ChutesSummary struct {
	QuotaType       string
	CurrentUsedUSD  float64
	LimitUSD        float64
	CurrentPercent  float64
	CurrentRate     float64
	CompletedCycles int
	AvgPerCycle     float64
	PeakCycle       float64
	TotalTracked    float64
	TrackingSince   time.Time
}

// NewChutesTracker creates a new ChutesTracker.
func NewChutesTracker(s *store.Store, logger *slog.Logger) *ChutesTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ChutesTracker{store: s, logger: logger}
}

// SetOnReset registers a callback invoked when a usage reset is detected.
func (t *ChutesTracker) SetOnReset(fn func(string)) {
	t.onReset = fn
}

// Process compares current snapshot with previous, detects resets, and updates cycles.
func (t *ChutesTracker) Process(snapshot *api.ChutesSnapshot) error {
	if snapshot == nil {
		return nil
	}
	quotaType := "credits"
	cycle, err := t.store.QueryActiveChutesCycle(quotaType)
	if err != nil {
		return fmt.Errorf("chutes tracker: query active cycle: %w", err)
	}
	if cycle == nil {
		if _, err := t.store.CreateChutesCycle(quotaType, snapshot.CapturedAt); err != nil {
			return fmt.Errorf("chutes tracker: create cycle: %w", err)
		}
		cycle, err = t.store.QueryActiveChutesCycle(quotaType)
		if err != nil || cycle == nil {
			return err
		}
	}

	if t.hasLastValues && snapshot.UsedUSD+0.001 < t.lastUsedUSD*0.5 {
		if err := t.store.CloseChutesCycle(cycle.ID, snapshot.CapturedAt, cycle.PeakUsage, cycle.TotalDelta); err != nil {
			return err
		}
		if _, err := t.store.CreateChutesCycle(quotaType, snapshot.CapturedAt); err != nil {
			return err
		}
		if t.onReset != nil {
			t.onReset(quotaType)
		}
	} else {
		peak := cycle.PeakUsage
		if snapshot.UsedUSD > peak {
			peak = snapshot.UsedUSD
		}
		delta := cycle.TotalDelta
		if t.hasLastValues && snapshot.UsedUSD > t.lastUsedUSD {
			delta += snapshot.UsedUSD - t.lastUsedUSD
		}
		_ = t.store.UpdateChutesCycle(cycle.ID, peak, delta)
	}

	t.lastUsedUSD = snapshot.UsedUSD
	t.hasLastValues = true
	return nil
}

// UsageSummary returns computed usage statistics for Chutes.
func (t *ChutesTracker) UsageSummary() (*ChutesSummary, error) {
	latest, err := t.store.QueryLatestChutes()
	if err != nil {
		return nil, err
	}
	history, err := t.store.QueryChutesCycleHistory("credits", 50)
	if err != nil {
		return nil, err
	}
	summary := &ChutesSummary{
		QuotaType:       "credits",
		CompletedCycles: len(history),
	}
	if latest != nil {
		summary.CurrentUsedUSD = latest.UsedUSD
		summary.LimitUSD = latest.LimitUSD
		summary.CurrentPercent = latest.UsedPercent
		summary.TrackingSince = latest.CapturedAt
	}
	var sum float64
	for _, c := range history {
		sum += c.TotalDelta
		if c.PeakUsage > summary.PeakCycle {
			summary.PeakCycle = c.PeakUsage
		}
	}
	summary.TotalTracked = sum
	if len(history) > 0 {
		summary.AvgPerCycle = sum / float64(len(history))
	}
	return summary, nil
}
