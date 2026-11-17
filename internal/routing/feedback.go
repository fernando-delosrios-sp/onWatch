package routing

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// ApplyFeedback records caller telemetry, updates pool health/cooldowns, and accumulates USD spend.
func (e *Engine) ApplyFeedback(ctx context.Context, in FeedbackInput, now time.Time) (*FeedbackEvent, *PoolHealth, error) {
	if e == nil || e.store == nil {
		return nil, nil, fmt.Errorf("routing engine not initialized")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	in = normalizeFeedbackInput(in)

	poolID, matchedModel := e.resolveFeedbackTarget(in)
	if poolID == "" {
		return nil, nil, fmt.Errorf("unable to resolve pool from feedback (pool=%q provider=%q model=%q)", in.Pool, in.Provider, in.Model)
	}

	// Ensure pool exists; if not, fail cleanly.
	pool, err := e.store.GetPool(poolID)
	if err != nil {
		return nil, nil, err
	}
	if pool == nil {
		return nil, nil, fmt.Errorf("unknown pool %q", poolID)
	}

	kind := classifyFeedback(in)

	costUSD := in.CostUSD
	if costUSD <= 0 && (in.PromptTokens > 0 || in.CompletionTokens > 0) && matchedModel != nil {
		costUSD = EstimateCostUSD(*matchedModel, in.PromptTokens, in.CompletionTokens, in.CachedTokens)
	}
	if costUSD > 0 {
		_ = e.store.AddPoolSpend(poolID, costUSD, now)
	}

	healthMap, err := e.store.GetPoolHealthMap()
	if err != nil {
		return nil, nil, err
	}
	h, ok := healthMap[poolID]
	if !ok {
		h = PoolHealth{PoolID: poolID, HealthScore: 100, UpdatedAt: now}
	}

	reason := strings.TrimSpace(in.Note)
	if reason == "" {
		reason = strings.TrimSpace(in.Error)
	}
	if reason == "" {
		reason = kind
	}

	switch kind {
	case "ok":
		h.ConsecutiveErrors = 0
		h.CooldownUntil = nil
		h.LastOKAt = &now
		h.ErrorRate = clampFloat(h.ErrorRate*0.7, 0, 1)
		h.HealthScore = clampFloat(h.HealthScore+15, 0, 100)
		ttft := in.TTFTMs
		if ttft <= 0 {
			ttft = in.LatencyMs
		}
		if ttft > 0 {
			if h.AvgTTFTMs <= 0 {
				h.AvgTTFTMs = ttft
			} else {
				h.AvgTTFTMs = 0.3*ttft + 0.7*h.AvgTTFTMs
			}
		}
		h.LastReason = "ok"

	case "429":
		h.ConsecutiveErrors++
		h.Last429At = &now
		h.ErrorRate = clampFloat(h.ErrorRate*0.7+0.3, 0, 1)
		h.HealthScore = clampFloat(h.HealthScore-25, 0, 100)
		cd := cooldownDuration(in.CooldownSeconds, h.ConsecutiveErrors, []time.Duration{
			5 * time.Minute,
			15 * time.Minute,
			30 * time.Minute,
			60 * time.Minute,
		})
		until := now.Add(cd)
		h.CooldownUntil = &until
		h.LastReason = fmt.Sprintf("rate limited (429): %s", reason)

	case "bad":
		h.ConsecutiveErrors++
		h.LastBadAt = &now
		h.ErrorRate = clampFloat(h.ErrorRate*0.7+0.3, 0, 1)
		h.HealthScore = clampFloat(h.HealthScore-35, 0, 100)
		cd := cooldownDuration(in.CooldownSeconds, h.ConsecutiveErrors, []time.Duration{
			15 * time.Minute,
			30 * time.Minute,
			60 * time.Minute,
		})
		until := now.Add(cd)
		h.CooldownUntil = &until
		h.LastReason = fmt.Sprintf("user /bad demotion: %s", reason)

	default: // startup_failure or generic error
		h.ConsecutiveErrors++
		h.ErrorRate = clampFloat(h.ErrorRate*0.7+0.3, 0, 1)
		h.HealthScore = clampFloat(h.HealthScore-20, 0, 100)
		if h.ConsecutiveErrors >= 2 || in.CooldownSeconds > 0 {
			cd := cooldownDuration(in.CooldownSeconds, h.ConsecutiveErrors-1, []time.Duration{
				2 * time.Minute,
				5 * time.Minute,
				15 * time.Minute,
				30 * time.Minute,
			})
			until := now.Add(cd)
			h.CooldownUntil = &until
		}
		h.LastReason = fmt.Sprintf("%s: %s", kind, reason)
	}
	h.UpdatedAt = now

	if err := e.store.UpsertPoolHealth(h); err != nil {
		return nil, nil, err
	}

	ev := FeedbackEvent{
		PoolID:           poolID,
		ModelID:          strings.TrimSpace(in.Model),
		Harness:          strings.TrimSpace(in.Harness),
		Provider:         strings.TrimSpace(in.Provider),
		Tier:             strings.TrimSpace(in.Tier),
		Event:            strings.TrimSpace(in.Event),
		Outcome:          kind,
		StatusCode:       in.StatusCode,
		TTFTMs:           in.TTFTMs,
		LatencyMs:        in.LatencyMs,
		PromptTokens:     in.PromptTokens,
		CompletionTokens: in.CompletionTokens,
		CachedTokens:     in.CachedTokens,
		CostUSD:          math.Round(costUSD*1e6) / 1e6,
		Note:             reason,
		Session:          strings.TrimSpace(in.Session),
		Issue:            strings.TrimSpace(in.Issue),
		CreatedAt:        now,
	}
	id, err := e.store.InsertFeedback(ev)
	if err != nil {
		return nil, nil, err
	}
	ev.ID = id
	return &ev, &h, nil
}

// ClearCooldown resets cooldown and consecutive errors for a pool.
func (e *Engine) ClearCooldown(poolID string, now time.Time) (*PoolHealth, error) {
	if e == nil || e.store == nil {
		return nil, fmt.Errorf("routing engine not initialized")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	pool, err := e.store.GetPool(poolID)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, fmt.Errorf("unknown pool %q", poolID)
	}
	healthMap, err := e.store.GetPoolHealthMap()
	if err != nil {
		return nil, err
	}
	h := healthMap[poolID]
	h.PoolID = poolID
	h.CooldownUntil = nil
	h.ConsecutiveErrors = 0
	h.ErrorRate = 0
	h.HealthScore = 100
	h.LastReason = "cooldown cleared"
	h.UpdatedAt = now
	if err := e.store.UpsertPoolHealth(h); err != nil {
		return nil, err
	}
	return &h, nil
}

func normalizeFeedbackInput(in FeedbackInput) FeedbackInput {
	if in.StatusCode == 0 {
		in.StatusCode = in.StatusCodeCamel
	}
	if in.TTFTMs == 0 {
		in.TTFTMs = in.TTFTMsCamel
	}
	if in.LatencyMs == 0 {
		in.LatencyMs = in.LatencyMsCamel
	}
	if in.PromptTokens == 0 {
		in.PromptTokens = in.PromptTokensCamel
	}
	if in.CompletionTokens == 0 {
		in.CompletionTokens = in.CompletionTokensCamel
	}
	if in.CachedTokens == 0 {
		in.CachedTokens = in.CachedTokensCamel
	}
	if in.CostUSD == 0 {
		in.CostUSD = in.CostUSDCamel
	}
	if in.CooldownSeconds == 0 {
		in.CooldownSeconds = in.CooldownSecondsCamel
	}
	if strings.TrimSpace(in.Note) == "" && strings.TrimSpace(in.Reason) != "" {
		in.Note = strings.TrimSpace(in.Reason)
	}
	return in
}

func (e *Engine) resolveFeedbackTarget(in FeedbackInput) (string, *RouteModel) {
	poolID := strings.TrimSpace(in.Pool)
	modelID := strings.TrimSpace(in.Model)
	harness := strings.TrimSpace(in.Harness)
	provider := strings.TrimSpace(in.Provider)

	models, err := e.store.ListModels()
	if err == nil {
		var best *RouteModel
		bestScore := -1
		for i := range models {
			m := &models[i]
			if poolID != "" && m.PoolID != poolID {
				continue
			}
			score := 0
			if modelID != "" {
				if strings.EqualFold(m.ModelID, modelID) {
					score += 4
				} else if m.Overlay != "" && (strings.EqualFold(m.Overlay, modelID) || strings.EqualFold("openrouter/"+m.Overlay, modelID)) {
					score += 3
				} else if strings.EqualFold(m.Provider+"/"+m.ModelID, modelID) || strings.EqualFold(m.Harness+"/"+m.ModelID, modelID) {
					score += 3
				} else {
					continue
				}
			}
			if harness != "" && strings.EqualFold(m.Harness, harness) {
				score += 2
			}
			if provider != "" && strings.EqualFold(m.Provider, provider) {
				score += 1
			}
			if score > bestScore {
				bestScore = score
				best = m
			}
		}
		if poolID != "" {
			return poolID, best
		}
		if best != nil && bestScore > 0 {
			return best.PoolID, best
		}
	}

	if poolID != "" {
		return poolID, nil
	}

	// Fallback provider/harness alias mapping.
	switch strings.ToLower(provider) {
	case "nvidia":
		return "nvidia-free", nil
	case "grok", "xai":
		return "xai", nil
	case "anthropic", "claude", "claudeagent":
		return "claude", nil
	case "openai", "codex", "openai-codex":
		return "codex", nil
	case "cursor":
		return "cursor", nil
	case "google-ai-studio", "google":
		return "google-ai-studio", nil
	case "chutes":
		return "chutes", nil
	case "openrouter":
		return "openrouter-payg", nil
	}
	switch strings.ToLower(harness) {
	case "grok":
		return "xai", nil
	case "claudeagent":
		return "claude", nil
	case "codex":
		return "codex", nil
	case "cursor":
		return "cursor", nil
	case "antigravity":
		return "antigravity-3p", nil
	}
	return "", nil
}

func classifyFeedback(in FeedbackInput) string {
	event := strings.ToLower(strings.TrimSpace(in.Event))
	outcome := strings.ToLower(strings.TrimSpace(in.Outcome))
	errText := strings.ToLower(strings.TrimSpace(in.Error))
	if errText == "" && outcome != "ok" && outcome != "success" && outcome != "demoted" && outcome != "bad" {
		errText = strings.ToLower(strings.TrimSpace(in.Reason))
	}

	if in.StatusCode == 429 || outcome == "429" || outcome == "rate_limit" || event == "429" || event == "rate_limit" ||
		strings.Contains(errText, "429") || strings.Contains(errText, "rate limit") || strings.Contains(errText, "too many requests") || strings.Contains(errText, "quota exceeded") {
		return "429"
	}
	if outcome == "bad" || outcome == "demoted" || event == "bad" || event == "/bad" || event == "demoted" {
		return "bad"
	}
	if outcome == "ok" || outcome == "success" || event == "ok" || event == "success" {
		return "ok"
	}
	if event == "startup_failure" || outcome == "startup_failure" {
		return "startup_failure"
	}
	if in.StatusCode >= 400 || outcome == "error" || outcome == "fail" || outcome == "failure" || errText != "" {
		return "error"
	}
	return "ok"
}

func cooldownDuration(explicitSeconds int, count int, schedule []time.Duration) time.Duration {
	if explicitSeconds > 0 {
		return time.Duration(explicitSeconds) * time.Second
	}
	if len(schedule) == 0 {
		return 5 * time.Minute
	}
	idx := count - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(schedule) {
		idx = len(schedule) - 1
	}
	return schedule[idx]
}
