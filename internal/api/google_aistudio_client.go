package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrGoogleAIStudioUnauthorized    = errors.New("googleaistudio: unauthorized - invalid API key")
	ErrGoogleAIStudioRateLimited     = errors.New("googleaistudio: rate limited")
	ErrGoogleAIStudioServerError     = errors.New("googleaistudio: server error")
	ErrGoogleAIStudioNetworkError    = errors.New("googleaistudio: network error")
	ErrGoogleAIStudioInvalidResponse = errors.New("googleaistudio: invalid response")
)

const (
	defaultGoogleAIStudioBaseURL       = "https://generativelanguage.googleapis.com"
	defaultGoogleAIStudioMonitoringURL = "https://monitoring.googleapis.com"
)

// GoogleAIStudioClient is an HTTP client for Google AI Studio (generativelanguage.googleapis.com) + optional GCP Cloud Monitoring.
type GoogleAIStudioClient struct {
	httpClient    *http.Client
	apiKey        string
	project       string
	capUSD        float64
	baseURL       string
	monitoringURL string
	accessToken   string
	logger        *slog.Logger
}

// GoogleAIStudioOption configures a GoogleAIStudioClient.
type GoogleAIStudioOption func(*GoogleAIStudioClient)

// WithGoogleAIStudioBaseURL sets a custom base URL (for testing).
func WithGoogleAIStudioBaseURL(u string) GoogleAIStudioOption {
	return func(c *GoogleAIStudioClient) {
		if strings.TrimSpace(u) != "" {
			c.baseURL = strings.TrimRight(strings.TrimSpace(u), "/")
		}
	}
}

// WithGoogleAIStudioMonitoringURL sets a custom GCP Cloud Monitoring base URL (for testing).
func WithGoogleAIStudioMonitoringURL(u string) GoogleAIStudioOption {
	return func(c *GoogleAIStudioClient) {
		if strings.TrimSpace(u) != "" {
			c.monitoringURL = strings.TrimRight(strings.TrimSpace(u), "/")
		}
	}
}

// WithGoogleAIStudioAccessToken sets an explicit OAuth access token for GCP Cloud Monitoring (for testing).
func WithGoogleAIStudioAccessToken(tok string) GoogleAIStudioOption {
	return func(c *GoogleAIStudioClient) {
		c.accessToken = strings.TrimSpace(tok)
	}
}

// NewGoogleAIStudioClient creates a new Google AI Studio API client.
func NewGoogleAIStudioClient(apiKey, project string, capUSD float64, logger *slog.Logger, opts ...GoogleAIStudioOption) *GoogleAIStudioClient {
	if logger == nil {
		logger = slog.Default()
	}
	if capUSD <= 0 {
		capUSD = 15.0
	}
	c := &GoogleAIStudioClient{
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
		apiKey:        strings.TrimSpace(apiKey),
		project:       strings.TrimSpace(project),
		capUSD:        capUSD,
		baseURL:       defaultGoogleAIStudioBaseURL,
		monitoringURL: defaultGoogleAIStudioMonitoringURL,
		logger:        logger,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// FetchUsage validates the Google AI Studio API key via v1beta/models and optionally queries GCP Cloud Monitoring for 24h request count.
func (c *GoogleAIStudioClient) FetchUsage(ctx context.Context) (*GoogleAIStudioSnapshot, error) {
	if c.apiKey == "" && c.project == "" {
		return nil, ErrGoogleAIStudioUnauthorized
	}

	var modelCount int
	var keyValid bool
	if c.apiKey != "" {
		models, err := c.FetchModels(ctx)
		if err != nil {
			return nil, err
		}
		modelCount = len(models)
		keyValid = true
	}

	var requests24h int64
	var spentUSD float64
	var inputTokens, outputTokens int64
	if c.project != "" {
		tok := c.resolveAccessToken()
		if tok != "" {
			if reqs, err := c.fetchMonitoringRequests24h(ctx, tok); err == nil {
				requests24h = reqs
			}
			inTok, outTok, cost := c.fetchMonitoringMonthlySpend(ctx, tok)
			inputTokens = inTok
			outputTokens = outTok
			spentUSD = cost
		}
	}

	rawBytes, _ := json.Marshal(map[string]interface{}{
		"project":       c.project,
		"requests_24h":  requests24h,
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"spent_usd":     spentUSD,
		"model_count":   modelCount,
		"key_valid":     keyValid,
		"cap_usd":       c.capUSD,
	})

	return &GoogleAIStudioSnapshot{
		CapturedAt:  time.Now().UTC(),
		Project:     c.project,
		Requests24h: requests24h,
		CapUSD:      c.capUSD,
		SpentUSD:    spentUSD,
		ModelCount:  modelCount,
		KeyValid:    keyValid,
		RawJSON:     string(rawBytes),
	}, nil
}

// FetchModels lists available models from generativelanguage.googleapis.com/v1beta/models.
func (c *GoogleAIStudioClient) FetchModels(ctx context.Context) ([]ProviderModel, error) {
	if c.apiKey == "" {
		return nil, ErrGoogleAIStudioUnauthorized
	}
	endpoint := fmt.Sprintf("%s/v1beta/models?key=%s&pageSize=100", c.baseURL, url.QueryEscape(c.apiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGoogleAIStudioNetworkError, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGoogleAIStudioNetworkError, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGoogleAIStudioNetworkError, err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest:
		return nil, ErrGoogleAIStudioUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrGoogleAIStudioRateLimited
	case resp.StatusCode >= 500:
		return nil, ErrGoogleAIStudioServerError
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: HTTP %d", ErrGoogleAIStudioInvalidResponse, resp.StatusCode)
	}

	var parsed struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			Description                string   `json:"description"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			OutputTokenLimit           int      `json:"outputTokenLimit"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGoogleAIStudioInvalidResponse, err)
	}

	out := make([]ProviderModel, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		id := strings.TrimPrefix(strings.TrimSpace(m.Name), "models/")
		if id == "" {
			continue
		}
		name := strings.TrimSpace(m.DisplayName)
		if name == "" {
			name = id
		}
		out = append(out, ProviderModel{
			ID:            id,
			Name:          name,
			Provider:      "googleaistudio",
			OwnedBy:       "google",
			Description:   m.Description,
			ContextWindow: m.InputTokenLimit,
			MaxOutput:     m.OutputTokenLimit,
			Methods:       m.SupportedGenerationMethods,
		})
	}
	return out, nil
}

func (c *GoogleAIStudioClient) fetchMonitoringRequests24h(ctx context.Context, accessToken string) (int64, error) {
	end := time.Now().UTC()
	start := end.Add(-24 * time.Hour)
	filter := `metric.type="serviceruntime.googleapis.com/api/request_count" AND resource.labels.service="generativelanguage.googleapis.com"`

	q := url.Values{}
	q.Set("filter", filter)
	q.Set("interval.startTime", start.Format(time.RFC3339))
	q.Set("interval.endTime", end.Format(time.RFC3339))
	q.Set("aggregation.alignmentPeriod", "86400s")
	q.Set("aggregation.perSeriesAligner", "ALIGN_SUM")
	q.Set("aggregation.crossSeriesReducer", "REDUCE_SUM")

	endpoint := fmt.Sprintf("%s/v3/projects/%s/timeSeries?%s", c.monitoringURL, url.PathEscape(c.project), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("google monitoring status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return 0, err
	}

	var parsed struct {
		TimeSeries []struct {
			Points []struct {
				Value struct {
					Int64Value  string  `json:"int64Value"`
					DoubleValue float64 `json:"doubleValue"`
				} `json:"value"`
			} `json:"points"`
		} `json:"timeSeries"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, err
	}
	var total int64
	for _, ts := range parsed.TimeSeries {
		for _, pt := range ts.Points {
			if pt.Value.Int64Value != "" {
				if v, err := strconv.ParseInt(pt.Value.Int64Value, 10, 64); err == nil {
					total += v
				}
			} else if pt.Value.DoubleValue > 0 {
				total += int64(pt.Value.DoubleValue)
			}
		}
	}
	return total, nil
}

func (c *GoogleAIStudioClient) fetchMonitoringMonthlySpend(ctx context.Context, accessToken string) (inputTotal, outputTotal int64, costUSD float64) {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if !now.After(start) {
		start = start.Add(-24 * time.Hour)
	}

	inByModel := c.fetchMonthlyTokensByModel(ctx, accessToken, start, now,
		`metric.type="generativelanguage.googleapis.com/quota/generate_content_paid_tier_input_token_count/usage" AND metric.labels.limit_name="GenerateContentPaidTierInputTokensPerModelPerMinute"`)
	outByModel := c.fetchMonthlyTokensByModel(ctx, accessToken, start, now,
		`metric.type="generativelanguage.googleapis.com/generate_content_usage_output_token_count"`)

	models := make(map[string]struct{})
	for m := range inByModel {
		models[m] = struct{}{}
	}
	for m := range outByModel {
		models[m] = struct{}{}
	}
	for m := range models {
		inT := inByModel[m]
		outT := outByModel[m]
		inputTotal += inT
		outputTotal += outT
		inRate, outRate := googleModelPricingPer1M(m)
		costUSD += (float64(inT)/1_000_000.0)*inRate + (float64(outT)/1_000_000.0)*outRate
	}
	return inputTotal, outputTotal, costUSD
}

func (c *GoogleAIStudioClient) fetchMonthlyTokensByModel(ctx context.Context, accessToken string, start, end time.Time, filter string) map[string]int64 {
	res := make(map[string]int64)
	q := url.Values{}
	q.Set("filter", filter)
	q.Set("interval.startTime", start.Format(time.RFC3339))
	q.Set("interval.endTime", end.Format(time.RFC3339))
	q.Set("aggregation.alignmentPeriod", "2592000s")
	q.Set("aggregation.perSeriesAligner", "ALIGN_SUM")

	endpoint := fmt.Sprintf("%s/v3/projects/%s/timeSeries?%s", c.monitoringURL, url.PathEscape(c.project), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return res
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp != nil {
			resp.Body.Close()
		}
		return res
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return res
	}
	var parsed struct {
		TimeSeries []struct {
			Metric struct {
				Labels map[string]string `json:"labels"`
			} `json:"metric"`
			Points []struct {
				Value struct {
					Int64Value  string  `json:"int64Value"`
					DoubleValue float64 `json:"doubleValue"`
				} `json:"value"`
			} `json:"points"`
		} `json:"timeSeries"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return res
	}
	for _, ts := range parsed.TimeSeries {
		model := ts.Metric.Labels["model"]
		for _, pt := range ts.Points {
			if pt.Value.Int64Value != "" {
				if v, err := strconv.ParseInt(pt.Value.Int64Value, 10, 64); err == nil {
					res[model] += v
				}
			} else if pt.Value.DoubleValue > 0 {
				res[model] += int64(pt.Value.DoubleValue)
			}
		}
	}
	return res
}

func googleModelPricingPer1M(model string) (inPer1M, outPer1M float64) {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gemma"):
		return 0, 0
	case strings.Contains(m, "flash-lite"):
		return 0.075, 0.30
	case strings.Contains(m, "flash"):
		return 0.15, 0.60
	case strings.Contains(m, "carbon") || strings.Contains(m, "pro"):
		return 1.25, 10.0
	case strings.Contains(m, "hydrogen"):
		return 0.30, 2.50
	default:
		return 0.30, 2.50
	}
}

func (c *GoogleAIStudioClient) resolveAccessToken() string {
	if c.accessToken != "" {
		return c.accessToken
	}
	if t := strings.TrimSpace(os.Getenv("GOOGLE_ACCESS_TOKEN")); t != "" {
		return t
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".gemini", "oauth_creds.json"))
	if err == nil {
		var parsed struct {
			AccessToken string `json:"access_token"`
		}
		if json.Unmarshal(data, &parsed) == nil && strings.TrimSpace(parsed.AccessToken) != "" {
			return strings.TrimSpace(parsed.AccessToken)
		}
	}
	return resolveGcloudAccessToken()
}

func resolveGcloudAccessToken() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gcloud", "auth", "print-access-token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
