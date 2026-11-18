package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// ProbeReading captures normalized quota and spend telemetry for a single pool.
type ProbeReading struct {
	ProbeStatus   string
	ProbeSummary  string
	LastPolledAt  *time.Time
	ResetsAt      *time.Time
	Windows       []QuotaWindow
	HasQuotaUsage bool
	UsedShare     float64
	ElapsedShare  float64
	Exhausted     bool
	HasSpendData  bool
	SpentUSD      float64
	LimitUSD      float64
}

// ProbeReader reads pool telemetry from onWatch SQLite tables and external probe snapshots.
type ProbeReader interface {
	ReadProbe(ctx context.Context, p Pool, now time.Time) ProbeReading
}

// SQLiteProbeReader reads existing onWatch provider snapshot tables and routing_probe_snapshots.
type SQLiteProbeReader struct {
	db *sql.DB
}

// NewSQLiteProbeReader creates a ProbeReader backed by the shared onWatch SQLite DB.
func NewSQLiteProbeReader(db *sql.DB) *SQLiteProbeReader {
	return &SQLiteProbeReader{db: db}
}

// MonthlyCycleBounds returns the UTC [start, end) billing cycle boundaries for resetDay (1..31).
func MonthlyCycleBounds(now time.Time, resetDay int) (time.Time, time.Time) {
	u := now.UTC()
	if resetDay < 1 || resetDay > 31 {
		resetDay = 1
	}

	clampDay := func(year int, month time.Month, day int) int {
		firstNext := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
		lastDay := firstNext.AddDate(0, 0, -1).Day()
		if day > lastDay {
			return lastDay
		}
		return day
	}

	curDay := clampDay(u.Year(), u.Month(), resetDay)
	curReset := time.Date(u.Year(), u.Month(), curDay, 0, 0, 0, 0, time.UTC)

	if !u.Before(curReset) {
		nextMonth := u.Month() + 1
		nextYear := u.Year()
		if nextMonth > 12 {
			nextMonth = 1
			nextYear++
		}
		nextDay := clampDay(nextYear, nextMonth, resetDay)
		nextReset := time.Date(nextYear, nextMonth, nextDay, 0, 0, 0, 0, time.UTC)
		return curReset, nextReset
	}

	prevMonth := u.Month() - 1
	prevYear := u.Year()
	if prevMonth < 1 {
		prevMonth = 12
		prevYear--
	}
	prevDay := clampDay(prevYear, prevMonth, resetDay)
	prevReset := time.Date(prevYear, prevMonth, prevDay, 0, 0, 0, 0, time.UTC)
	return prevReset, curReset
}

// CycleBounds calculates the start and end of the current cycle given cycle type ("daily", "weekly", "monthly") and reset day.
func CycleBounds(now time.Time, cycle string, resetDay int) (time.Time, time.Time) {
	u := now.UTC()
	switch strings.ToLower(strings.TrimSpace(cycle)) {
	case "daily", "24h":
		start := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
		end := start.Add(24 * time.Hour)
		return start, end
	case "weekly", "7d":
		weekday := int(u.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday as 7th day, week starts Monday
		}
		start := time.Date(u.Year(), u.Month(), u.Day()-weekday+1, 0, 0, 0, 0, time.UTC)
		end := start.Add(7 * 24 * time.Hour)
		return start, end
	case "monthly":
		fallthrough
	default:
		if resetDay < 1 || resetDay > 31 {
			resetDay = 1
		}
		return MonthlyCycleBounds(u, resetDay)
	}
}

// WindowElapsedShare computes elapsed fraction [0..1] for a quota window ending at resetsAt.
func WindowElapsedShare(now time.Time, resetsAt *time.Time, windowHours int) float64 {
	if resetsAt == nil || windowHours <= 0 {
		return 0.5
	}
	dur := time.Duration(windowHours) * time.Hour
	start := resetsAt.UTC().Add(-dur)
	elapsed := now.UTC().Sub(start)
	if elapsed <= 0 {
		return 0
	}
	if elapsed >= dur {
		return 1
	}
	return float64(elapsed) / float64(dur)
}

// InferWindowHours maps common quota bucket names to window durations in hours.
func InferWindowHours(name string, defaultHours int) int {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(n, "5h") || strings.Contains(n, "five_hour") || n == "primary" || strings.Contains(n, "4h") || strings.Contains(n, "four_hour"):
		return 5
	case strings.Contains(n, "weekly") || strings.Contains(n, "7d") || strings.Contains(n, "seven_day") || n == "secondary":
		return 168
	case strings.Contains(n, "monthly") || strings.Contains(n, "30d") || n == "total_usage" || n == "credits":
		if defaultHours > 0 {
			return defaultHours
		}
		return 720
	default:
		if defaultHours > 0 {
			return defaultHours
		}
		return 168
	}
}

func (r *SQLiteProbeReader) ReadProbe(ctx context.Context, p Pool, now time.Time) ProbeReading {
	if r == nil || r.db == nil {
		return ProbeReading{ProbeStatus: "no_db", ProbeSummary: "database unavailable"}
	}

	switch p.ProbeSource {
	case "feedback_health":
		return ProbeReading{
			ProbeStatus:  "ok",
			ProbeSummary: "Health-gated via caller feedback",
		}
	case "antigravity_3p":
		return r.readAntigravityPool(ctx, now, "3p", p.WindowHours)
	case "antigravity_argon":
		return r.readAntigravityPool(ctx, now, "argon", p.WindowHours)
	case "antigravity_gemini":
		return r.readAntigravityPool(ctx, now, "gemini", p.WindowHours)
	case "antigravity_flash":
		return r.readAntigravityPool(ctx, now, "flash", p.WindowHours)
	case "grok":
		return r.readGrokPool(ctx, p, now)
	case "anthropic":
		return r.readStandardQuotaPool(ctx, p, now, "anthropic_snapshots", "anthropic_quota_values")
	case "codex":
		return r.readStandardQuotaPool(ctx, p, now, "codex_snapshots", "codex_quota_values")
	case "cursor":
		return r.readCursorPool(ctx, p, now)
	case "chutes":
		return r.readRoutingProbePool(ctx, p, now, "Chutes API")
	case "openrouter":
		return r.readOpenRouterPool(ctx, p, now)
	case "google_ai_studio":
		return r.readGoogleAIStudioPool(ctx, p, now)
	default:
		return r.readRoutingProbePool(ctx, p, now, p.ProbeSource)
	}
}

type agRawModel struct {
	ModelID  string `json:"modelId"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

func (r *SQLiteProbeReader) readAntigravityPool(ctx context.Context, now time.Time, group string, defaultHours int) ProbeReading {
	var snapID int64
	var capturedAtStr string
	var rawJSON sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT id, captured_at, raw_json
		FROM antigravity_snapshots
		WHERE model_count > 0
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&snapID, &capturedAtStr, &rawJSON)
	if err != nil {
		return ProbeReading{ProbeStatus: "no_data", ProbeSummary: "No Antigravity snapshot recorded"}
	}

	capturedAt := parseTimeFlexible(capturedAtStr)
	disabledByID := map[string]bool{}
	if rawJSON.Valid && rawJSON.String != "" {
		var parsed struct {
			Models []agRawModel `json:"models"`
		}
		if json.Unmarshal([]byte(rawJSON.String), &parsed) == nil {
			for _, m := range parsed.Models {
				if m.Disabled {
					disabledByID[strings.ToLower(m.ModelID)] = true
				}
			}
		}
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT model_id, COALESCE(label, ''), remaining_fraction, remaining_percent, is_exhausted, reset_time
		FROM antigravity_model_values
		WHERE snapshot_id = ?
		ORDER BY model_id ASC
	`, snapID)
	if err != nil {
		return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
	}
	defer rows.Close()

	type agRow struct {
		modelID     string
		label       string
		remFraction float64
		remPercent  float64
		exhausted   bool
		resetAt     *time.Time
		disabled    bool
	}
	var all []agRow
	for rows.Next() {
		var row agRow
		var isExh int
		var resetStr sql.NullString
		if err := rows.Scan(&row.modelID, &row.label, &row.remFraction, &row.remPercent, &isExh, &resetStr); err != nil {
			return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
		}
		row.exhausted = isExh != 0
		row.resetAt = parseNullTime(resetStr)
		row.disabled = disabledByID[strings.ToLower(row.modelID)]
		all = append(all, row)
	}
	if err := rows.Err(); err != nil {
		return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
	}

	var matched []agRow
	switch group {
	case "3p":
		for _, row := range all {
			id := strings.ToLower(row.modelID)
			lbl := strings.ToLower(row.label)
			if strings.HasPrefix(id, "3p-") || strings.Contains(id, "claude") || strings.Contains(id, "gpt") || strings.Contains(lbl, "claude") {
				matched = append(matched, row)
			}
		}
	case "argon":
		for _, row := range all {
			id := strings.ToLower(row.modelID)
			lbl := strings.ToLower(row.label)
			if strings.Contains(id, "argon") || strings.Contains(lbl, "argon") {
				matched = append(matched, row)
			}
		}
	case "gemini", "flash":
		for _, row := range all {
			id := strings.ToLower(row.modelID)
			lbl := strings.ToLower(row.label)
			if strings.Contains(id, "argon") || strings.Contains(lbl, "argon") {
				continue
			}
			if strings.HasPrefix(id, "gemini-") || strings.Contains(id, "flash") || strings.Contains(id, "hydrogen") || strings.Contains(lbl, "gemini") {
				matched = append(matched, row)
			}
		}
	}

	if len(matched) == 0 {
		return ProbeReading{
			ProbeStatus:  "no_data",
			ProbeSummary: fmt.Sprintf("No %s quota buckets in Antigravity snapshot", group),
			LastPolledAt: capturedAt,
		}
	}

	windows := make([]QuotaWindow, 0, len(matched))
	var maxUsedShare float64
	var bindingElapsed float64
	var earliestReset *time.Time
	anyExhausted := false

	for _, row := range matched {
		remPct := row.remPercent
		if remPct == 0 && row.remFraction > 0 {
			remPct = row.remFraction * 100
		}
		usedPct := clampFloat(100-remPct, 0, 100)
		exhausted := row.exhausted || remPct <= 0
		if row.disabled && exhausted {
			usedPct = 100
		}
		usedShare := usedPct / 100.0
		wh := InferWindowHours(row.modelID, defaultHours)
		elapsed := WindowElapsedShare(now, row.resetAt, wh)
		windows = append(windows, QuotaWindow{
			Name:         row.modelID,
			UsedPercent:  usedPct,
			ResetsAt:     row.resetAt,
			WindowHours:  wh,
			ElapsedShare: elapsed,
			UsedShare:    usedShare,
			Exhausted:    exhausted,
		})
		if exhausted {
			anyExhausted = true
			maxUsedShare = 1.0
		} else if usedShare > maxUsedShare {
			maxUsedShare = usedShare
			bindingElapsed = elapsed
		}
		if row.resetAt != nil && (earliestReset == nil || row.resetAt.Before(*earliestReset)) {
			t := *row.resetAt
			earliestReset = &t
		}
	}

	if anyExhausted {
		maxUsedShare = 1.0
		for _, w := range windows {
			if w.Exhausted {
				bindingElapsed = w.ElapsedShare
				if w.ResetsAt != nil {
					t := *w.ResetsAt
					earliestReset = &t
				}
				break
			}
		}
	} else if len(windows) > 0 && bindingElapsed == 0 {
		bindingElapsed = windows[0].ElapsedShare
	}

	// If any window is ahead of pace (usedShare > elapsedShare), reflect the worst pacing ratio.
	// We keep UsedShare at maxUsedShare and let Engine check every window individually.
	return ProbeReading{
		ProbeStatus:   "ok",
		ProbeSummary:  fmt.Sprintf("%d Antigravity %s buckets (%.0f%% max used)", len(windows), group, maxUsedShare*100),
		LastPolledAt:  capturedAt,
		ResetsAt:      earliestReset,
		Windows:       windows,
		HasQuotaUsage: true,
		UsedShare:     maxUsedShare,
		ElapsedShare:  bindingElapsed,
		Exhausted:     anyExhausted,
	}
}

func (r *SQLiteProbeReader) readGrokPool(ctx context.Context, p Pool, now time.Time) ProbeReading {
	var snapID int64
	var capturedAtStr string
	var rawJSON sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT id, captured_at, raw_json
		FROM grok_snapshots
		WHERE quota_count > 0
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&snapID, &capturedAtStr, &rawJSON)
	if err != nil {
		return ProbeReading{ProbeStatus: "no_data", ProbeSummary: "No Grok snapshot recorded"}
	}

	capturedAt := parseTimeFlexible(capturedAtStr)
	rows, err := r.db.QueryContext(ctx, `
		SELECT quota_name, utilization, resets_at, status
		FROM grok_quota_values
		WHERE snapshot_id = ?
		ORDER BY quota_name ASC
	`, snapID)
	if err != nil {
		return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
	}
	defer rows.Close()

	var windows []QuotaWindow
	var maxUsedShare float64
	var bindingElapsed float64
	var earliestReset *time.Time
	anyExhausted := false

	for rows.Next() {
		var name, status string
		var util float64
		var resetsStr sql.NullString
		if err := rows.Scan(&name, &util, &resetsStr, &status); err != nil {
			return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
		}
		resetsAt := parseNullTime(resetsStr)
		usedPct := clampFloat(util, 0, 100)
		usedShare := usedPct / 100.0
		wh := InferWindowHours(name, p.WindowHours)
		elapsed := WindowElapsedShare(now, resetsAt, wh)
		exh := usedPct >= 100 || strings.EqualFold(status, "exhausted")
		windows = append(windows, QuotaWindow{
			Name:         name,
			UsedPercent:  usedPct,
			ResetsAt:     resetsAt,
			WindowHours:  wh,
			ElapsedShare: elapsed,
			UsedShare:    usedShare,
			Exhausted:    exh,
		})
		if exh {
			anyExhausted = true
			maxUsedShare = 1.0
		} else if usedShare >= maxUsedShare {
			maxUsedShare = usedShare
			bindingElapsed = elapsed
		}
		if resetsAt != nil && (earliestReset == nil || resetsAt.Before(*earliestReset)) {
			t := *resetsAt
			earliestReset = &t
		}
	}
	if err := rows.Err(); err != nil {
		return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
	}
	if len(windows) == 0 {
		return ProbeReading{ProbeStatus: "no_data", ProbeSummary: "Empty Grok snapshot", LastPolledAt: capturedAt}
	}

	return ProbeReading{
		ProbeStatus:   "ok",
		ProbeSummary:  fmt.Sprintf("Grok %.0f%% used (%d bucket)", maxUsedShare*100, len(windows)),
		LastPolledAt:  capturedAt,
		ResetsAt:      earliestReset,
		Windows:       windows,
		HasQuotaUsage: true,
		UsedShare:     maxUsedShare,
		ElapsedShare:  bindingElapsed,
		Exhausted:     anyExhausted,
	}
}

func (r *SQLiteProbeReader) readStandardQuotaPool(ctx context.Context, p Pool, now time.Time, snapTable, valTable string) ProbeReading {
	var snapID int64
	var capturedAtStr string
	querySnap := fmt.Sprintf(`
		SELECT id, captured_at
		FROM %s
		WHERE quota_count > 0
		ORDER BY id DESC
		LIMIT 1
	`, snapTable)
	if err := r.db.QueryRowContext(ctx, querySnap).Scan(&snapID, &capturedAtStr); err != nil {
		return ProbeReading{ProbeStatus: "no_data", ProbeSummary: fmt.Sprintf("No %s snapshot recorded", p.ProbeSource)}
	}

	capturedAt := parseTimeFlexible(capturedAtStr)
	queryVals := fmt.Sprintf(`
		SELECT quota_name, utilization, resets_at
		FROM %s
		WHERE snapshot_id = ?
		ORDER BY quota_name ASC
	`, valTable)
	rows, err := r.db.QueryContext(ctx, queryVals, snapID)
	if err != nil {
		return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
	}
	defer rows.Close()

	var windows []QuotaWindow
	var maxUsedShare float64
	var bindingElapsed float64
	var earliestReset *time.Time
	anyExhausted := false

	for rows.Next() {
		var name string
		var util float64
		var resetsStr sql.NullString
		if err := rows.Scan(&name, &util, &resetsStr); err != nil {
			return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
		}
		resetsAt := parseNullTime(resetsStr)
		usedPct := clampFloat(util, 0, 100)
		usedShare := usedPct / 100.0
		wh := InferWindowHours(name, p.WindowHours)
		elapsed := WindowElapsedShare(now, resetsAt, wh)
		exh := usedPct >= 100
		windows = append(windows, QuotaWindow{
			Name:         name,
			UsedPercent:  usedPct,
			ResetsAt:     resetsAt,
			WindowHours:  wh,
			ElapsedShare: elapsed,
			UsedShare:    usedShare,
			Exhausted:    exh,
		})
		if exh {
			anyExhausted = true
			maxUsedShare = 1.0
		} else if usedShare >= maxUsedShare {
			maxUsedShare = usedShare
			bindingElapsed = elapsed
		}
		if resetsAt != nil && (earliestReset == nil || resetsAt.Before(*earliestReset)) {
			t := *resetsAt
			earliestReset = &t
		}
	}
	if err := rows.Err(); err != nil {
		return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
	}
	if len(windows) == 0 {
		return ProbeReading{ProbeStatus: "no_data", ProbeSummary: fmt.Sprintf("Empty %s snapshot", p.ProbeSource), LastPolledAt: capturedAt}
	}

	return ProbeReading{
		ProbeStatus:   "ok",
		ProbeSummary:  fmt.Sprintf("%s %.0f%% max used (%d windows)", p.DisplayName, maxUsedShare*100, len(windows)),
		LastPolledAt:  capturedAt,
		ResetsAt:      earliestReset,
		Windows:       windows,
		HasQuotaUsage: true,
		UsedShare:     maxUsedShare,
		ElapsedShare:  bindingElapsed,
		Exhausted:     anyExhausted,
	}
}

func (r *SQLiteProbeReader) readCursorPool(ctx context.Context, p Pool, now time.Time) ProbeReading {
	var snapID int64
	var capturedAtStr string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, captured_at
		FROM cursor_snapshots
		WHERE quota_count > 0
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&snapID, &capturedAtStr)
	if err != nil {
		return ProbeReading{ProbeStatus: "no_data", ProbeSummary: "No Cursor snapshot recorded"}
	}

	capturedAt := parseTimeFlexible(capturedAtStr)
	rows, err := r.db.QueryContext(ctx, `
		SELECT quota_name, used, limit_value, utilization, format, resets_at
		FROM cursor_quota_values
		WHERE snapshot_id = ?
		ORDER BY quota_name ASC
	`, snapID)
	if err != nil {
		return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
	}
	defer rows.Close()

	var windows []QuotaWindow
	var usedUSD, limitUSD float64
	var hasDollars bool
	var maxUsedShare float64
	var resetsAt *time.Time

	for rows.Next() {
		var name, format string
		var used, limitVal, util float64
		var resetsStr sql.NullString
		if err := rows.Scan(&name, &used, &limitVal, &util, &format, &resetsStr); err != nil {
			return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
		}
		rt := parseNullTime(resetsStr)
		if rt != nil && (resetsAt == nil || rt.Before(*resetsAt)) {
			resetsAt = rt
		}
		usedPct := clampFloat(util, 0, 100)
		if usedPct == 0 && limitVal > 0 && used > 0 {
			usedPct = clampFloat((used/limitVal)*100, 0, 100)
		}
		if strings.EqualFold(format, "dollars") || name == "total_usage" {
			hasDollars = true
			if used > usedUSD {
				usedUSD = used
			}
			if limitVal > limitUSD {
				limitUSD = limitVal
			}
		}
		start, end := MonthlyCycleBounds(now, p.ResetDay)
		elapsed := 0.5
		if end.After(start) {
			elapsed = clampFloat(float64(now.UTC().Sub(start))/float64(end.Sub(start)), 0, 1)
		}
		usedShare := usedPct / 100.0
		if usedShare > maxUsedShare {
			maxUsedShare = usedShare
		}
		windows = append(windows, QuotaWindow{
			Name:         name,
			UsedPercent:  usedPct,
			UsedUSD:      used,
			LimitUSD:     limitVal,
			ResetsAt:     rt,
			ElapsedShare: elapsed,
			UsedShare:    usedShare,
			Exhausted:    usedPct >= 100,
		})
	}
	if err := rows.Err(); err != nil {
		return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
	}
	if len(windows) == 0 {
		return ProbeReading{ProbeStatus: "no_data", ProbeSummary: "Empty Cursor snapshot", LastPolledAt: capturedAt}
	}

	effectiveLimit := p.CapUSD
	if effectiveLimit <= 0 {
		effectiveLimit = limitUSD
	}
	start, end := MonthlyCycleBounds(now, p.ResetDay)
	elapsed := 0.5
	if end.After(start) {
		elapsed = clampFloat(float64(now.UTC().Sub(start))/float64(end.Sub(start)), 0, 1)
	}
	if resetsAt == nil {
		t := end
		resetsAt = &t
	}

	return ProbeReading{
		ProbeStatus:   "ok",
		ProbeSummary:  fmt.Sprintf("Cursor $%.2f / $%.2f", usedUSD, effectiveLimit),
		LastPolledAt:  capturedAt,
		ResetsAt:      resetsAt,
		Windows:       windows,
		HasQuotaUsage: true,
		UsedShare:     maxUsedShare,
		ElapsedShare:  elapsed,
		Exhausted:     maxUsedShare >= 1.0,
		HasSpendData:  hasDollars,
		SpentUSD:      usedUSD,
		LimitUSD:      effectiveLimit,
	}
}

func (r *SQLiteProbeReader) readRoutingProbePool(ctx context.Context, p Pool, now time.Time, label string) ProbeReading {
	var usedUSD, limitUSD, usedPct float64
	var resetsStr sql.NullString
	var extraJSON, capturedAtStr string
	err := r.db.QueryRowContext(ctx, `
		SELECT used_usd, limit_usd, used_percent, resets_at, extra_json, captured_at
		FROM routing_probe_snapshots
		WHERE pool_id = ?
		ORDER BY id DESC
		LIMIT 1
	`, p.ID).Scan(&usedUSD, &limitUSD, &usedPct, &resetsStr, &extraJSON, &capturedAtStr)

	start, end := MonthlyCycleBounds(now, p.ResetDay)
	elapsed := 0.5
	if end.After(start) {
		elapsed = clampFloat(float64(now.UTC().Sub(start))/float64(end.Sub(start)), 0, 1)
	}
	cycleReset := end

	if err == sql.ErrNoRows {
		spent := cycleAdjustedSpent(p, now)
		if p.ID == "chutes" {
			var snapUsed, snapLimit, snapBalance float64
			if cErr := r.db.QueryRowContext(ctx, `SELECT used_usd, limit_usd, balance_usd FROM chutes_snapshots ORDER BY captured_at DESC LIMIT 1`).Scan(&snapUsed, &snapLimit, &snapBalance); cErr == nil {
				eff := p.CapUSD
				if eff <= 0 && snapLimit > 0 {
					eff = snapLimit
				}
				if snapUsed > spent {
					spent = snapUsed
				} else if eff > 0 && snapBalance > 0 && snapBalance < eff && eff-snapBalance > spent {
					spent = eff - snapBalance
				}
				if eff > 0 {
					usedShare := clampFloat(spent/eff, 0, 1)
					return ProbeReading{
						ProbeStatus:   "ok",
						ProbeSummary:  fmt.Sprintf("%s: $%.2f / $%.2f (%.0f%%)", label, spent, eff, usedShare*100),
						ResetsAt:      &cycleReset,
						ElapsedShare:  elapsed,
						HasQuotaUsage: true,
						HasSpendData:  true,
						SpentUSD:      spent,
						LimitUSD:      eff,
						UsedShare:     usedShare,
						Exhausted:     usedShare >= 1.0,
					}
				}
			}
		}
		usedShare := 0.0
		if p.CapUSD > 0 {
			usedShare = clampFloat(spent/p.CapUSD, 0, 1)
		}
		return ProbeReading{
			ProbeStatus:  "fallback",
			ProbeSummary: fmt.Sprintf("%s awaiting API poll; tracked spend $%.2f / $%.2f", label, spent, p.CapUSD),
			ResetsAt:     &cycleReset,
			ElapsedShare: elapsed,
			HasSpendData: true,
			SpentUSD:     spent,
			LimitUSD:     p.CapUSD,
			UsedShare:    usedShare,
		}
	}
	if err != nil {
		return ProbeReading{ProbeStatus: "error", ProbeSummary: err.Error()}
	}

	capturedAt := parseTimeFlexible(capturedAtStr)
	resetsAt := parseNullTime(resetsStr)
	if resetsAt == nil {
		resetsAt = &cycleReset
	}
	effectiveLimit := p.CapUSD
	if effectiveLimit <= 0 && limitUSD > 0 {
		effectiveLimit = limitUSD
	}
	spent := math.Max(cycleAdjustedSpent(p, now), usedUSD)
	if p.ID == "chutes" && effectiveLimit > 0 {
		var snapUsed, snapBalance float64
		if cErr := r.db.QueryRowContext(ctx, `SELECT used_usd, balance_usd FROM chutes_snapshots ORDER BY captured_at DESC LIMIT 1`).Scan(&snapUsed, &snapBalance); cErr == nil {
			if snapUsed > spent {
				spent = snapUsed
			} else if spent == 0 && snapBalance > 0 && snapBalance < effectiveLimit {
				spent = effectiveLimit - snapBalance
			}
		}
	}
	share := usedPct / 100.0
	if effectiveLimit > 0 {
		dollarShare := clampFloat(spent/effectiveLimit, 0, 1)
		if dollarShare > share {
			share = dollarShare
		}
	}

	var windows []QuotaWindow
	windows = append(windows, QuotaWindow{
		Name:         p.ID,
		UsedPercent:  clampFloat(share*100, 0, 100),
		UsedUSD:      spent,
		LimitUSD:     effectiveLimit,
		ResetsAt:     resetsAt,
		ElapsedShare: elapsed,
		UsedShare:    share,
		Exhausted:    share >= 1.0,
	})

	return ProbeReading{
		ProbeStatus:   "ok",
		ProbeSummary:  fmt.Sprintf("%s: $%.2f / $%.2f (%.0f%%)", label, spent, effectiveLimit, share*100),
		LastPolledAt:  capturedAt,
		ResetsAt:      resetsAt,
		Windows:       windows,
		HasQuotaUsage: true,
		UsedShare:     share,
		ElapsedShare:  elapsed,
		Exhausted:     share >= 1.0,
		HasSpendData:  true,
		SpentUSD:      spent,
		LimitUSD:      effectiveLimit,
	}
}

func (r *SQLiteProbeReader) readOpenRouterPool(ctx context.Context, p Pool, now time.Time) ProbeReading {
	// Prefer a dedicated PAYG key probe in routing_probe_snapshots if configured.
	reading := r.readRoutingProbePool(ctx, p, now, "OpenRouter")
	if reading.ProbeStatus == "ok" {
		return reading
	}

	// Surface account-wide credit balance from openrouter_snapshots if available.
	var capturedAtStr string
	var usageMonthly float64
	var acctCredits, acctUsage sql.NullFloat64
	if err := r.db.QueryRowContext(ctx, `
		SELECT captured_at, usage_monthly, account_total_credits, account_total_usage
		FROM openrouter_snapshots
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&capturedAtStr, &usageMonthly, &acctCredits, &acctUsage); err == nil {
		reading.LastPolledAt = parseTimeFlexible(capturedAtStr)
		reading.ProbeStatus = "ok"

		if acctCredits.Valid && acctCredits.Float64 > 0 && acctUsage.Valid {
			credits := acctCredits.Float64
			usage := acctUsage.Float64
			avail := math.Max(0, credits-usage)

			if p.CapUSD > 0 {
				availOfCap := math.Min(avail, p.CapUSD)
				spentOfCap := math.Max(0, p.CapUSD-availOfCap)
				share := clampFloat(spentOfCap/p.CapUSD, 0, 1)

				reading.HasQuotaUsage = true
				reading.HasSpendData = true
				reading.LimitUSD = p.CapUSD
				reading.SpentUSD = spentOfCap
				reading.UsedShare = share
				reading.Exhausted = avail <= 0.0001
				reading.ProbeSummary = fmt.Sprintf("OpenRouter credits: $%.2f / $%.2f (%.0f%%, $%.2f available)", spentOfCap, p.CapUSD, share*100, avail)

				reading.Windows = []QuotaWindow{
					{
						Name:        "openrouter-credits",
						UsedPercent: share * 100,
						UsedUSD:     spentOfCap,
						LimitUSD:    p.CapUSD,
						UsedShare:   share,
						Exhausted:   avail <= 0.0001,
					},
				}
				return reading
			}

			share := clampFloat(usage/credits, 0, 1)

			reading.HasQuotaUsage = true
			reading.HasSpendData = true
			reading.LimitUSD = credits
			reading.SpentUSD = usage
			reading.UsedShare = share
			reading.Exhausted = avail <= 0.0001
			reading.ProbeSummary = fmt.Sprintf("OpenRouter credits: $%.2f / $%.2f (%.0f%%, $%.2f available)", usage, credits, share*100, avail)

			reading.Windows = []QuotaWindow{
				{
					Name:        "openrouter-credits",
					UsedPercent: share * 100,
					UsedUSD:     usage,
					LimitUSD:    credits,
					UsedShare:   share,
					Exhausted:   avail <= 0.0001,
				},
			}
			return reading
		}

		reading.ProbeSummary = fmt.Sprintf("Tracked PAYG spend $%.2f / $%.2f (OR acct monthly $%.2f)", reading.SpentUSD, p.CapUSD, usageMonthly)
	}
	return reading
}

func (r *SQLiteProbeReader) readGoogleAIStudioPool(ctx context.Context, p Pool, now time.Time) ProbeReading {
	start, end := MonthlyCycleBounds(now, p.ResetDay)
	elapsed := 0.5
	if end.After(start) {
		elapsed = clampFloat(float64(now.UTC().Sub(start))/float64(end.Sub(start)), 0, 1)
	}
	spent := cycleAdjustedSpent(p, now)
	var lastPolled *time.Time
	var summary string
	var extraJSON, capturedAtStr string
	err := r.db.QueryRowContext(ctx, `
		SELECT extra_json, captured_at
		FROM routing_probe_snapshots
		WHERE pool_id = ?
		ORDER BY id DESC
		LIMIT 1
	`, p.ID).Scan(&extraJSON, &capturedAtStr)
	if err == nil {
		lastPolled = parseTimeFlexible(capturedAtStr)
		var extra struct {
			Requests24h int64  `json:"requests_24h"`
			Project     string `json:"project"`
		}
		if json.Unmarshal([]byte(extraJSON), &extra) == nil && extra.Requests24h > 0 {
			summary = fmt.Sprintf("$%.2f / $%.2f (%d requests/24h on %s)", spent, p.CapUSD, extra.Requests24h, extra.Project)
		}
	} else {
		var snapSpent, snapCap float64
		var snapReqs int64
		var snapProject, snapCapturedAt string
		if errSnap := r.db.QueryRowContext(ctx, `
			SELECT spent_usd, cap_usd, requests_24h, project, captured_at
			FROM google_aistudio_snapshots
			WHERE spent_usd > 0 OR requests_24h > 0
			ORDER BY id DESC
			LIMIT 1
		`).Scan(&snapSpent, &snapCap, &snapReqs, &snapProject, &snapCapturedAt); errSnap == nil {
			if snapSpent > spent {
				spent = snapSpent
			}
			if snapCap > 0 && p.CapUSD <= 0 {
				p.CapUSD = snapCap
			}
			if lastPolled == nil {
				lastPolled = parseTimeFlexible(snapCapturedAt)
			}
			if snapReqs > 0 && snapProject != "" {
				summary = fmt.Sprintf("$%.2f / $%.2f (%d requests/24h on %s)", spent, p.CapUSD, snapReqs, snapProject)
			}
		}
	}

	share := 0.0
	if p.CapUSD > 0 {
		share = clampFloat(spent/p.CapUSD, 0, 1)
	}
	if summary == "" {
		summary = fmt.Sprintf("Tracked voucher spend $%.2f / $%.2f", spent, p.CapUSD)
	}

	cycleReset := end
	return ProbeReading{
		ProbeStatus:   "ok",
		ProbeSummary:  summary,
		LastPolledAt:  lastPolled,
		ResetsAt:      &cycleReset,
		HasQuotaUsage: p.CapUSD > 0,
		UsedShare:     share,
		ElapsedShare:  elapsed,
		Exhausted:     p.CapUSD > 0 && spent >= p.CapUSD,
		HasSpendData:  true,
		SpentUSD:      spent,
		LimitUSD:      p.CapUSD,
		Windows: []QuotaWindow{
			{
				Name:         "monthly_voucher",
				UsedPercent:  share * 100,
				UsedUSD:      spent,
				LimitUSD:     p.CapUSD,
				ResetsAt:     &cycleReset,
				ElapsedShare: elapsed,
				UsedShare:    share,
				Exhausted:    p.CapUSD > 0 && spent >= p.CapUSD,
			},
		},
	}
}

func cycleAdjustedSpent(p Pool, now time.Time) float64 {
	curStart, _ := CycleBounds(now, p.ResetCycle, p.ResetDay)
	if p.CycleStart != nil && p.CycleStart.Before(curStart) {
		return 0
	}
	return math.Max(0, p.SpentUSD)
}

func parseTimeFlexible(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
