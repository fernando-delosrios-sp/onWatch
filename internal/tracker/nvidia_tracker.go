package tracker

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

// NvidiaTracker manages cycle tracking and summary calculation for NVIDIA NIM.
type NvidiaTracker struct {
	store         *store.Store
	logger        *slog.Logger
	lastRemaining int
	hasLastValues bool
	onReset       func(quotaName string)
}

// NvidiaSummary contains computed usage statistics for NVIDIA NIM.
type NvidiaSummary struct {
	QuotaType          string
	ModelCount         int
	RateLimitRemaining int
	RateLimitLimit     int
	CompletedCycles    int
	TrackingSince      time.Time
}

// NewNvidiaTracker creates a new NvidiaTracker.
func NewNvidiaTracker(s *store.Store, logger *slog.Logger) *NvidiaTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &NvidiaTracker{store: s, logger: logger}
}

// SetOnReset registers a callback invoked when a rate limit reset is detected.
func (t *NvidiaTracker) SetOnReset(fn func(string)) {
	t.onReset = fn
}

// Process records cycle state for an NVIDIA NIM snapshot.
func (t *NvidiaTracker) Process(snapshot *api.NvidiaSnapshot) error {
	if snapshot == nil {
		return nil
	}
	quotaType := "nim"
	cycle, err := t.store.QueryActiveNvidiaCycle(quotaType)
	if err != nil {
		return fmt.Errorf("nvidia tracker: query active cycle: %w", err)
	}
	if cycle == nil {
		if _, err := t.store.CreateNvidiaCycle(quotaType, snapshot.CapturedAt); err != nil {
			return fmt.Errorf("nvidia tracker: create cycle: %w", err)
		}
		cycle, err = t.store.QueryActiveNvidiaCycle(quotaType)
		if err != nil || cycle == nil {
			return err
		}
	}

	used := 0.0
	if snapshot.RateLimitLimit > 0 {
		used = float64(snapshot.RateLimitLimit - snapshot.RateLimitRemaining)
		if used < 0 {
			used = 0
		}
	}
	peak := cycle.PeakUsage
	if used > peak {
		peak = used
	}
	_ = t.store.UpdateNvidiaCycle(cycle.ID, peak, used)

	t.lastRemaining = snapshot.RateLimitRemaining
	t.hasLastValues = true
	return nil
}

// UsageSummary returns computed summary statistics for NVIDIA NIM.
func (t *NvidiaTracker) UsageSummary() (*NvidiaSummary, error) {
	latest, err := t.store.QueryLatestNvidia()
	if err != nil {
		return nil, err
	}
	history, err := t.store.QueryNvidiaCycleHistory("nim", 50)
	if err != nil {
		return nil, err
	}
	summary := &NvidiaSummary{
		QuotaType:       "nim",
		CompletedCycles: len(history),
	}
	if latest != nil {
		summary.ModelCount = latest.ModelCount
		summary.RateLimitRemaining = latest.RateLimitRemaining
		summary.RateLimitLimit = latest.RateLimitLimit
		summary.TrackingSince = latest.CapturedAt
	}
	return summary, nil
}
