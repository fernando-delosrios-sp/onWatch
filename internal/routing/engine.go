package routing

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const paceEpsilon = 1e-9

// Engine computes pool economy status, waterfall ranking, and 3-tier route selection.
type Engine struct {
	store  *Store
	probes ProbeReader
}

// NewEngine creates a routing Engine. If probes is nil, a SQLiteProbeReader on store.db is used.
func NewEngine(store *Store, probes ProbeReader) *Engine {
	if probes == nil && store != nil {
		probes = NewSQLiteProbeReader(store.db)
	}
	return &Engine{
		store:  store,
		probes: probes,
	}
}

// RankPools evaluates all configured pools at now and sorts them by waterfall economy preference.
func (e *Engine) RankPools(ctx context.Context, now time.Time) ([]PoolStatus, error) {
	if e == nil || e.store == nil {
		return nil, fmt.Errorf("routing engine not initialized")
	}
	now = now.UTC()

	pools, err := e.store.ListPools()
	if err != nil {
		return nil, err
	}
	healthMap, err := e.store.GetPoolHealthMap()
	if err != nil {
		return nil, err
	}

	statuses := make([]PoolStatus, 0, len(pools))
	for _, p := range pools {
		h, ok := healthMap[p.ID]
		if !ok {
			h = PoolHealth{PoolID: p.ID, HealthScore: 100, UpdatedAt: now}
		}
		var reading ProbeReading
		if e.probes != nil {
			reading = e.probes.ReadProbe(ctx, p, now)
		}
		statuses = append(statuses, EvaluatePoolStatus(p, h, reading, now))
	}

	sort.SliceStable(statuses, func(i, j int) bool {
		return comparePoolStatus(statuses[i], statuses[j])
	})

	for idx := range statuses {
		statuses[idx].PreferenceRank = idx + 1
	}
	return statuses, nil
}

// EvaluatePoolStatus combines a pool config, health state, and probe reading into a PoolStatus.
func EvaluatePoolStatus(p Pool, h PoolHealth, reading ProbeReading, now time.Time) PoolStatus {
	now = now.UTC()
	st := PoolStatus{
		Pool:               p,
		Health:             h,
		Eligible:           true,
		Windows:            reading.Windows,
		ProbeStatus:        reading.ProbeStatus,
		ProbeSummary:       reading.ProbeSummary,
		LastPolledAt:       reading.LastPolledAt,
		ResetsAt:           reading.ResetsAt,
		EffectiveExpiresAt: p.ExpiresAt,
	}
	if st.ProbeStatus == "" {
		st.ProbeStatus = "unknown"
	}

	// Effective dollar cap / spend.
	effectiveCap := p.CapUSD
	if effectiveCap <= 0 && reading.LimitUSD > 0 {
		effectiveCap = reading.LimitUSD
	}
	effectiveSpent := cycleAdjustedSpent(p, now)
	if reading.HasSpendData && reading.SpentUSD > effectiveSpent {
		effectiveSpent = reading.SpentUSD
	}
	st.EffectiveCapUSD = effectiveCap
	st.EffectiveSpentUSD = effectiveSpent
	if effectiveCap > 0 {
		st.RemainingUSD = math.Max(0, effectiveCap-effectiveSpent)
	}

	// Used share & elapsed share.
	usedShare := reading.UsedShare
	elapsedShare := reading.ElapsedShare
	if effectiveCap > 0 {
		dollarShare := clampFloat(effectiveSpent/effectiveCap, 0, 1)
		if dollarShare > usedShare {
			usedShare = dollarShare
		}
		if len(st.Windows) == 0 && p.ResetDay >= 1 && p.ResetDay <= 31 {
			start, end := MonthlyCycleBounds(now, p.ResetDay)
			if end.After(start) {
				elapsedShare = clampFloat(float64(now.Sub(start))/float64(end.Sub(start)), 0, 1)
			}
			if st.ResetsAt == nil {
				t := end
				st.ResetsAt = &t
			}
		}
	} else if !reading.HasQuotaUsage {
		elapsedShare = 0.5
	}
	st.UsedShare = clampFloat(usedShare, 0, 1)
	st.ElapsedShare = clampFloat(elapsedShare, 0, 1)

	// Check pacing across all windows (and overall cap).
	slack := p.PaceSlack
	if slack < 0 {
		slack = 0
	}
	onPace := st.UsedShare <= st.ElapsedShare+slack+paceEpsilon
	for _, w := range st.Windows {
		if w.Exhausted || w.UsedShare > w.ElapsedShare+slack+paceEpsilon {
			onPace = false
			break
		}
	}
	st.OnPace = onPace

	// Eligibility checks.
	if !p.Enabled {
		st.Eligible = false
		st.IneligibleReason = "disabled"
	} else if p.ExpiresAt != nil && !p.ExpiresAt.After(now) {
		st.Eligible = false
		st.IneligibleReason = "credits expired"
	} else if h.CooldownUntil != nil && h.CooldownUntil.After(now) {
		st.Eligible = false
		rem := h.CooldownUntil.Sub(now).Round(time.Second)
		st.IneligibleReason = fmt.Sprintf("cooldown (%s remaining)", rem)
	} else if p.Category == CategoryFree && (h.ConsecutiveErrors >= 2 || h.ErrorRate >= 0.5 || h.HealthScore < 40) {
		st.Eligible = false
		st.IneligibleReason = fmt.Sprintf("unhealthy (score %.0f, err_rate %.0f%%)", h.HealthScore, h.ErrorRate*100)
	} else if reading.Exhausted || (p.Category == CategoryMonthlyCooldown && st.UsedShare >= 1.0) {
		st.Eligible = false
		st.IneligibleReason = "quota exhausted"
	} else if effectiveCap > 0 && (effectiveCap-effectiveSpent-p.ReserveUSD) <= 1e-6 {
		st.Eligible = false
		if effectiveSpent >= effectiveCap {
			st.IneligibleReason = "cap exhausted"
		} else {
			st.IneligibleReason = fmt.Sprintf("reserve reached ($%.2f reserve)", p.ReserveUSD)
		}
	}

	// Assign waterfall bucket 1..6.
	st.WaterfallBucket, st.BucketLabel = assignWaterfallBucket(p.Category, st.OnPace)
	return st
}

func assignWaterfallBucket(cat PoolCategory, onPace bool) (int, string) {
	switch cat {
	case CategoryFree:
		return 1, "free"
	case CategoryMonthlyCooldown:
		if onPace {
			return 2, "monthly_cooldown_on_pace"
		}
		return 4, "monthly_cooldown_ahead"
	case CategoryMonthlyNoCooldown:
		if onPace {
			return 3, "monthly_no_cooldown_on_pace"
		}
		return 5, "monthly_no_cooldown_ahead"
	case CategoryPAYG:
		return 6, "payg"
	default:
		return 6, "payg"
	}
}

func comparePoolStatus(a, b PoolStatus) bool {
	// Eligible pools always sort ahead of ineligible pools for display/selection clarity.
	if a.Eligible != b.Eligible {
		return a.Eligible
	}
	if a.WaterfallBucket != b.WaterfallBucket {
		return a.WaterfallBucket < b.WaterfallBucket
	}
	// Soonest-expiring credits first within the same bucket.
	if a.EffectiveExpiresAt != nil || b.EffectiveExpiresAt != nil {
		if a.EffectiveExpiresAt == nil {
			return false
		}
		if b.EffectiveExpiresAt == nil {
			return true
		}
		if !a.EffectiveExpiresAt.Equal(*b.EffectiveExpiresAt) {
			return a.EffectiveExpiresAt.Before(*b.EffectiveExpiresAt)
		}
	}
	// Higher pool weight wins.
	if a.Pool.Weight != b.Pool.Weight {
		return a.Pool.Weight > b.Pool.Weight
	}
	// Higher health score wins.
	if math.Abs(a.Health.HealthScore-b.Health.HealthScore) > 0.01 {
		return a.Health.HealthScore > b.Health.HealthScore
	}
	// Lower used share (more headroom) wins.
	if math.Abs(a.UsedShare-b.UsedShare) > 0.001 {
		return a.UsedShare < b.UsedShare
	}
	return a.Pool.ID < b.Pool.ID
}

// SelectRoute evaluates pool economy and chooses the best route per tier for the requested harnesses.
func (e *Engine) SelectRoute(ctx context.Context, harnesses []string, now time.Time) (*RouteResponse, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	statuses, err := e.RankPools(ctx, now)
	if err != nil {
		return nil, err
	}
	models, err := e.store.ListModels()
	if err != nil {
		return nil, err
	}
	excludedList, err := e.store.ListExcludedModels()
	if err != nil {
		return nil, err
	}
	excluded := make(map[string]bool, len(excludedList))
	for _, id := range excludedList {
		excluded[strings.ToLower(strings.TrimSpace(id))] = true
	}

	normalizedHarnesses, harnessSet := normalizeHarnesses(harnesses)
	tiers := make(map[Tier]TierSelection, len(AllTiers))

	for _, tier := range AllTiers {
		sel := TierSelection{
			Tier:       tier,
			Candidates: []CandidateModel{},
		}

		for _, st := range statuses {
			if !st.Eligible {
				continue
			}
			poolModels := filterTierPoolModels(models, tier, st.Pool.ID, harnessSet, excluded)
			if len(poolModels) == 0 {
				continue
			}

			winHarness, candidates := pickHarnessCandidates(poolModels, st.Pool.ID)
			if len(candidates) == 0 {
				continue
			}
			winProvider := candidates[0].Provider
			winOverlay := candidates[0].Overlay

			if !sel.Available {
				sel.Available = true
				sel.Pool = st.Pool.ID
				sel.Category = st.Pool.Category
				sel.Bucket = st.BucketLabel
				sel.Harness = winHarness
				sel.Provider = winProvider
				sel.Overlay = winOverlay
				sel.Preset = winOverlay
				sel.UsedShare = st.UsedShare
				sel.OnPace = st.OnPace
				sel.Candidates = candidates
				sel.Reason = fmt.Sprintf("bucket=%s rank=#%d used=%.0f%%", st.BucketLabel, st.PreferenceRank, st.UsedShare*100)
			} else {
				sel.Fallbacks = append(sel.Fallbacks, TierFallback{
					Pool:       st.Pool.ID,
					Category:   st.Pool.Category,
					Bucket:     st.BucketLabel,
					Harness:    winHarness,
					Provider:   winProvider,
					Overlay:    winOverlay,
					UsedShare:  st.UsedShare,
					OnPace:     st.OnPace,
					Candidates: candidates,
				})
			}
		}

		if !sel.Available {
			sel.Reason = "no eligible pool with matching harness models"
		}
		tiers[tier] = sel
	}

	return &RouteResponse{
		GeneratedAt:        now,
		RequestedHarnesses: normalizedHarnesses,
		Tiers:              tiers,
		Pools:              statuses,
	}, nil
}

func normalizeHarnesses(harnesses []string) ([]string, map[string]bool) {
	if len(harnesses) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(harnesses))
	out := make([]string, 0, len(harnesses))
	for _, h := range harnesses {
		for _, part := range strings.Split(h, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, seen
}

func filterTierPoolModels(models []RouteModel, tier Tier, poolID string, harnessSet map[string]bool, excluded map[string]bool) []RouteModel {
	var out []RouteModel
	for _, m := range models {
		if !m.Enabled || m.Tier != tier || m.PoolID != poolID {
			continue
		}
		if excluded[strings.ToLower(strings.TrimSpace(m.ModelID))] {
			continue
		}
		if len(harnessSet) > 0 && !harnessSet[m.Harness] {
			continue
		}
		out = append(out, m)
	}
	return out
}

func pickHarnessCandidates(poolModels []RouteModel, poolID string) (string, []CandidateModel) {
	byHarness := make(map[string][]RouteModel)
	overridePi := false
	for _, m := range poolModels {
		byHarness[m.Harness] = append(byHarness[m.Harness], m)
		if m.Harness != "pi" && m.OverridePiPreference {
			overridePi = true
		}
	}

	var chosenHarness string
	if piModels, hasPi := byHarness["pi"]; hasPi && !overridePi && len(piModels) > 0 {
		chosenHarness = "pi"
	} else {
		bestPriority := math.MinInt
		for h, list := range byHarness {
			sortRouteModels(list)
			byHarness[h] = list
			topPrio := list[0].Priority
			if chosenHarness == "" || topPrio > bestPriority || (topPrio == bestPriority && h < chosenHarness) {
				chosenHarness = h
				bestPriority = topPrio
			}
		}
	}

	chosen := byHarness[chosenHarness]
	sortRouteModels(chosen)
	candidates := make([]CandidateModel, 0, len(chosen))
	for _, m := range chosen {
		candidates = append(candidates, CandidateModel{
			Model:           m.ModelID,
			Pool:            poolID,
			Harness:         m.Harness,
			Provider:        m.Provider,
			Overlay:         m.Overlay,
			Billing:         m.Billing,
			Description:     m.Description,
			Priority:        m.Priority,
			InputCostPer1M:  m.InputCostPer1M,
			OutputCostPer1M: m.OutputCostPer1M,
		})
	}
	return chosenHarness, candidates
}

func sortRouteModels(models []RouteModel) {
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Priority != models[j].Priority {
			return models[i].Priority > models[j].Priority
		}
		return models[i].ID < models[j].ID
	})
}
