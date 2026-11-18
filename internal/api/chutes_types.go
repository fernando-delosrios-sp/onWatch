package api

import "time"

// ChutesSnapshot represents a persisted Chutes usage/credit snapshot.
type ChutesSnapshot struct {
	ID          int64      `json:"id"`
	CapturedAt  time.Time  `json:"captured_at"`
	UsedUSD     float64    `json:"used_usd"`
	LimitUSD    float64    `json:"limit_usd"`
	UsedPercent float64    `json:"used_percent"`
	BalanceUSD  float64    `json:"balance_usd"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	ModelCount  int        `json:"model_count"`
	RawJSON     string     `json:"raw_json,omitempty"`
}
