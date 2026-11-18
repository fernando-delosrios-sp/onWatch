package tracker

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

// GoogleAIStudioTracker manages cycle tracking and summary calculation for Google AI Studio.
type GoogleAIStudioTracker struct {
	store         *store.Store
	logger        *slog.Logger
	lastSpentUSD  float64
	hasLastValues bool
	onReset       func(quotaName string)
}

// GoogleAIStudioSummary contains computed usage statistics for Google AI Studio.
type GoogleAIStudioSummary struct {
	QuotaType       string
	Project         string
	Requests24h     int64
	CapUSD          float64
	SpentUSD        float64
	ModelCount      int
	CompletedCycles int
	TrackingSince   time.Time
}

// NewGoogleAIStudioTracker creates a new GoogleAIStudioTracker.
func NewGoogleAIStudioTracker(s *store.Store, logger *slog.Logger) *GoogleAIStudioTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &GoogleAIStudioTracker{store: s, logger: logger}
}

// SetOnReset registers a callback invoked when a cycle reset is detected.
func (t *GoogleAIStudioTracker) SetOnReset(fn func(string)) {
	t.onReset = fn
}

// Process records cycle state for a Google AI Studio snapshot.
func (t *GoogleAIStudioTracker) Process(snapshot *api.GoogleAIStudioSnapshot) error {
	if snapshot == nil {
		return nil
	}
	quotaType := "voucher"
	cycle, err := t.store.QueryActiveGoogleAIStudioCycle(quotaType)
	if err != nil {
		return fmt.Errorf("google_aistudio tracker: query active cycle: %w", err)
	}
	if cycle == nil {
		if _, err := t.store.CreateGoogleAIStudioCycle(quotaType, snapshot.CapturedAt); err != nil {
			return fmt.Errorf("google_aistudio tracker: create cycle: %w", err)
		}
		cycle, err = t.store.QueryActiveGoogleAIStudioCycle(quotaType)
		if err != nil || cycle == nil {
			return err
		}
	}

	peak := cycle.PeakUsage
	if float64(snapshot.Requests24h) > peak {
		peak = float64(snapshot.Requests24h)
	}
	_ = t.store.UpdateGoogleAIStudioCycle(cycle.ID, peak, snapshot.SpentUSD)

	t.lastSpentUSD = snapshot.SpentUSD
	t.hasLastValues = true
	return nil
}

// UsageSummary returns computed summary statistics for Google AI Studio.
func (t *GoogleAIStudioTracker) UsageSummary() (*GoogleAIStudioSummary, error) {
	latest, err := t.store.QueryLatestGoogleAIStudio()
	if err != nil {
		return nil, err
	}
	history, err := t.store.QueryGoogleAIStudioCycleHistory("voucher", 50)
	if err != nil {
		return nil, err
	}
	summary := &GoogleAIStudioSummary{
		QuotaType:       "voucher",
		CompletedCycles: len(history),
	}
	if latest != nil {
		summary.Project = latest.Project
		summary.Requests24h = latest.Requests24h
		summary.CapUSD = latest.CapUSD
		summary.SpentUSD = latest.SpentUSD
		summary.ModelCount = latest.ModelCount
		summary.TrackingSince = latest.CapturedAt
	}
	return summary, nil
}
