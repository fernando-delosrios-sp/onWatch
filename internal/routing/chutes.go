package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultChutesBaseURL        = "https://api.chutes.ai"
	defaultOpenRouterKeyBaseURL = "https://openrouter.ai"
	settingChutesAPIKey         = "chutes_api_key"
	settingOpenRouterPAYGKey    = "openrouter_payg_api_key"
	settingGoogleCloudProject   = "google_cloud_project"
)

// ChutesClient queries api.chutes.ai for subscription usage, quotas, and user balance.
type ChutesClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewChutesClient constructs a Chutes API client.
func NewChutesClient(baseURL string, httpClient *http.Client) *ChutesClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultChutesBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &ChutesClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

// FetchSnapshot queries Chutes usage endpoints and normalizes a ProbeSnapshot for the "chutes" pool.
func (c *ChutesClient) FetchSnapshot(ctx context.Context, apiKey string, fallbackCapUSD float64, now time.Time) (ProbeSnapshot, error) {
	if strings.TrimSpace(apiKey) == "" {
		return ProbeSnapshot{}, fmt.Errorf("chutes api key is empty")
	}

	extra := map[string]interface{}{}
	var usedUSD, limitUSD, usedPct float64
	var resetsAt *time.Time
	var successCount int

	// 1. /users/me/subscription_usage (rolling 4h & monthly usage vs limits)
	if data, err := c.getJSON(ctx, "/users/me/subscription_usage", apiKey); err == nil {
		successCount++
		extra["subscription_usage"] = data
		extractChutesNumbers(data, &usedUSD, &limitUSD, &usedPct, &resetsAt)
	}

	// 2. /users/me/quotas
	if data, err := c.getJSON(ctx, "/users/me/quotas", apiKey); err == nil {
		successCount++
		extra["quotas"] = data
		extractChutesNumbers(data, &usedUSD, &limitUSD, &usedPct, &resetsAt)
	}

	// 3. /users/me/quota_usage/me
	if data, err := c.getJSON(ctx, "/users/me/quota_usage/me", apiKey); err == nil {
		successCount++
		extra["quota_usage"] = data
		extractChutesNumbers(data, &usedUSD, &limitUSD, &usedPct, &resetsAt)
	}

	// 4. /idp/userinfo -> optionally /users/{user_id} for balance / account usage
	if info, err := c.getJSON(ctx, "/idp/userinfo", apiKey); err == nil {
		successCount++
		if m, ok := info.(map[string]interface{}); ok {
			uid := firstString(m, "sub", "user_id", "id")
			if uid != "" {
				if userObj, uErr := c.getJSON(ctx, "/users/"+uid, apiKey); uErr == nil {
					extra["user"] = userObj
					extractChutesNumbers(userObj, &usedUSD, &limitUSD, &usedPct, &resetsAt)
				}
			}
		}
	}

	if successCount == 0 {
		return ProbeSnapshot{}, fmt.Errorf("chutes probe failed: all endpoints returned errors")
	}

	if limitUSD <= 0 {
		limitUSD = fallbackCapUSD
	}
	if usedPct <= 0 && limitUSD > 0 && usedUSD > 0 {
		usedPct = clampFloat((usedUSD/limitUSD)*100, 0, 100)
	}
	if usedUSD <= 0 && limitUSD > 0 && usedPct > 0 {
		usedUSD = (usedPct / 100.0) * limitUSD
	}

	extraBytes, _ := json.Marshal(extra)
	return ProbeSnapshot{
		ProbeSource: "chutes",
		PoolID:      "chutes",
		UsedUSD:     usedUSD,
		LimitUSD:    limitUSD,
		UsedPercent: clampFloat(usedPct, 0, 100),
		ResetsAt:    resetsAt,
		ExtraJSON:   string(extraBytes),
		CapturedAt:  now.UTC(),
	}, nil
}

func (c *ChutesClient) getJSON(ctx context.Context, path, apiKey string) (interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("chutes %s status %d", path, resp.StatusCode)
	}
	var out interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func extractChutesNumbers(v interface{}, usedUSD, limitUSD, usedPct *float64, resetsAt **time.Time) {
	switch val := v.(type) {
	case map[string]interface{}:
		// Direct USD fields
		if u, ok := pickFloat(val, "used_usd", "monthly_used_usd", "monthly_usage_usd", "spent_usd", "usage_usd"); ok && u > *usedUSD {
			*usedUSD = u
		}
		if l, ok := pickFloat(val, "limit_usd", "monthly_limit_usd", "cap_usd"); ok && l > *limitUSD {
			*limitUSD = l
		}
		if pct, ok := pickFloat(val, "monthly_used_pct", "monthly_utilization", "utilization", "used_percent", "pct_used", "usage_pct"); ok {
			if pct > 0 && pct <= 1.0 && strings.Contains(strings.ToLower(fmt.Sprint(val)), "ratio") {
				pct *= 100
			}
			if pct > *usedPct {
				*usedPct = pct
			}
		}
		// Compute ratio if monthly_usage & monthly_limit are present
		if mu, okU := pickFloat(val, "monthly_usage", "used", "usage"); okU {
			if ml, okL := pickFloat(val, "monthly_limit", "limit", "quota"); okL && ml > 0 {
				pct := clampFloat((mu/ml)*100, 0, 100)
				if pct > *usedPct {
					*usedPct = pct
				}
			}
		}
		if u4, okU := pickFloat(val, "four_hour_usage", "4h_usage"); okU {
			if l4, okL := pickFloat(val, "four_hour_limit", "4h_limit"); okL && l4 > 0 {
				pct := clampFloat((u4/l4)*100, 0, 100)
				if pct > *usedPct {
					*usedPct = pct
				}
			}
		}
		if rs := firstString(val, "resets_at", "reset_at", "monthly_resets_at", "period_end"); rs != "" {
			if t := parseTimeFlexible(rs); t != nil {
				if *resetsAt == nil || t.Before(**resetsAt) {
					*resetsAt = t
				}
			}
		}
		for _, child := range val {
			extractChutesNumbers(child, usedUSD, limitUSD, usedPct, resetsAt)
		}
	case []interface{}:
		for _, item := range val {
			extractChutesNumbers(item, usedUSD, limitUSD, usedPct, resetsAt)
		}
	}
}

func pickFloat(m map[string]interface{}, keys ...string) (float64, bool) {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			if f, okF := raw.(float64); okF && !math.IsNaN(f) {
				return f, true
			}
		}
	}
	return 0, false
}

func firstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			if s, okS := raw.(string); okS && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// OpenRouterKeyClient optionally probes a dedicated OpenRouter PAYG key via /api/v1/key.
type OpenRouterKeyClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewOpenRouterKeyClient constructs an OpenRouter key client.
func NewOpenRouterKeyClient(baseURL string, httpClient *http.Client) *OpenRouterKeyClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOpenRouterKeyBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &OpenRouterKeyClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

// FetchPAYGSnapshot calls /api/v1/key and returns a ProbeSnapshot for openrouter-payg.
func (c *OpenRouterKeyClient) FetchPAYGSnapshot(ctx context.Context, apiKey string, fallbackCapUSD float64, now time.Time) (ProbeSnapshot, error) {
	if strings.TrimSpace(apiKey) == "" {
		return ProbeSnapshot{}, fmt.Errorf("openrouter payg key is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/key", nil)
	if err != nil {
		return ProbeSnapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ProbeSnapshot{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return ProbeSnapshot{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProbeSnapshot{}, fmt.Errorf("openrouter key status %d", resp.StatusCode)
	}

	var parsed struct {
		Data struct {
			Label          string   `json:"label"`
			Usage          float64  `json:"usage"`
			UsageMonthly   float64  `json:"usage_monthly"`
			Limit          *float64 `json:"limit"`
			LimitRemaining *float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ProbeSnapshot{}, err
	}

	used := parsed.Data.UsageMonthly
	if used == 0 && parsed.Data.Usage > 0 {
		used = parsed.Data.Usage
	}
	limit := fallbackCapUSD
	if parsed.Data.Limit != nil && *parsed.Data.Limit > 0 {
		limit = *parsed.Data.Limit
	}
	pct := 0.0
	if limit > 0 {
		pct = clampFloat((used/limit)*100, 0, 100)
	}
	extraBytes, _ := json.Marshal(map[string]interface{}{
		"label":           parsed.Data.Label,
		"usage_monthly":   parsed.Data.UsageMonthly,
		"limit":           parsed.Data.Limit,
		"limit_remaining": parsed.Data.LimitRemaining,
	})

	return ProbeSnapshot{
		ProbeSource: "openrouter",
		PoolID:      "openrouter-payg",
		UsedUSD:     used,
		LimitUSD:    limit,
		UsedPercent: pct,
		ExtraJSON:   string(extraBytes),
		CapturedAt:  now.UTC(),
	}, nil
}

// Poller periodically refreshes external routing probes (Chutes, OpenRouter PAYG, Google Cloud Monitoring).
type Poller struct {
	store      *Store
	chutes     *ChutesClient
	openrouter *OpenRouterKeyClient
	google     *GoogleMonitoringClient
	logger     *slog.Logger
	interval   time.Duration

	mu        sync.Mutex
	lastError map[string]string
}

// NewPoller creates a routing background poller.
func NewPoller(store *Store, logger *slog.Logger, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Poller{
		store:      store,
		chutes:     NewChutesClient("", nil),
		openrouter: NewOpenRouterKeyClient("", nil),
		google:     NewGoogleMonitoringClient("", nil),
		logger:     logger,
		interval:   interval,
		lastError:  make(map[string]string),
	}
}

// Run starts the polling loop until ctx is canceled.
func (p *Poller) Run(ctx context.Context) {
	if p == nil || p.store == nil {
		return
	}
	p.PollAll(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.PollAll(ctx)
		}
	}
}

// PollAll polls all configured external routing probes.
func (p *Poller) PollAll(ctx context.Context) {
	_, _ = p.ProbePool(ctx, "chutes")
	_, _ = p.ProbePool(ctx, "openrouter-payg")
	_, _ = p.ProbePool(ctx, "google-ai-studio")
}

// ProbePool runs an immediate probe for a specific pool ID if supported.
func (p *Poller) ProbePool(ctx context.Context, poolID string) (*ProbeSnapshot, error) {
	if p == nil || p.store == nil {
		return nil, fmt.Errorf("poller not initialized")
	}
	now := time.Now().UTC()
	pool, err := p.store.GetPool(poolID)
	if err != nil {
		return nil, err
	}
	capUSD := 0.0
	if pool != nil {
		capUSD = pool.CapUSD
	}

	switch poolID {
	case "chutes":
		key := p.resolveSecret(settingChutesAPIKey, "CHUTES_API_KEY")
		if key == "" {
			return nil, nil
		}
		snap, err := p.chutes.FetchSnapshot(ctx, key, capUSD, now)
		if err != nil {
			p.recordError(poolID, err)
			return nil, err
		}
		p.clearError(poolID)
		if err := p.store.InsertProbeSnapshot(snap); err != nil {
			return nil, err
		}
		return &snap, nil

	case "openrouter-payg":
		key := p.resolveSecret(settingOpenRouterPAYGKey, "OPENROUTER_PAYG_API_KEY")
		if key == "" {
			return nil, nil
		}
		snap, err := p.openrouter.FetchPAYGSnapshot(ctx, key, capUSD, now)
		if err != nil {
			p.recordError(poolID, err)
			return nil, err
		}
		p.clearError(poolID)
		if err := p.store.InsertProbeSnapshot(snap); err != nil {
			return nil, err
		}
		return &snap, nil

	case "google-ai-studio":
		project := p.resolveSecret(settingGoogleCloudProject, "GOOGLE_CLOUD_PROJECT")
		token, _ := resolveGoogleAccessToken()
		if project == "" || token == "" {
			return nil, nil
		}
		snap, err := p.google.FetchRequestMetrics(ctx, project, token, now)
		if err != nil {
			p.recordError(poolID, err)
			return nil, err
		}
		p.clearError(poolID)
		if err := p.store.InsertProbeSnapshot(snap); err != nil {
			return nil, err
		}
		return &snap, nil

	default:
		return nil, nil
	}
}

// GetProbeSettings returns non-secret masked probe configuration.
func (p *Poller) GetProbeSettings() ProbeSettings {
	chutesKey := p.resolveSecret(settingChutesAPIKey, "CHUTES_API_KEY")
	orKey := p.resolveSecret(settingOpenRouterPAYGKey, "OPENROUTER_PAYG_API_KEY")
	project := p.resolveSecret(settingGoogleCloudProject, "GOOGLE_CLOUD_PROJECT")
	_, tokenSrc := resolveGoogleAccessToken()

	return ProbeSettings{
		ChutesAPIKeyConfigured:      chutesKey != "",
		ChutesAPIKeyMasked:          maskSecret(chutesKey),
		OpenRouterPAYGKeyConfigured: orKey != "",
		OpenRouterPAYGKeyMasked:     maskSecret(orKey),
		GoogleCloudProject:          project,
		GoogleMonitoringTokenSource: tokenSrc,
	}
}

// UpdateProbeSettings stores non-empty secret/config updates into routing_settings.
func (p *Poller) UpdateProbeSettings(chutesKey, openrouterPaygKey, googleProject *string) error {
	if p == nil || p.store == nil {
		return fmt.Errorf("store not initialized")
	}
	if chutesKey != nil {
		if err := p.store.SetSetting(settingChutesAPIKey, strings.TrimSpace(*chutesKey)); err != nil {
			return err
		}
	}
	if openrouterPaygKey != nil {
		if err := p.store.SetSetting(settingOpenRouterPAYGKey, strings.TrimSpace(*openrouterPaygKey)); err != nil {
			return err
		}
	}
	if googleProject != nil {
		if err := p.store.SetSetting(settingGoogleCloudProject, strings.TrimSpace(*googleProject)); err != nil {
			return err
		}
	}
	return nil
}

func (p *Poller) resolveSecret(settingKey, envName string) string {
	if p != nil && p.store != nil {
		if v, err := p.store.GetSetting(settingKey); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func (p *Poller) recordError(poolID string, err error) {
	p.mu.Lock()
	p.lastError[poolID] = err.Error()
	p.mu.Unlock()
	if p.logger != nil {
		p.logger.Warn("routing probe error", "pool", poolID, "error", err)
	}
}

func (p *Poller) clearError(poolID string) {
	p.mu.Lock()
	delete(p.lastError, poolID)
	p.mu.Unlock()
}

func maskSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}
