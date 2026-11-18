package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"
)

var (
	ErrChutesUnauthorized    = errors.New("chutes: unauthorized - invalid API key")
	ErrChutesRateLimited     = errors.New("chutes: rate limited")
	ErrChutesServerError     = errors.New("chutes: server error")
	ErrChutesNetworkError    = errors.New("chutes: network error")
	ErrChutesInvalidResponse = errors.New("chutes: invalid response")
)

const (
	defaultChutesAPIBaseURL    = "https://api.chutes.ai"
	defaultChutesModelsURL     = "https://llm.chutes.ai/v1/models"
)

// ChutesClient is an HTTP client for Chutes usage & model discovery.
type ChutesClient struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	modelsURL  string
	logger     *slog.Logger
}

// ChutesOption configures a ChutesClient.
type ChutesOption func(*ChutesClient)

// WithChutesBaseURL sets a custom base URL (for testing or proxies).
func WithChutesBaseURL(u string) ChutesOption {
	return func(c *ChutesClient) {
		if strings.TrimSpace(u) != "" {
			c.baseURL = strings.TrimRight(strings.TrimSpace(u), "/")
		}
	}
}

// WithChutesModelsURL sets a custom models URL (for testing).
func WithChutesModelsURL(u string) ChutesOption {
	return func(c *ChutesClient) {
		if strings.TrimSpace(u) != "" {
			c.modelsURL = strings.TrimSpace(u)
		}
	}
}

// NewChutesClient creates a new Chutes API client.
func NewChutesClient(apiKey string, logger *slog.Logger, opts ...ChutesOption) *ChutesClient {
	if logger == nil {
		logger = slog.Default()
	}
	c := &ChutesClient{
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:          1,
				MaxIdleConnsPerHost:   1,
				ResponseHeaderTimeout: 20 * time.Second,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
			},
		},
		apiKey:    strings.TrimSpace(apiKey),
		baseURL:   defaultChutesAPIBaseURL,
		modelsURL: defaultChutesModelsURL,
		logger:    logger,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// FetchUsage queries Chutes usage/quota endpoints and returns a ChutesSnapshot.
func (c *ChutesClient) FetchUsage(ctx context.Context) (*ChutesSnapshot, error) {
	if c.apiKey == "" {
		return nil, ErrChutesUnauthorized
	}

	extra := map[string]interface{}{}
	var usedUSD, limitUSD, usedPct, balanceUSD float64
	var resetsAt *time.Time
	var successCount int
	var lastErr error

	endpoints := []string{
		"/users/me",
		"/users/me/subscription_usage",
		"/users/me/quotas",
		"/users/me/quota_usage/me",
	}
	for _, ep := range endpoints {
		data, err := c.getJSON(ctx, c.baseURL+ep)
		if err != nil {
			lastErr = err
			continue
		}
		successCount++
		extra[strings.TrimPrefix(ep, "/")] = data
		extractChutesMetrics(data, &usedUSD, &limitUSD, &usedPct, &balanceUSD, &resetsAt)
	}

	if info, err := c.getJSON(ctx, c.baseURL+"/idp/userinfo"); err == nil {
		successCount++
		if m, ok := info.(map[string]interface{}); ok {
			for _, k := range []string{"sub", "user_id", "id"} {
				if uid, okS := m[k].(string); okS && strings.TrimSpace(uid) != "" {
					if userObj, uErr := c.getJSON(ctx, c.baseURL+"/users/"+strings.TrimSpace(uid)); uErr == nil {
						extra["user"] = userObj
						extractChutesMetrics(userObj, &usedUSD, &limitUSD, &usedPct, &balanceUSD, &resetsAt)
					}
					break
				}
			}
		}
	} else if lastErr == nil {
		lastErr = err
	}

	if successCount == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, ErrChutesNetworkError
	}

	if usedPct <= 0 && limitUSD > 0 && usedUSD > 0 {
		usedPct = (usedUSD / limitUSD) * 100
	}
	if usedUSD <= 0 && limitUSD > 0 && usedPct > 0 {
		usedUSD = (usedPct / 100.0) * limitUSD
	}
	if usedPct < 0 {
		usedPct = 0
	}
	if usedPct > 100 {
		usedPct = 100
	}

	rawBytes, _ := json.Marshal(extra)
	return &ChutesSnapshot{
		CapturedAt:  time.Now().UTC(),
		UsedUSD:     usedUSD,
		LimitUSD:    limitUSD,
		UsedPercent: usedPct,
		BalanceUSD:  balanceUSD,
		ResetsAt:    resetsAt,
		RawJSON:     string(rawBytes),
	}, nil
}

// FetchModels queries Chutes' OpenAI-compatible /v1/models endpoint.
func (c *ChutesClient) FetchModels(ctx context.Context) ([]ProviderModel, error) {
	if c.apiKey == "" {
		return nil, ErrChutesUnauthorized
	}
	raw, err := c.getJSON(ctx, c.modelsURL)
	if err != nil {
		return nil, err
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, ErrChutesInvalidResponse
	}
	items, _ := m["data"].([]interface{})
	out := make([]ProviderModel, 0, len(items))
	for _, it := range items {
		obj, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := obj["id"].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		ownedBy, _ := obj["owned_by"].(string)
		ctxLen := intFromAny(obj["context_length"])
		maxOut := intFromAny(obj["max_output_length"])
		out = append(out, ProviderModel{
			ID:            id,
			Name:          id,
			Provider:      "chutes",
			OwnedBy:       ownedBy,
			ContextWindow: ctxLen,
			MaxOutput:     maxOut,
		})
	}
	return out, nil
}

func (c *ChutesClient) getJSON(ctx context.Context, fullURL string) (interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChutesNetworkError, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChutesNetworkError, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChutesNetworkError, err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrChutesUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrChutesRateLimited
	case resp.StatusCode >= 500:
		return nil, ErrChutesServerError
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: HTTP %d", ErrChutesInvalidResponse, resp.StatusCode)
	}

	var out interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChutesInvalidResponse, err)
	}
	return out, nil
}

func extractChutesMetrics(v interface{}, usedUSD, limitUSD, usedPct, balanceUSD *float64, resetsAt **time.Time) {
	switch val := v.(type) {
	case map[string]interface{}:
		for _, k := range []string{"used_usd", "monthly_used_usd", "monthly_usage_usd", "spent_usd", "usage_usd"} {
			if f, ok := floatFromMap(val, k); ok && f > *usedUSD {
				*usedUSD = f
			}
		}
		for _, k := range []string{"limit_usd", "monthly_limit_usd", "cap_usd"} {
			if f, ok := floatFromMap(val, k); ok && f > *limitUSD {
				*limitUSD = f
			}
		}
		for _, k := range []string{"balance", "balance_usd", "credits", "remaining_usd"} {
			if f, ok := floatFromMap(val, k); ok && f > *balanceUSD {
				*balanceUSD = f
			}
		}
		for _, k := range []string{"monthly_used_pct", "monthly_utilization", "utilization", "used_percent", "pct_used", "usage_pct"} {
			if f, ok := floatFromMap(val, k); ok && f > *usedPct {
				*usedPct = f
			}
		}
		if mu, okU := firstFloatFromMap(val, "monthly_usage", "used", "usage"); okU {
			if ml, okL := firstFloatFromMap(val, "monthly_limit", "limit", "quota"); okL && ml > 0 {
				pct := math.Min(100, math.Max(0, (mu/ml)*100))
				if pct > *usedPct {
					*usedPct = pct
				}
			}
		}
		for _, k := range []string{"resets_at", "reset_at", "monthly_resets_at", "period_end"} {
			if s, ok := val[k].(string); ok && strings.TrimSpace(s) != "" {
				if t, err := time.Parse(time.RFC3339, strings.TrimSpace(s)); err == nil {
					u := t.UTC()
					if *resetsAt == nil || u.Before(**resetsAt) {
						*resetsAt = &u
					}
				}
			}
		}
		for _, child := range val {
			extractChutesMetrics(child, usedUSD, limitUSD, usedPct, balanceUSD, resetsAt)
		}
	case []interface{}:
		for _, item := range val {
			extractChutesMetrics(item, usedUSD, limitUSD, usedPct, balanceUSD, resetsAt)
		}
	}
}

func floatFromMap(m map[string]interface{}, key string) (float64, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	f, ok := raw.(float64)
	if !ok || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

func firstFloatFromMap(m map[string]interface{}, keys ...string) (float64, bool) {
	for _, k := range keys {
		if f, ok := floatFromMap(m, k); ok {
			return f, true
		}
	}
	return 0, false
}

func intFromAny(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}
