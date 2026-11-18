package api

import "time"

// NvidiaSnapshot represents a persisted NVIDIA NIM health/rate-limit/models snapshot.
type NvidiaSnapshot struct {
	ID                 int64     `json:"id"`
	CapturedAt         time.Time `json:"captured_at"`
	ModelCount         int       `json:"model_count"`
	RateLimitRemaining int       `json:"rate_limit_remaining"`
	RateLimitLimit     int       `json:"rate_limit_limit"`
	KeyValid           bool      `json:"key_valid"`
	RawJSON            string    `json:"raw_json,omitempty"`
}
