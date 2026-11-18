package web

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

// ProviderSubroutes handles /api/providers/{provider}/models
func (h *Handler) ProviderSubroutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	// Strip optional basePath if configured
	if idx := strings.Index(path, "api/providers/"); idx >= 0 {
		path = path[idx+len("api/providers/"):]
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 2 && parts[1] == "models" {
		h.providerModels(w, r, parts[0])
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) providerModels(w http.ResponseWriter, r *http.Request, provider string) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	provider = strings.ToLower(strings.TrimSpace(provider))
	var models []api.ProviderModel
	var err error

	switch provider {
	case "chutes":
		if strings.TrimSpace(h.config.ChutesAPIKey) == "" {
			respondJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "Chutes API key is not configured"})
			return
		}
		var opts []api.ChutesOption
		if h.config.ChutesBaseURL != "" {
			opts = append(opts, api.WithChutesBaseURL(h.config.ChutesBaseURL))
		}
		if h.config.ChutesModelsURL != "" {
			opts = append(opts, api.WithChutesModelsURL(h.config.ChutesModelsURL))
		}
		models, err = api.NewChutesClient(h.config.ChutesAPIKey, h.logger, opts...).FetchModels(ctx)

	case "nvidia":
		if strings.TrimSpace(h.config.NvidiaAPIKey) == "" {
			respondJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "NVIDIA NIM API key is not configured"})
			return
		}
		var opts []api.NvidiaOption
		if h.config.NvidiaBaseURL != "" {
			opts = append(opts, api.WithNvidiaBaseURL(h.config.NvidiaBaseURL))
		}
		models, err = api.NewNvidiaClient(h.config.NvidiaAPIKey, h.logger, opts...).FetchModels(ctx)

	case "googleaistudio", "google-ai-studio":
		if strings.TrimSpace(h.config.GoogleAIStudioAPIKey) == "" {
			respondJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "Google AI Studio API key is not configured"})
			return
		}
		var opts []api.GoogleAIStudioOption
		if h.config.GoogleAIStudioBaseURL != "" {
			opts = append(opts, api.WithGoogleAIStudioBaseURL(h.config.GoogleAIStudioBaseURL))
		}
		models, err = api.NewGoogleAIStudioClient(h.config.GoogleAIStudioAPIKey, h.config.GoogleAIStudioProject, h.config.GoogleAIStudioCapUSD, h.logger, opts...).FetchModels(ctx)

	default:
		respondJSON(w, http.StatusNotFound, map[string]interface{}{"error": "model discovery not supported for provider: " + provider})
		return
	}

	if err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]interface{}{"error": err.Error()})
		return
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"provider": provider,
		"count":    len(models),
		"models":   models,
	})
}

// --- Chutes ---

func (h *Handler) currentChutes(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildChutesCurrent())
}

func (h *Handler) routingPoolCapUSD(poolID string) float64 {
	if h.store == nil || h.store.DB() == nil || poolID == "" {
		return 0
	}
	var capUSD float64
	if err := h.store.DB().QueryRow(`SELECT cap_usd FROM routing_pools WHERE id = ?`, poolID).Scan(&capUSD); err != nil || capUSD <= 0 {
		return 0
	}
	return capUSD
}

func (h *Handler) buildChutesCurrent() map[string]interface{} {
	now := time.Now().UTC()
	resp := map[string]interface{}{
		"capturedAt": now.Format(time.RFC3339),
		"credits": map[string]interface{}{
			"name":             "Chutes Credits",
			"description":      "Chutes PAYG usage & quota",
			"usedUsd":          0.0,
			"limitUsd":         0.0,
			"percent":          0.0,
			"cardPercent":      0.0,
			"remainingPercent": 0.0,
			"balanceUsd":       0.0,
			"remaining":        0.0,
			"modelCount":       0,
			"status":           "healthy",
		},
	}
	if h.store == nil {
		return resp
	}
	latest, err := h.store.QueryLatestChutes()
	if err != nil || latest == nil {
		return resp
	}
	resp["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)

	capUSD := latest.LimitUSD
	if capUSD <= 0 {
		capUSD = h.routingPoolCapUSD("chutes")
	}

	remaining := latest.BalanceUSD
	if remaining < 0 {
		remaining = 0
	}
	if remaining == 0 && capUSD > 0 {
		if latest.UsedUSD > 0 {
			remaining = math.Max(0, capUSD-latest.UsedUSD)
		} else if latest.UsedPercent > 0 {
			remaining = math.Max(0, capUSD*(1.0-latest.UsedPercent/100.0))
		}
	}

	good := 0.0
	if capUSD > 0 {
		if latest.BalanceUSD > 0 || latest.UsedUSD > 0 || latest.UsedPercent == 0 {
			avail := math.Min(math.Max(0, remaining), capUSD)
			good = (avail / capUSD) * 100.0
		} else {
			good = math.Min(100, math.Max(0, 100.0-latest.UsedPercent))
		}
	} else {
		if latest.BalanceUSD > 0 {
			good = 100.0
		} else if latest.UsedPercent > 0 {
			good = math.Min(100, math.Max(0, 100.0-latest.UsedPercent))
		} else {
			good = 0.0
		}
	}
	good = math.Min(100, math.Max(0, good))
	util := math.Min(100, math.Max(0, 100.0-good))

	usedUSD := latest.UsedUSD
	if usedUSD == 0 && capUSD > 0 && latest.BalanceUSD > 0 && latest.BalanceUSD < capUSD {
		usedUSD = capUSD - latest.BalanceUSD
	}

	status := "healthy"
	if util >= 95 {
		status = "critical"
	} else if util >= 80 {
		status = "warning"
	}
	credits := map[string]interface{}{
		"name":             "Chutes Credits",
		"description":      "Chutes PAYG usage & quota",
		"usedUsd":          usedUSD,
		"limitUsd":         capUSD,
		"percent":          util,
		"cardPercent":      good,
		"remainingPercent": good,
		"balanceUsd":       latest.BalanceUSD,
		"remaining":        remaining,
		"modelCount":       latest.ModelCount,
		"status":           status,
	}
	if latest.ResetsAt != nil {
		credits["resetsAt"] = latest.ResetsAt.Format(time.RFC3339)
	}
	if h.chutesTracker != nil {
		if sum, err := h.chutesTracker.UsageSummary(); err == nil && sum != nil {
			credits["completedCycles"] = sum.CompletedCycles
			credits["avgPerCycle"] = sum.AvgPerCycle
			credits["peakCycle"] = sum.PeakCycle
			credits["totalTracked"] = sum.TotalTracked
		}
	}
	resp["credits"] = credits
	return resp
}

func (h *Handler) historyChutes(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	duration, err := parseTimeRange(r.URL.Query().Get("range"))
	if err != nil {
		duration = 24 * time.Hour
	}
	end := time.Now().UTC()
	snaps, err := h.store.QueryChutesRange(end.Add(-duration), end)
	if err != nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	out := make([]map[string]interface{}, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, map[string]interface{}{
			"capturedAt":  s.CapturedAt.Format(time.RFC3339),
			"usedUsd":     s.UsedUSD,
			"limitUsd":    s.LimitUSD,
			"usedPercent": s.UsedPercent,
			"balanceUsd":  s.BalanceUSD,
			"modelCount":  s.ModelCount,
		})
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *Handler) chutesCycleOverview() map[string]interface{} {
	return h.buildChutesSummaryMap()
}

func (h *Handler) buildChutesSummaryMap() map[string]interface{} {
	if h.chutesTracker == nil {
		return map[string]interface{}{}
	}
	sum, err := h.chutesTracker.UsageSummary()
	if err != nil || sum == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"usedUsd":         sum.CurrentUsedUSD,
		"limitUsd":        sum.LimitUSD,
		"percent":         sum.CurrentPercent,
		"completedCycles": sum.CompletedCycles,
		"avgPerCycle":     sum.AvgPerCycle,
		"peakCycle":       sum.PeakCycle,
		"totalTracked":    sum.TotalTracked,
	}
}

func (h *Handler) buildChutesInsights() map[string]interface{} {
	cur := h.buildChutesCurrent()
	return map[string]interface{}{
		"provider": "chutes",
		"summary":  cur["credits"],
	}
}

func (h *Handler) loggingHistoryChutes(w http.ResponseWriter, r *http.Request) {
	h.historyChutes(w, r)
}

// --- NVIDIA NIM ---

func (h *Handler) currentNvidia(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildNvidiaCurrent())
}

func (h *Handler) buildNvidiaCurrent() map[string]interface{} {
	now := time.Now().UTC()
	resp := map[string]interface{}{
		"capturedAt": now.Format(time.RFC3339),
		"nim": map[string]interface{}{
			"name":               "NVIDIA NIM",
			"description":        "NVIDIA NIM API models & rate limits",
			"modelCount":         0,
			"rateLimitRemaining": 0,
			"rateLimitLimit":     0,
			"keyValid":           false,
			"status":             "healthy",
		},
	}
	if h.store == nil {
		return resp
	}
	latest, err := h.store.QueryLatestNvidia()
	if err != nil || latest == nil {
		return resp
	}
	resp["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
	status := "healthy"
	if !latest.KeyValid {
		status = "critical"
	} else if latest.RateLimitLimit > 0 && latest.RateLimitRemaining <= 2 {
		status = "warning"
	}
	resp["nim"] = map[string]interface{}{
		"name":               "NVIDIA NIM",
		"description":        "NVIDIA NIM API models & rate limits",
		"modelCount":         latest.ModelCount,
		"rateLimitRemaining": latest.RateLimitRemaining,
		"rateLimitLimit":     latest.RateLimitLimit,
		"keyValid":           latest.KeyValid,
		"status":             status,
	}
	return resp
}

func (h *Handler) historyNvidia(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	duration, err := parseTimeRange(r.URL.Query().Get("range"))
	if err != nil {
		duration = 24 * time.Hour
	}
	end := time.Now().UTC()
	snaps, err := h.store.QueryNvidiaRange(end.Add(-duration), end)
	if err != nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	out := make([]map[string]interface{}, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, map[string]interface{}{
			"capturedAt":         s.CapturedAt.Format(time.RFC3339),
			"modelCount":         s.ModelCount,
			"rateLimitRemaining": s.RateLimitRemaining,
			"rateLimitLimit":     s.RateLimitLimit,
			"keyValid":           s.KeyValid,
		})
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *Handler) nvidiaCycleOverview() map[string]interface{} {
	return h.buildNvidiaSummaryMap()
}

func (h *Handler) buildNvidiaSummaryMap() map[string]interface{} {
	if h.nvidiaTracker == nil {
		return map[string]interface{}{}
	}
	sum, err := h.nvidiaTracker.UsageSummary()
	if err != nil || sum == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"modelCount":         sum.ModelCount,
		"rateLimitRemaining": sum.RateLimitRemaining,
		"rateLimitLimit":     sum.RateLimitLimit,
		"completedCycles":    sum.CompletedCycles,
	}
}

func (h *Handler) buildNvidiaInsights() map[string]interface{} {
	cur := h.buildNvidiaCurrent()
	return map[string]interface{}{
		"provider": "nvidia",
		"summary":  cur["nim"],
	}
}

func (h *Handler) loggingHistoryNvidia(w http.ResponseWriter, r *http.Request) {
	h.historyNvidia(w, r)
}

// --- Google AI Studio ---

func (h *Handler) currentGoogleAIStudio(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildGoogleAIStudioCurrent())
}

func (h *Handler) buildGoogleAIStudioCurrent() map[string]interface{} {
	now := time.Now().UTC()
	capUSD := h.config.GoogleAIStudioCapUSD
	if capUSD <= 0 {
		capUSD = 15.0
	}
	resp := map[string]interface{}{
		"capturedAt": now.Format(time.RFC3339),
		"aistudio": map[string]interface{}{
			"name":        "Google AI Studio",
			"description": "Google AI Studio models, GCP request count & voucher cap",
			"project":     h.config.GoogleAIStudioProject,
			"requests24h": int64(0),
			"capUsd":      capUSD,
			"spentUsd":    0.0,
			"modelCount":  0,
			"keyValid":    false,
			"status":      "healthy",
		},
	}
	if h.store == nil {
		return resp
	}
	latest, err := h.store.QueryLatestGoogleAIStudio()
	if err != nil || latest == nil {
		return resp
	}
	resp["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
	status := "healthy"
	if latest.CapUSD > 0 && latest.SpentUSD >= latest.CapUSD {
		status = "critical"
	} else if latest.CapUSD > 0 && latest.SpentUSD >= latest.CapUSD*0.8 {
		status = "warning"
	}
	resp["aistudio"] = map[string]interface{}{
		"name":        "Google AI Studio",
		"description": "Google AI Studio models, GCP request count & voucher cap",
		"project":     latest.Project,
		"requests24h": latest.Requests24h,
		"capUsd":      latest.CapUSD,
		"spentUsd":    latest.SpentUSD,
		"modelCount":  latest.ModelCount,
		"keyValid":    latest.KeyValid,
		"status":      status,
	}
	return resp
}

func (h *Handler) historyGoogleAIStudio(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	duration, err := parseTimeRange(r.URL.Query().Get("range"))
	if err != nil {
		duration = 24 * time.Hour
	}
	end := time.Now().UTC()
	snaps, err := h.store.QueryGoogleAIStudioRange(end.Add(-duration), end)
	if err != nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	out := make([]map[string]interface{}, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, map[string]interface{}{
			"capturedAt":  s.CapturedAt.Format(time.RFC3339),
			"requests24h": s.Requests24h,
			"capUsd":      s.CapUSD,
			"spentUsd":    s.SpentUSD,
			"modelCount":  s.ModelCount,
		})
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *Handler) googleAIStudioCycleOverview() map[string]interface{} {
	return h.buildGoogleAIStudioSummaryMap()
}

func (h *Handler) buildGoogleAIStudioSummaryMap() map[string]interface{} {
	if h.googleAIStudioTracker == nil {
		return map[string]interface{}{}
	}
	sum, err := h.googleAIStudioTracker.UsageSummary()
	if err != nil || sum == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"project":         sum.Project,
		"requests24h":     sum.Requests24h,
		"capUsd":          sum.CapUSD,
		"spentUsd":        sum.SpentUSD,
		"modelCount":      sum.ModelCount,
		"completedCycles": sum.CompletedCycles,
	}
}

func (h *Handler) buildGoogleAIStudioInsights() map[string]interface{} {
	cur := h.buildGoogleAIStudioCurrent()
	aistudio, _ := cur["aistudio"].(map[string]interface{})
	var stats []map[string]interface{}
	var insights []map[string]interface{}
	if aistudio != nil {
		spent, _ := aistudio["spentUsd"].(float64)
		capUSD, _ := aistudio["capUsd"].(float64)
		reqs, _ := aistudio["requests24h"].(int64)
		models, _ := aistudio["modelCount"].(int)
		status, _ := aistudio["status"].(string)

		stats = append(stats,
			map[string]interface{}{"label": "Tracked Spend", "value": fmt.Sprintf("$%.2f / $%.2f", spent, capUSD)},
			map[string]interface{}{"label": "24h Requests", "value": fmt.Sprintf("%d", reqs)},
			map[string]interface{}{"label": "Available Models", "value": fmt.Sprintf("%d", models)},
		)

		msg := fmt.Sprintf("Voucher spend is $%.2f of $%.2f monthly cap with %d requests in the last 24h across %d models.", spent, capUSD, reqs, models)
		insights = append(insights, map[string]interface{}{
			"key":      "voucher_summary",
			"title":    "Voucher & API Usage",
			"message":  msg,
			"severity": status,
		})
	}

	return map[string]interface{}{
		"provider": "googleaistudio",
		"summary":  cur["aistudio"],
		"stats":    stats,
		"insights": insights,
	}
}

func (h *Handler) loggingHistoryGoogleAIStudio(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"provider": "googleaistudio", "quotaNames": []string{"voucher"}, "logs": []interface{}{}})
		return
	}
	start, end, limit := h.loggingHistoryRangeAndLimit(r)
	snaps, err := h.store.QueryGoogleAIStudioRange(start, end)
	if err != nil {
		h.logger.Error("failed to query googleaistudio range", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query history")
		return
	}
	if limit > 0 && len(snaps) > limit {
		snaps = snaps[len(snaps)-limit:]
	}

	quotaNames := []string{"voucher"}
	capturedAt := make([]time.Time, 0, len(snaps))
	ids := make([]int64, 0, len(snaps))
	quotaSeries := make([]map[string]loggingHistoryCrossQuota, 0, len(snaps))

	for _, s := range snaps {
		capturedAt = append(capturedAt, s.CapturedAt)
		ids = append(ids, s.ID)

		pct := 0.0
		if s.CapUSD > 0 {
			pct = (s.SpentUSD / s.CapUSD) * 100
		}
		quotaSeries = append(quotaSeries, map[string]loggingHistoryCrossQuota{
			"voucher": {
				Name:     "voucher",
				Percent:  pct,
				Value:    s.SpentUSD,
				Limit:    s.CapUSD,
				Format:   "currency",
				HasValue: true,
				HasLimit: s.CapUSD > 0,
			},
		})
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"provider":   "googleaistudio",
		"quotaNames": quotaNames,
		"logs":       loggingHistoryRowsFromSnapshots(capturedAt, ids, quotaNames, quotaSeries),
	})
}

