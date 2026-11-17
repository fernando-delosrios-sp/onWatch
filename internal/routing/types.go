package routing

import "time"

// PoolCategory defines the economy category of a quota pool.
type PoolCategory string

const (
	CategoryFree              PoolCategory = "free"
	CategoryMonthlyCooldown   PoolCategory = "monthly_cooldown"
	CategoryMonthlyNoCooldown PoolCategory = "monthly_no_cooldown"
	CategoryPAYG              PoolCategory = "payg"
)

// Tier defines the 3-tier model capability classification.
type Tier string

const (
	TierFrontier    Tier = "frontier"
	TierWorkhorse   Tier = "workhorse"
	TierLightweight Tier = "lightweight"
)

// AllTiers lists tiers from highest capability to lowest.
var AllTiers = []Tier{TierFrontier, TierWorkhorse, TierLightweight}

// ValidCategory reports whether c is a recognized pool category.
func ValidCategory(c PoolCategory) bool {
	switch c {
	case CategoryFree, CategoryMonthlyCooldown, CategoryMonthlyNoCooldown, CategoryPAYG:
		return true
	default:
		return false
	}
}

// ValidTier reports whether t is a recognized capability tier.
func ValidTier(t Tier) bool {
	switch t {
	case TierFrontier, TierWorkhorse, TierLightweight:
		return true
	default:
		return false
	}
}

// Pool represents a real billing/quota account tracked by onWatch.
type Pool struct {
	ID          string       `json:"id"`
	DisplayName string       `json:"display_name"`
	Category    PoolCategory `json:"category"`
	Enabled     bool         `json:"enabled"`
	CapUSD      float64      `json:"cap_usd"`
	ReserveUSD  float64      `json:"reserve_usd"`
	SpentUSD    float64      `json:"spent_usd"`
	ResetDay    int          `json:"reset_day"`
	ExpiresAt   *time.Time   `json:"expires_at,omitempty"`
	WindowHours int          `json:"window_hours"`
	PaceSlack   float64      `json:"pace_slack"`
	Weight      int          `json:"weight"`
	ProbeSource string       `json:"probe_source"`
	CycleStart  *time.Time   `json:"cycle_start,omitempty"`
	Notes       string       `json:"notes,omitempty"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// RouteModel represents an enabled/configured model route inside a pool & tier.
type RouteModel struct {
	ID                   int64   `json:"id"`
	Tier                 Tier    `json:"tier"`
	PoolID               string  `json:"pool_id"`
	Harness              string  `json:"harness"`
	Provider             string  `json:"provider"`
	Overlay              string  `json:"overlay,omitempty"`
	ModelID              string  `json:"model_id"`
	Billing              string  `json:"billing"`
	Description          string  `json:"description,omitempty"`
	Priority             int     `json:"priority"`
	OverridePiPreference bool    `json:"override_pi_preference"`
	InputCostPer1M       float64 `json:"input_cost_per_1m"`
	OutputCostPer1M      float64 `json:"output_cost_per_1m"`
	Enabled              bool    `json:"enabled"`
}

// PoolHealth stores caller-driven health, latency, and cooldown state.
type PoolHealth struct {
	PoolID            string     `json:"pool_id"`
	CooldownUntil     *time.Time `json:"cooldown_until,omitempty"`
	ConsecutiveErrors int        `json:"consecutive_errors"`
	Last429At         *time.Time `json:"last_429_at,omitempty"`
	LastBadAt         *time.Time `json:"last_bad_at,omitempty"`
	LastOKAt          *time.Time `json:"last_ok_at,omitempty"`
	AvgTTFTMs         float64    `json:"avg_ttft_ms"`
	ErrorRate         float64    `json:"error_rate"`
	HealthScore       float64    `json:"health_score"`
	LastReason        string     `json:"last_reason,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// FeedbackInput is accepted by POST /api/route/feedback.
type FeedbackInput struct {
	Pool             string  `json:"pool"`
	Provider         string  `json:"provider"`
	Harness          string  `json:"harness"`
	Model            string  `json:"model"`
	Tier             string  `json:"tier"`
	Event            string  `json:"event"`
	Outcome          string  `json:"outcome"`
	StatusCode       int     `json:"status_code"`
	TTFTMs           float64 `json:"ttft_ms"`
	LatencyMs        float64 `json:"latency_ms"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	CooldownSeconds  int     `json:"cooldown_seconds"`
	Error            string  `json:"error"`
	Note             string  `json:"note"`
	Session          string  `json:"session"`
	Issue            string  `json:"issue"`

	// CamelCase / alternate field aliases sent by pi-onwatch-jev and similar TypeScript clients.
	StatusCodeCamel       int     `json:"statusCode,omitempty"`
	TTFTMsCamel           float64 `json:"ttftMs,omitempty"`
	LatencyMsCamel        float64 `json:"latencyMs,omitempty"`
	PromptTokensCamel     int64   `json:"promptTokens,omitempty"`
	CompletionTokensCamel int64   `json:"completionTokens,omitempty"`
	CachedTokensCamel     int64   `json:"cachedTokens,omitempty"`
	CostUSDCamel          float64 `json:"costUsd,omitempty"`
	CooldownSecondsCamel  int     `json:"cooldownSeconds,omitempty"`
	Reason                string  `json:"reason,omitempty"`
}

// FeedbackEvent is a persisted row in routing_feedback.
type FeedbackEvent struct {
	ID               int64     `json:"id"`
	PoolID           string    `json:"pool_id"`
	ModelID          string    `json:"model_id,omitempty"`
	Harness          string    `json:"harness,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	Tier             string    `json:"tier,omitempty"`
	Event            string    `json:"event,omitempty"`
	Outcome          string    `json:"outcome"`
	StatusCode       int       `json:"status_code,omitempty"`
	TTFTMs           float64   `json:"ttft_ms,omitempty"`
	LatencyMs        float64   `json:"latency_ms,omitempty"`
	PromptTokens     int64     `json:"prompt_tokens,omitempty"`
	CompletionTokens int64     `json:"completion_tokens,omitempty"`
	CachedTokens     int64     `json:"cached_tokens,omitempty"`
	CostUSD          float64   `json:"cost_usd,omitempty"`
	Note             string    `json:"note,omitempty"`
	Session          string    `json:"session,omitempty"`
	Issue            string    `json:"issue,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// ProbeSnapshot stores a captured usage reading from external probes like Chutes or Google AI Studio.
type ProbeSnapshot struct {
	ID          int64     `json:"id"`
	ProbeSource string    `json:"probe_source"`
	PoolID      string    `json:"pool_id"`
	UsedUSD     float64   `json:"used_usd"`
	LimitUSD    float64   `json:"limit_usd"`
	UsedPercent float64   `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	ExtraJSON   string    `json:"extra_json,omitempty"`
	CapturedAt  time.Time `json:"captured_at"`
}

// QuotaWindow captures an individual quota bucket (e.g. 5h, 7d, monthly).
type QuotaWindow struct {
	Name         string     `json:"name"`
	UsedPercent  float64    `json:"used_percent"`
	UsedUSD      float64    `json:"used_usd,omitempty"`
	LimitUSD     float64    `json:"limit_usd,omitempty"`
	ResetsAt     *time.Time `json:"resets_at,omitempty"`
	WindowHours  int        `json:"window_hours,omitempty"`
	ElapsedShare float64    `json:"elapsed_share"`
	UsedShare    float64    `json:"used_share"`
	Exhausted    bool       `json:"exhausted"`
}

// PoolStatus combines configuration, probe readings, health, pacing, and waterfall ranking.
type PoolStatus struct {
	Pool               Pool          `json:"pool"`
	Health             PoolHealth    `json:"health"`
	PreferenceRank     int           `json:"preference_rank"`
	WaterfallBucket    int           `json:"waterfall_bucket"`
	BucketLabel        string        `json:"bucket_label"`
	Eligible           bool          `json:"eligible"`
	IneligibleReason   string        `json:"ineligible_reason,omitempty"`
	UsedShare          float64       `json:"used_share"`
	ElapsedShare       float64       `json:"elapsed_share"`
	OnPace             bool          `json:"on_pace"`
	EffectiveCapUSD    float64       `json:"effective_cap_usd"`
	EffectiveSpentUSD  float64       `json:"effective_spent_usd"`
	RemainingUSD       float64       `json:"remaining_usd"`
	ResetsAt           *time.Time    `json:"resets_at,omitempty"`
	EffectiveExpiresAt *time.Time    `json:"effective_expires_at,omitempty"`
	Windows            []QuotaWindow `json:"windows,omitempty"`
	ProbeStatus        string        `json:"probe_status"`
	ProbeSummary       string        `json:"probe_summary,omitempty"`
	LastPolledAt       *time.Time    `json:"last_polled_at,omitempty"`
}

// CandidateModel is one candidate route returned to callers for Jev selection.
type CandidateModel struct {
	ID               string   `json:"id,omitempty"`
	Tier             Tier     `json:"tier,omitempty"`
	Model            string   `json:"model"`
	Pool             string   `json:"pool"`
	Harness          string   `json:"harness"`
	Provider         string   `json:"provider"`
	Status           string   `json:"status,omitempty"`
	Weight           int      `json:"weight,omitempty"`
	Overlay          string   `json:"overlay,omitempty"`
	UnderlyingModel  string   `json:"underlying_model,omitempty"`
	UnderlyingModels []string `json:"underlying_models,omitempty"`
	Billing          string   `json:"billing"`
	Description      string   `json:"description,omitempty"`
	Priority         int      `json:"priority"`
	InputCostPer1M   float64  `json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M  float64  `json:"output_cost_per_1m,omitempty"`
}

// TierSelection is the winning route for a single tier.
type TierSelection struct {
	Tier       Tier             `json:"tier"`
	Available  bool             `json:"available"`
	Pool       string           `json:"pool,omitempty"`
	Category   PoolCategory     `json:"category,omitempty"`
	Bucket     string           `json:"bucket,omitempty"`
	Harness    string           `json:"harness,omitempty"`
	Provider   string           `json:"provider,omitempty"`
	Overlay    string           `json:"overlay,omitempty"`
	Preset     string           `json:"preset,omitempty"`
	UsedShare  float64          `json:"used_share,omitempty"`
	OnPace     bool             `json:"on_pace,omitempty"`
	Candidates []CandidateModel `json:"candidates"`
	Fallbacks  []TierFallback   `json:"fallbacks,omitempty"`
	Reason     string           `json:"reason,omitempty"`
}

// TierFallback summarizes alternative eligible pools for a tier.
type TierFallback struct {
	Pool       string           `json:"pool"`
	Category   PoolCategory     `json:"category"`
	Bucket     string           `json:"bucket"`
	Harness    string           `json:"harness"`
	Provider   string           `json:"provider"`
	Overlay    string           `json:"overlay,omitempty"`
	UsedShare  float64          `json:"used_share"`
	OnPace     bool             `json:"on_pace"`
	Candidates []CandidateModel `json:"candidates"`
}

// RouteOptions controls route selection for GET /api/route.
type RouteOptions struct {
	Harnesses  []string
	Tier       Tier
	StickyPool string
	Exclude    []string
}

// RouteResponse is returned by GET /api/route.
type RouteResponse struct {
	GeneratedAt        time.Time                 `json:"generated_at"`
	RequestedHarnesses []string                  `json:"requested_harnesses,omitempty"`
	Tiers              map[Tier][]CandidateModel `json:"tiers"`
	TierSelections     map[Tier]TierSelection    `json:"tier_selections"`
	Pools              []PoolStatus              `json:"pools"`
	StickyPool         string                    `json:"stickyPool,omitempty"`
	Stale              bool                      `json:"stale"`
}

// ProbeSettings holds non-secret status and configuration for routing probes.
type ProbeSettings struct {
	ChutesAPIKeyConfigured        bool   `json:"chutes_api_key_configured"`
	ChutesAPIKeyMasked            string `json:"chutes_api_key_masked,omitempty"`
	OpenRouterPAYGKeyConfigured   bool   `json:"openrouter_payg_key_configured"`
	OpenRouterPAYGKeyMasked       string `json:"openrouter_payg_key_masked,omitempty"`
	GoogleCloudProject            string `json:"google_cloud_project,omitempty"`
	GoogleMonitoringTokenSource   string `json:"google_monitoring_token_source,omitempty"`
}
