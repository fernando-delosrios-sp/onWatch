package api

import "testing"

func TestParseGrokCLIProxyBilling_PercentAndReset(t *testing.T) {
	payload := []byte(`{
	  "config": {
	    "creditUsagePercent": 42.5,
	    "currentPeriod": { "end": "2026-11-19T18:56:09Z" }
	  }
	}`)
	b, err := parseGrokCLIProxyBilling(payload)
	if err != nil {
		t.Fatal(err)
	}
	used, resets := billingToUsedPercentAndReset(b)
	if used != 42.5 {
		t.Fatalf("used = %v, want 42.5", used)
	}
	if resets == nil {
		t.Fatal("expected resets")
	}
}

func TestParseGrokCLIProxyBilling_ErrorPayload(t *testing.T) {
	_, err := parseGrokCLIProxyBilling([]byte(`{"error":"snapshot failed","code":"unavailable"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}
