package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenRouterFetchAccountCredits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/credits" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer management-key" {
			t.Errorf("authorization = %q", got)
		}
		fmt.Fprint(w, `{"data":{"total_credits":100.5,"total_usage":25.75}}`)
	}))
	defer server.Close()

	client := NewOpenRouterClient("regular-key", nil, WithOpenRouterBaseURL(server.URL), WithOpenRouterManagementKey("management-key"))
	credits, err := client.FetchCredits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credits.TotalCredits != 100.5 || credits.TotalUsage != 25.75 {
		t.Fatalf("unexpected account credits: %+v", credits)
	}
}

func TestOpenRouterFetchAccountCreditsWithRegularKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/credits" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer regular-key" {
			t.Errorf("authorization = %q", got)
		}
		fmt.Fprint(w, `{"data":{"total_credits":20.0,"total_usage":18.06}}`)
	}))
	defer server.Close()

	client := NewOpenRouterClient("regular-key", nil, WithOpenRouterBaseURL(server.URL))
	credits, err := client.FetchCredits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credits.TotalCredits != 20.0 || credits.TotalUsage != 18.06 {
		t.Fatalf("unexpected account credits: %+v", credits)
	}
}
