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
	// Respect an explicit user-configured cap when set; otherwise fall back to the provider probe limit.
	effectiveCap := p.CapUSD
	if effectiveCap <= 0 && reading.LimitUSD > 0 {
		effectiveCap = reading.LimitUSD
	}
	effectiveSpent := cycleAdjustedSpent(p, now)
	if reading.HasSpendData && (NormalizeCategory(p.Category) == CategoryPAYG || reading.SpentUSD > effectiveSpent) {
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
		if len(st.Windows) == 0 {
			start, end := CycleBounds(now, p.ResetCycle, p.ResetDay)
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
	} else if reading.Exhausted || (NormalizeCategory(p.Category) == CategorySubscription && st.UsedShare >= 1.0) {
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
	switch NormalizeCategory(cat) {
	case CategoryFree:
		return 1, "free"
	case CategorySubscription:
		if onPace {
			return 2, "subscription_on_pace"
		}
		return 4, "subscription_ahead"
	case CategoryAllowance:
		if onPace {
			return 3, "allowance_on_pace"
		}
		return 5, "allowance_ahead"
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
	return e.SelectRouteWithOptions(ctx, RouteOptions{Harnesses: harnesses}, now)
}

// SelectRouteWithOptions evaluates pool economy and chooses routes per tier with optional tier, sticky pool, and exclude filters.
func (e *Engine) SelectRouteWithOptions(ctx context.Context, opts RouteOptions, now time.Time) (*RouteResponse, error) {
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
	excluded := make(map[string]bool, len(excludedList)+len(opts.Exclude))
	for _, id := range excludedList {
		if k := strings.ToLower(strings.TrimSpace(id)); k != "" {
			excluded[k] = true
		}
	}
	for _, raw := range opts.Exclude {
		for _, part := range strings.Split(raw, ",") {
			if k := strings.ToLower(strings.TrimSpace(part)); k != "" {
				excluded[k] = true
			}
		}
	}

	targetTiers := AllTiers
	if opts.Tier != "" && ValidTier(opts.Tier) {
		targetTiers = []Tier{opts.Tier}
	}

	stickyReq := strings.TrimSpace(opts.StickyPool)
	orderedStatuses := statuses
	stickyEligible := false
	if stickyReq != "" {
		for _, st := range statuses {
			if st.Pool.ID == stickyReq && st.Eligible && !(opts.ExcludeFree && st.Pool.Category == CategoryFree) {
				stickyEligible = true
				break
			}
		}
		if stickyEligible {
			reordered := make([]PoolStatus, 0, len(statuses))
			for _, st := range statuses {
				if st.Pool.ID == stickyReq {
					reordered = append(reordered, st)
				}
			}
			for _, st := range statuses {
				if st.Pool.ID != stickyReq {
					reordered = append(reordered, st)
				}
			}
			orderedStatuses = reordered
		}
	}

	normalizedHarnesses, harnessSet := normalizeHarnesses(opts.Harnesses)
	tiers := make(map[Tier][]CandidateModel, len(targetTiers))
	tierSelections := make(map[Tier]TierSelection, len(targetTiers))
	stickyHonored := false

	for _, tier := range targetTiers {
		sel := TierSelection{
			Tier:       tier,
			Candidates: []CandidateModel{},
		}
		routeCandidates := []CandidateModel{}

		for _, st := range orderedStatuses {
			if !st.Eligible {
				continue
			}
			if opts.ExcludeFree && st.Pool.Category == CategoryFree {
				continue
			}
			poolModels := filterTierPoolModels(models, tier, st.Pool.ID, harnessSet, excluded, opts.ExcludeFree)
			if len(poolModels) == 0 {
				continue
			}

			winHarness, candidates := pickHarnessCandidates(poolModels, st)
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
				if stickyEligible && st.Pool.ID == stickyReq {
					stickyHonored = true
				}
				routeCandidates = normalizeCandidatesForRouteResponse(candidates)
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
		tiers[tier] = routeCandidates
		tierSelections[tier] = sel
	}

	resp := &RouteResponse{
		GeneratedAt:        now,
		RequestedHarnesses: normalizedHarnesses,
		Tiers:              tiers,
		TierSelections:     tierSelections,
		Pools:              statuses,
		Stale:              false,
	}
	if stickyHonored {
		resp.StickyPool = stickyReq
	}
	return resp, nil
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

func isModelExcluded(m RouteModel, excluded map[string]bool) bool {
	if len(excluded) == 0 {
		return false
	}
	if excluded[strings.ToLower(strings.TrimSpace(m.ModelID))] {
		return true
	}
	if m.Provider != "" && excluded[strings.ToLower(strings.TrimSpace(m.Provider+"/"+m.ModelID))] {
		return true
	}
	if m.Harness != "" && excluded[strings.ToLower(strings.TrimSpace(m.Harness+"/"+m.ModelID))] {
		return true
	}
	if m.Overlay != "" {
		ov := strings.ToLower(strings.TrimSpace(m.Overlay))
		if excluded[ov] || excluded["openrouter/"+ov] {
			return true
		}
	}
	return false
}

func isFreeRouteModel(m RouteModel) bool {
	if strings.EqualFold(strings.TrimSpace(m.Billing), "free") {
		return true
	}
	mid := strings.ToLower(strings.TrimSpace(m.ModelID))
	if strings.HasSuffix(mid, ":free") || mid == "openrouter/free" {
		return true
	}
	ov := strings.ToLower(strings.TrimSpace(m.Overlay))
	return ov == "@preset/code-nvidia" || ov == "@preset/code-fast-free"
}

func filterTierPoolModels(models []RouteModel, tier Tier, poolID string, harnessSet map[string]bool, excluded map[string]bool, excludeFree bool) []RouteModel {
	var out []RouteModel
	for _, m := range models {
		if !m.Enabled || m.Tier != tier || m.PoolID != poolID {
			continue
		}
		if excludeFree && isFreeRouteModel(m) {
			continue
		}
		if isModelExcluded(m, excluded) {
			continue
		}
		if len(harnessSet) > 0 && !harnessSet[m.Harness] {
			continue
		}
		out = append(out, m)
	}
	return out
}

func candidateStatusForBucket(bucket int) string {
	if bucket == 4 || bucket == 5 {
		return "ahead"
	}
	return "ready"
}

func canonicalCandidateID(harness, provider, model string) string {
	if harness == "pi" {
		if provider != "" && strings.HasPrefix(model, provider+"/") {
			return model
		}
		if provider != "" {
			return provider + "/" + model
		}
	}
	return harness + "/" + model
}

func pickHarnessCandidates(poolModels []RouteModel, st PoolStatus) (string, []CandidateModel) {
	byHarness := make(map[string][]RouteModel)
	for _, m := range poolModels {
		byHarness[m.Harness] = append(byHarness[m.Harness], m)
	}

	var chosenHarness string
	bestPriority := math.MinInt
	for h, list := range byHarness {
		sortRouteModels(list)
		byHarness[h] = list
		topPrio := list[0].Priority
		if chosenHarness == "" || topPrio > bestPriority || (topPrio == bestPriority && h == "pi") || (topPrio == bestPriority && chosenHarness != "pi" && h < chosenHarness) {
			chosenHarness = h
			bestPriority = topPrio
		}
	}

	chosen := byHarness[chosenHarness]
	sortRouteModels(chosen)
	statusStr := candidateStatusForBucket(st.WaterfallBucket)
	candidates := make([]CandidateModel, 0, len(chosen))
	for _, m := range chosen {
		modelID := m.ModelID
		provider := m.Provider
		if m.Provider == "openrouter" || st.Pool.RoutingProvider == "openrouter" {
			modelID = GenerateOpenRouterModel(m.Provider, m.ModelID)
			provider = "openrouter"
		}
		inCost := m.InputCostPer1M
		outCost := m.OutputCostPer1M
		if inCost == 0 && outCost == 0 {
			inCost = st.Pool.InputCostPer1M
			outCost = st.Pool.OutputCostPer1M
		}
		billing := m.Billing
		if billing == "" {
			switch NormalizeCategory(st.Pool.Category) {
			case CategoryFree:
				billing = "free"
			case CategoryPAYG:
				billing = "payg"
			default:
				billing = "byok"
			}
		}
		candidates = append(candidates, CandidateModel{
			ID:              canonicalCandidateID(m.Harness, provider, modelID),
			Tier:            m.Tier,
			Model:           modelID,
			Pool:            st.Pool.ID,
			Harness:         m.Harness,
			Provider:        provider,
			Status:          statusStr,
			Weight:          st.Pool.Weight,
			Overlay:         m.Overlay,
			Billing:         billing,
			Description:     m.Description,
			Priority:        m.Priority,
			InputCostPer1M:  inCost,
			OutputCostPer1M: outCost,
		})
	}
	return chosenHarness, candidates
}

// normalizeCandidatesForRouteResponse converts Pi OpenRouter @preset/* routes so Pi resolves them
// via the registered openrouter/@preset/* model IDs while preserving underlying model metadata.
func normalizeCandidatesForRouteResponse(candidates []CandidateModel) []CandidateModel {
	out := make([]CandidateModel, 0, len(candidates))
	byID := make(map[string]int, len(candidates))

	for _, c := range candidates {
		norm := c
		if c.Harness == "pi" && strings.HasPrefix(c.Overlay, "@preset/") {
			norm.UnderlyingModel = c.Model
			norm.UnderlyingModels = []string{c.Model}
			norm.Provider = "openrouter"
			norm.Model = c.Overlay
			norm.ID = "openrouter/" + c.Overlay
		}

		if idx, exists := byID[norm.ID]; exists {
			existing := &out[idx]
			if norm.UnderlyingModel != "" {
				existing.UnderlyingModels = append(existing.UnderlyingModels, norm.UnderlyingModel)
			}
			if norm.Description != "" && !strings.Contains(existing.Description, norm.Description) {
				if existing.Description != "" {
					existing.Description += "; " + norm.Description
				} else {
					existing.Description = norm.Description
				}
			}
			continue
		}

		byID[norm.ID] = len(out)
		out = append(out, norm)
	}
	return out
}

func sortRouteModels(models []RouteModel) {
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Priority != models[j].Priority {
			return models[i].Priority > models[j].Priority
		}
		return models[i].ID < models[j].ID
	})
}
