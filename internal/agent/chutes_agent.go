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

// ChutesAgent manages the background polling loop for Chutes usage & credits tracking.
type ChutesAgent struct {
	client       *api.ChutesClient
	store        *store.Store
	tracker      *tracker.ChutesTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	notifier     *notify.NotificationEngine
	pollingCheck func() bool
}

// SetPollingCheck sets a function that is called before each poll.
func (a *ChutesAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets the notification engine for sending alerts.
func (a *ChutesAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

// NewChutesAgent creates a new ChutesAgent.
func NewChutesAgent(client *api.ChutesClient, s *store.Store, tr *tracker.ChutesTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *ChutesAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &ChutesAgent{
		client:   client,
		store:    s,
		tracker:  tr,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// Run starts the Chutes agent's polling loop.
func (a *ChutesAgent) Run(ctx context.Context) error {
	a.logger.Info("Chutes agent started", "interval", a.interval)
	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("Chutes agent stopped")
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

func (a *ChutesAgent) poll(ctx context.Context) {
	if a.pollingCheck != nil && !a.pollingCheck() {
		return
	}

	snap, err := a.client.FetchUsage(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.logger.Error("Failed to fetch Chutes usage", "error", err)
		pollFailed("chutes", "", err)
		return
	}

	if models, mErr := a.client.FetchModels(ctx); mErr == nil {
		snap.ModelCount = len(models)
	}

	if _, err := a.store.InsertChutesSnapshot(snap); err != nil {
		a.logger.Error("Failed to insert Chutes snapshot", "error", err)
		return
	}

	if a.tracker != nil {
		if err := a.tracker.Process(snap); err != nil {
			a.logger.Error("Chutes tracker processing failed", "error", err)
		}
	}

	pollSucceeded("chutes", "")

	if a.sm != nil {
		a.sm.ReportPoll([]float64{snap.UsedUSD})
	}

	if a.notifier != nil && snap.LimitUSD > 0 {
		a.notifier.Check(notify.QuotaStatus{
			Provider:    "chutes",
			QuotaKey:    "credits",
			Utilization: snap.UsedPercent,
			Limit:       snap.LimitUSD,
		})
	}

	a.logger.Info("Chutes poll complete",
		"used_usd", snap.UsedUSD,
		"limit_usd", snap.LimitUSD,
		"used_percent", snap.UsedPercent,
		"models", snap.ModelCount,
	)
}
