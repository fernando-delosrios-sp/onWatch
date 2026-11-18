package api

import "time"

// GoogleAIStudioSnapshot represents a persisted Google AI Studio usage/models snapshot.
type GoogleAIStudioSnapshot struct {
	ID          int64     `json:"id"`
	CapturedAt  time.Time `json:"captured_at"`
	Project     string    `json:"project,omitempty"`
	Requests24h int64     `json:"requests_24h"`
	CapUSD      float64   `json:"cap_usd"`
	SpentUSD    float64   `json:"spent_usd"`
	ModelCount  int       `json:"model_count"`
	KeyValid    bool      `json:"key_valid"`
	RawJSON     string    `json:"raw_json,omitempty"`
}
