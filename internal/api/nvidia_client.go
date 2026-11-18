package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNvidiaUnauthorized    = errors.New("nvidia: unauthorized - invalid API key")
	ErrNvidiaRateLimited     = errors.New("nvidia: rate limited")
	ErrNvidiaServerError     = errors.New("nvidia: server error")
	ErrNvidiaNetworkError    = errors.New("nvidia: network error")
	ErrNvidiaInvalidResponse = errors.New("nvidia: invalid response")
)

const defaultNvidiaBaseURL = "https://integrate.api.nvidia.com"

// NvidiaClient is an HTTP client for NVIDIA NIM API (integrate.api.nvidia.com).
type NvidiaClient struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	logger     *slog.Logger
}

// NvidiaOption configures an NvidiaClient.
type NvidiaOption func(*NvidiaClient)

// WithNvidiaBaseURL sets a custom base URL (for testing or proxies).
func WithNvidiaBaseURL(u string) NvidiaOption {
	return func(c *NvidiaClient) {
		if strings.TrimSpace(u) != "" {
			c.baseURL = strings.TrimRight(strings.TrimSpace(u), "/")
		}
	}
}

// NewNvidiaClient creates a new NVIDIA NIM API client.
func NewNvidiaClient(apiKey string, logger *slog.Logger, opts ...NvidiaOption) *NvidiaClient {
	if logger == nil {
		logger = slog.Default()
	}
	c := &NvidiaClient{
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
		apiKey:  strings.TrimSpace(apiKey),
		baseURL: defaultNvidiaBaseURL,
		logger:  logger,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// FetchUsage queries /v1/models on NVIDIA NIM to verify key health, count models, and read rate-limit headers.
func (c *NvidiaClient) FetchUsage(ctx context.Context) (*NvidiaSnapshot, error) {
	if c.apiKey == "" {
		return nil, ErrNvidiaUnauthorized
	}

	models, headers, err := c.fetchModelsWithHeaders(ctx)
	if err != nil {
		return nil, err
	}

	rem := parseHeaderInt(headers, "X-RateLimit-Remaining-Requests", "X-RateLimit-Remaining")
	lim := parseHeaderInt(headers, "X-RateLimit-Limit-Requests", "X-RateLimit-Limit")

	rawBytes, _ := json.Marshal(map[string]interface{}{
		"model_count":          len(models),
		"rate_limit_remaining": rem,
		"rate_limit_limit":     lim,
	})

	return &NvidiaSnapshot{
		CapturedAt:         time.Now().UTC(),
		ModelCount:         len(models),
		RateLimitRemaining: rem,
		RateLimitLimit:     lim,
		KeyValid:           true,
		RawJSON:            string(rawBytes),
	}, nil
}

// FetchModels returns available models from NVIDIA NIM (/v1/models).
func (c *NvidiaClient) FetchModels(ctx context.Context) ([]ProviderModel, error) {
	if c.apiKey == "" {
		return nil, ErrNvidiaUnauthorized
	}
	models, _, err := c.fetchModelsWithHeaders(ctx)
	return models, err
}

func (c *NvidiaClient) fetchModelsWithHeaders(ctx context.Context) ([]ProviderModel, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNvidiaNetworkError, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNvidiaNetworkError, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNvidiaNetworkError, err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, nil, ErrNvidiaUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, nil, ErrNvidiaRateLimited
	case resp.StatusCode >= 500:
		return nil, nil, ErrNvidiaServerError
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, nil, fmt.Errorf("%w: HTTP %d", ErrNvidiaInvalidResponse, resp.StatusCode)
	}

	var parsed struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNvidiaInvalidResponse, err)
	}

	out := make([]ProviderModel, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		out = append(out, ProviderModel{
			ID:       m.ID,
			Name:     m.ID,
			Provider: "nvidia",
			OwnedBy:  m.OwnedBy,
			Free:     true,
		})
	}
	return out, resp.Header, nil
}

func parseHeaderInt(h http.Header, keys ...string) int {
	for _, k := range keys {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
	}
	return 0
}
