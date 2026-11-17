package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const grokCLIProxyBillingURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"

// grokCLIProxyCreditsResponse is the credits payload from the Grok CLI proxy.
type grokCLIProxyCreditsResponse struct {
	Config *grokCLIProxyCreditsConfig `json:"config"`
	Code   string                     `json:"code"`
	Error  string                     `json:"error"`
}

type grokCLIProxyCreditsConfig struct {
	CreditUsagePercent *float64              `json:"creditUsagePercent"`
	CurrentPeriod      *grokCLIProxyPeriod   `json:"currentPeriod"`
	BillingPeriodEnd   string                `json:"billingPeriodEnd"`
	OnDemandCap        *GrokCent             `json:"onDemandCap"`
	OnDemandUsed       *GrokCent             `json:"onDemandUsed"`
}

type grokCLIProxyPeriod struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func parseGrokCLIProxyBilling(data []byte) (*GrokBillingResponse, error) {
	var raw grokCLIProxyCreditsResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Error != "" || raw.Code != "" {
		return nil, fmt.Errorf("grok cli-proxy: %s %s", raw.Code, raw.Error)
	}
	if raw.Config == nil {
		return nil, fmt.Errorf("grok cli-proxy: missing config")
	}

	cfg := raw.Config
	resp := &GrokBillingResponse{}
	if cfg.OnDemandCap != nil {
		resp.OnDemandCap = cfg.OnDemandCap
	}
	used := &GrokBillingUsage{}
	if cfg.OnDemandUsed != nil {
		used.OnDemandUsed = cfg.OnDemandUsed
		used.TotalUsed = cfg.OnDemandUsed
	}
	resp.Usage = used

	end := ""
	if cfg.CurrentPeriod != nil && cfg.CurrentPeriod.End != "" {
		end = cfg.CurrentPeriod.End
	} else if cfg.BillingPeriodEnd != "" {
		end = cfg.BillingPeriodEnd
	}
	if end != "" {
		resp.BillingCycle = &GrokBillingCycle{BillingPeriodEnd: end}
	}

	// When the proxy omits creditUsagePercent, derive from on-demand cap/used (cents).
	if cfg.CreditUsagePercent != nil {
		pct := *cfg.CreditUsagePercent
		resp.CreditUsagePercent = &pct
	} else if cfg.OnDemandCap != nil && cfg.OnDemandCap.Val != nil && cfg.OnDemandUsed != nil && cfg.OnDemandUsed.Val != nil {
		cap := *cfg.OnDemandCap.Val
		usedCents := *cfg.OnDemandUsed.Val
		if cap > 0 {
			pct := float64(usedCents) / float64(cap) * 100.0
			resp.CreditUsagePercent = &pct
		}
	}

	return resp, nil
}

func (c *GrokClient) tryCLIProxyBilling(ctx context.Context, creds *GrokCredentials) (*GrokBillingResponse, error) {
	if creds == nil || creds.AccessToken == "" || creds.IsExpired() {
		return nil, ErrGrokUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, grokCLIProxyBillingURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("x-xai-token-auth", "xai-grok-cli")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "onwatch/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGrokNetworkError, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrGrokUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: http %d", ErrGrokServerError, resp.StatusCode)
	}
	billing, err := parseGrokCLIProxyBilling(body)
	if err != nil {
		return nil, err
	}
	return billing, nil
}
