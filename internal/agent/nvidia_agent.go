package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

// NvidiaAgent manages the background polling loop for NVIDIA NIM tracking.
type NvidiaAgent struct {
	client       *api.NvidiaClient
	store        *store.Store
	tracker      *tracker.NvidiaTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	notifier     *notify.NotificationEngine
	pollingCheck func() bool
}

// SetPollingCheck sets a function that is called before each poll.
func (a *NvidiaAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets the notification engine for sending alerts.
func (a *NvidiaAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

// NewNvidiaAgent creates a new NvidiaAgent.
func NewNvidiaAgent(client *api.NvidiaClient, s *store.Store, tr *tracker.NvidiaTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *NvidiaAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &NvidiaAgent{
		client:   client,
		store:    s,
		tracker:  tr,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// Run starts the NVIDIA NIM agent's polling loop.
func (a *NvidiaAgent) Run(ctx context.Context) error {
	a.logger.Info("NVIDIA NIM agent started", "interval", a.interval)
	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("NVIDIA NIM agent stopped")
	}()

	a.poll(ctx)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.poll(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}

func (a *NvidiaAgent) poll(ctx context.Context) {
	if a.pollingCheck != nil && !a.pollingCheck() {
		return
	}

	snap, err := a.client.FetchUsage(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.logger.Error("Failed to fetch NVIDIA NIM status", "error", err)
		pollFailed("nvidia", "", err)
		return
	}

	if _, err := a.store.InsertNvidiaSnapshot(snap); err != nil {
		a.logger.Error("Failed to insert NVIDIA NIM snapshot", "error", err)
		return
	}

	if a.tracker != nil {
		if err := a.tracker.Process(snap); err != nil {
			a.logger.Error("NVIDIA NIM tracker processing failed", "error", err)
		}
	}

	pollSucceeded("nvidia", "")

	if a.sm != nil && snap.RateLimitLimit > 0 {
		used := float64(snap.RateLimitLimit - snap.RateLimitRemaining)
		if used < 0 {
			used = 0
		}
		a.sm.ReportPoll([]float64{used})
	}

	a.logger.Info("NVIDIA NIM poll complete",
		"models", snap.ModelCount,
		"rate_limit_remaining", snap.RateLimitRemaining,
		"rate_limit_limit", snap.RateLimitLimit,
	)
}
