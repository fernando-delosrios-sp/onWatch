package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultGoogleMonitoringBaseURL = "https://monitoring.googleapis.com"

// GoogleMonitoringClient optionally queries GCP Cloud Monitoring for generativelanguage.googleapis.com request counts.
type GoogleMonitoringClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewGoogleMonitoringClient constructs a Google Cloud Monitoring client.
func NewGoogleMonitoringClient(baseURL string, httpClient *http.Client) *GoogleMonitoringClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultGoogleMonitoringBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &GoogleMonitoringClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

// FetchRequestMetrics queries last-24h request counts for generativelanguage.googleapis.com in a GCP project.
func (c *GoogleMonitoringClient) FetchRequestMetrics(ctx context.Context, project, accessToken string, now time.Time) (ProbeSnapshot, error) {
	if strings.TrimSpace(project) == "" || strings.TrimSpace(accessToken) == "" {
		return ProbeSnapshot{}, fmt.Errorf("google cloud project and access token required")
	}

	end := now.UTC()
	start := end.Add(-24 * time.Hour)
	filter := `metric.type="serviceruntime.googleapis.com/api/request_count" AND resource.labels.service="generativelanguage.googleapis.com"`

	q := url.Values{}
	q.Set("filter", filter)
	q.Set("interval.startTime", start.Format(time.RFC3339))
	q.Set("interval.endTime", end.Format(time.RFC3339))
	q.Set("aggregation.alignmentPeriod", "86400s")
	q.Set("aggregation.perSeriesAligner", "ALIGN_SUM")
	q.Set("aggregation.crossSeriesReducer", "REDUCE_SUM")

	endpoint := fmt.Sprintf("%s/v3/projects/%s/timeSeries?%s", c.baseURL, url.PathEscape(project), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ProbeSnapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
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
		return ProbeSnapshot{}, fmt.Errorf("google monitoring status %d", resp.StatusCode)
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
		return ProbeSnapshot{}, err
	}

	var totalReqs int64
	for _, ts := range parsed.TimeSeries {
		for _, pt := range ts.Points {
			if pt.Value.Int64Value != "" {
				if v, err := strconv.ParseInt(pt.Value.Int64Value, 10, 64); err == nil {
					totalReqs += v
				}
			} else if pt.Value.DoubleValue > 0 {
				totalReqs += int64(pt.Value.DoubleValue)
			}
		}
	}

	extraBytes, _ := json.Marshal(map[string]interface{}{
		"project":      project,
		"requests_24h": totalReqs,
	})
	return ProbeSnapshot{
		ProbeSource: "google_ai_studio",
		PoolID:      "google-ai-studio",
		ExtraJSON:   string(extraBytes),
		CapturedAt:  end,
	}, nil
}

// EstimateCostUSD computes request cost from token counts using RouteModel per-1M token pricing.
// Cached input tokens are billed at 25% of input token rate when cachedTokens <= promptTokens.
func EstimateCostUSD(m RouteModel, promptTokens, completionTokens, cachedTokens int64) float64 {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > promptTokens {
		cachedTokens = promptTokens
	}
	uncached := promptTokens - cachedTokens
	inputCost := (float64(uncached)/1_000_000.0)*m.InputCostPer1M + (float64(cachedTokens)/1_000_000.0)*(m.InputCostPer1M*0.25)
	outputCost := (float64(completionTokens) / 1_000_000.0) * m.OutputCostPer1M
	return inputCost + outputCost
}

func resolveGoogleAccessToken() (token, source string) {
	if t := strings.TrimSpace(os.Getenv("GOOGLE_ACCESS_TOKEN")); t != "" {
		return t, "env:GOOGLE_ACCESS_TOKEN"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", ""
	}
	credsPath := filepath.Join(home, ".gemini", "oauth_creds.json")
	data, err := os.ReadFile(credsPath)
	if err != nil {
		return "", ""
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(data, &parsed) == nil && strings.TrimSpace(parsed.AccessToken) != "" {
		return strings.TrimSpace(parsed.AccessToken), "~/.gemini/oauth_creds.json"
	}
	return "", ""
}
