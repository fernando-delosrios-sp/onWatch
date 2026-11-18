package web

import (
	"math"
	"strings"
	"time"
)

const (
	// PaceStatusGreen indicates current usage is at, below, or within 5% of linear pace.
	PaceStatusGreen = "green"
	// PaceStatusYellow indicates current usage is moderately ahead of linear pace (5% to 15%).
	PaceStatusYellow = "yellow"
	// PaceStatusRed indicates current usage is significantly ahead of linear pace (>15%).
	PaceStatusRed = "red"
)

// ProjectedPaceInfo holds the linear 0-100% cycle projection and traffic-light comparison.
type ProjectedPaceInfo struct {
	CycleStart    time.Time
	CycleEnd      time.Time
	ProjectedPace float64
	PaceDelta     float64
	PaceStatus    string
}

// ComputeLinearProjectedPace computes linear projected usage from 0% at cycleStart
// to 100% at cycleEnd evaluated at now, and compares current utilization against it.
func ComputeLinearProjectedPace(utilization float64, cycleStart, cycleEnd, now time.Time) (ProjectedPaceInfo, bool) {
	total := cycleEnd.Sub(cycleStart)
	if total <= 0 {
		return ProjectedPaceInfo{}, false
	}
	elapsed := now.Sub(cycleStart)
	ratio := float64(elapsed) / float64(total)
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}

	projected := math.Round(ratio*1000) / 10
	delta := math.Round((utilization-projected)*10) / 10

	status := PaceStatusGreen
	if delta > 15.0 {
		status = PaceStatusRed
	} else if delta > 5.0 {
		status = PaceStatusYellow
	}

	return ProjectedPaceInfo{
		CycleStart:    cycleStart,
		CycleEnd:      cycleEnd,
		ProjectedPace: projected,
		PaceDelta:     delta,
		PaceStatus:    status,
	}, true
}

// InferCycleWindow determines the start and end of the current quota cycle
// from the provider, quota name, reset timestamp, and optional window metadata.
func InferCycleWindow(provider, quotaName string, resetsAt time.Time, windowSeconds int64, isWeekly bool) (time.Time, time.Time, bool) {
	if resetsAt.IsZero() {
		return time.Time{}, time.Time{}, false
	}

	if windowSeconds > 0 {
		dur := time.Duration(windowSeconds) * time.Second
		return resetsAt.Add(-dur), resetsAt, true
	}

	p := strings.ToLower(strings.TrimSpace(provider))
	q := strings.ToLower(strings.TrimSpace(quotaName))

	switch p {
	case "cursor", "copilot":
		return resetsAt.AddDate(0, -1, 0), resetsAt, true
	case "synthetic":
		if q == "search" {
			return resetsAt.Add(-1 * time.Hour), resetsAt, true
		}
		return resetsAt.Add(-5 * time.Hour), resetsAt, true
	case "zai":
		if q == "tokenslimit" {
			return resetsAt.Add(-5 * time.Hour), resetsAt, true
		}
		return resetsAt.AddDate(0, -1, 0), resetsAt, true
	case "minimax":
		if isWeekly || strings.Contains(q, "weekly") || strings.Contains(q, "seven_day") {
			return resetsAt.Add(-7 * 24 * time.Hour), resetsAt, true
		}
		return resetsAt.Add(-5 * time.Hour), resetsAt, true
	case "ollama":
		if q == "session" || q == "five_hour" || strings.Contains(q, "5h") {
			return resetsAt.Add(-5 * time.Hour), resetsAt, true
		}
		return resetsAt.AddDate(0, -1, 0), resetsAt, true
	}

	// Standard quota naming conventions across Anthropic, Codex, OpenCode, Muse,
	// Alibaba, Zenmux, CommandCode, Mistral, Kimi, Grok, Antigravity, Gemini.
	switch {
	case q == "five_hour" || q == "5h" || strings.Contains(q, "five_hour") || strings.Contains(q, "5_hour"):
		return resetsAt.Add(-5 * time.Hour), resetsAt, true
	case q == "hourly" || q == "search":
		return resetsAt.Add(-1 * time.Hour), resetsAt, true
	case q == "daily" || q == "24h" || strings.Contains(q, "daily"):
		return resetsAt.Add(-24 * time.Hour), resetsAt, true
	case isWeekly || q == "weekly" || strings.HasPrefix(q, "seven_day") || strings.Contains(q, "weekly") || q == "code_review":
		return resetsAt.Add(-7 * 24 * time.Hour), resetsAt, true
	case q == "monthly" || strings.Contains(q, "monthly"):
		return resetsAt.AddDate(0, -1, 0), resetsAt, true
	}

	// Fallback based on remaining time until reset.
	remaining := time.Until(resetsAt)
	switch {
	case remaining > 7*24*time.Hour:
		return resetsAt.AddDate(0, -1, 0), resetsAt, true
	case remaining > 24*time.Hour:
		return resetsAt.Add(-7 * 24 * time.Hour), resetsAt, true
	case remaining > 5*time.Hour:
		return resetsAt.Add(-24 * time.Hour), resetsAt, true
	default:
		return resetsAt.Add(-5 * time.Hour), resetsAt, true
	}
}

func isSubscriptionPaceProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "moonshot", "deepseek", "openrouter", "chutes", "nvidia", "googleaistudio", "api-integrations":
		return false
	default:
		return true
	}
}

// EnrichSubscriptionPace adds cycleStart, cycleEnd, projectedPace, paceDelta, and paceStatus
// to subscription quota entries in a /api/current response map.
func EnrichSubscriptionPace(provider string, response map[string]interface{}, now time.Time) {
	if response == nil || !isSubscriptionPaceProvider(provider) {
		return
	}

	if qList, ok := response["quotas"].([]interface{}); ok {
		for _, item := range qList {
			if qMap, ok := item.(map[string]interface{}); ok {
				enrichQuotaMapPace(provider, "", qMap, now)
			}
		}
	} else if qMaps, ok := response["quotas"].([]map[string]interface{}); ok {
		for _, qMap := range qMaps {
			enrichQuotaMapPace(provider, "", qMap, now)
		}
	}

	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "synthetic":
		for _, key := range []string{"subscription", "search", "toolCalls"} {
			if qMap, ok := response[key].(map[string]interface{}); ok {
				enrichQuotaMapPace(provider, key, qMap, now)
			}
		}
	case "zai":
		for _, key := range []string{"tokensLimit", "timeLimit", "toolCalls"} {
			if qMap, ok := response[key].(map[string]interface{}); ok {
				enrichQuotaMapPace(provider, key, qMap, now)
			}
		}
	}
}

func enrichQuotaMapPace(provider, fallbackName string, qMap map[string]interface{}, now time.Time) {
	if qMap == nil {
		return
	}
	if boolVal(qMap["unlimited"]) || boolVal(qMap["utilizationUnknown"]) {
		return
	}

	name := stringVal(qMap["name"])
	if name == "" {
		name = stringVal(qMap["modelId"])
	}
	if name == "" {
		name = fallbackName
	}

	resetStr := firstNonEmptyString(
		stringVal(qMap["resetsAt"]),
		stringVal(qMap["renewsAt"]),
		stringVal(qMap["resetDate"]),
		stringVal(qMap["resetTime"]),
		stringVal(qMap["resetAt"]),
	)
	if resetStr == "" {
		return
	}
	resetsAt, err := parseFlexibleTime(resetStr)
	if err != nil || resetsAt.IsZero() {
		return
	}

	util, hasUtil := firstFloatVal(qMap, "usagePercent", "utilization", "percent", "cardPercent")
	if !hasUtil {
		return
	}

	winSec := int64Val(qMap["windowSeconds"])
	isWeekly := boolVal(qMap["isWeekly"])

	cycleStart, cycleEnd, ok := InferCycleWindow(provider, name, resetsAt, winSec, isWeekly)
	if !ok {
		return
	}

	info, ok := ComputeLinearProjectedPace(util, cycleStart, cycleEnd, now)
	if !ok {
		return
	}

	qMap["cycleStart"] = info.CycleStart.UTC().Format(time.RFC3339)
	qMap["cycleEnd"] = info.CycleEnd.UTC().Format(time.RFC3339)
	qMap["projectedPace"] = info.ProjectedPace
	qMap["paceDelta"] = info.PaceDelta
	qMap["paceStatus"] = info.PaceStatus
}

func parseFlexibleTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

func stringVal(v interface{}) string {
	s, _ := v.(string)
	return s
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func boolVal(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

func int64Val(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func firstFloatVal(m map[string]interface{}, keys ...string) (float64, bool) {
	for _, k := range keys {
		v, exists := m[k]
		if !exists || v == nil {
			continue
		}
		switch n := v.(type) {
		case float64:
			return n, true
		case float32:
			return float64(n), true
		case int:
			return float64(n), true
		case int64:
			return float64(n), true
		}
	}
	return 0, false
}
