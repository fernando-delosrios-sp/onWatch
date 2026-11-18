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

// GoogleAIStudioAgent manages the background polling loop for Google AI Studio tracking.
type GoogleAIStudioAgent struct {
	client       *api.GoogleAIStudioClient
	store        *store.Store
	tracker      *tracker.GoogleAIStudioTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	notifier     *notify.NotificationEngine
	pollingCheck func() bool
}

// SetPollingCheck sets a function that is called before each poll.
func (a *GoogleAIStudioAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets the notification engine for sending alerts.
func (a *GoogleAIStudioAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

// NewGoogleAIStudioAgent creates a new GoogleAIStudioAgent.
func NewGoogleAIStudioAgent(client *api.GoogleAIStudioClient, s *store.Store, tr *tracker.GoogleAIStudioTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *GoogleAIStudioAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &GoogleAIStudioAgent{
		client:   client,
		store:    s,
		tracker:  tr,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// Run starts the Google AI Studio agent's polling loop.
func (a *GoogleAIStudioAgent) Run(ctx context.Context) error {
	a.logger.Info("Google AI Studio agent started", "interval", a.interval)
	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("Google AI Studio agent stopped")
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

func (a *GoogleAIStudioAgent) poll(ctx context.Context) {
	if a.pollingCheck != nil && !a.pollingCheck() {
		return
	}

	snap, err := a.client.FetchUsage(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.logger.Error("Failed to fetch Google AI Studio usage", "error", err)
		pollFailed("googleaistudio", "", err)
		return
	}

	if _, err := a.store.InsertGoogleAIStudioSnapshot(snap); err != nil {
		a.logger.Error("Failed to insert Google AI Studio snapshot", "error", err)
		return
	}

	if a.tracker != nil {
		if err := a.tracker.Process(snap); err != nil {
			a.logger.Error("Google AI Studio tracker processing failed", "error", err)
		}
	}

	pollSucceeded("googleaistudio", "")

	if a.sm != nil {
		a.sm.ReportPoll([]float64{float64(snap.Requests24h)})
	}

	a.logger.Info("Google AI Studio poll complete",
		"models", snap.ModelCount,
		"requests_24h", snap.Requests24h,
		"cap_usd", snap.CapUSD,
	)
}
