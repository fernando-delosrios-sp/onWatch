package routing

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPHandlersEndToEnd(t *testing.T) {
	st, _ := openTestRoutingDB(t)
	svc, err := NewService(st.DB(), nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.Handler.RegisterRoutes(mux, "")

	// 1. Embedded UI page & assets
	for _, path := range []string{"/routing", "/routing/assets/routing.css", "/routing/assets/routing.js"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", path, rr.Code)
		}
		body := rr.Body.String()
		if path == "/routing" {
			if !strings.Contains(body, `<option value="" selected>All harnesses</option>`) {
				t.Fatalf("expected /routing to default #harness-preview-select to All harnesses")
			}
			if strings.Contains(body, `pi + all native`) || strings.Contains(body, `native CLIs only`) {
				t.Fatalf("expected /routing not to contain hardcoded harness preset options")
			}
		}
		if path == "/routing/assets/routing.js" && !strings.Contains(body, "populateHarnessPreviewSelect") {
			t.Fatalf("expected routing.js to define populateHarnessPreviewSelect")
		}
	}

	// 2. GET /api/route?harnesses=pi,cursor,antigravity,grok,claudeAgent,codex
	req := httptest.NewRequest(http.MethodGet, "/api/route?harnesses=pi,cursor,antigravity,grok,claudeAgent,codex", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/route returned %d: %s", rr.Code, rr.Body.String())
	}
	var routeResp RouteResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &routeResp); err != nil {
		t.Fatal(err)
	}
	if len(routeResp.Pools) != 11 {
		t.Fatalf("expected 11 pools, got %d", len(routeResp.Pools))
	}
	if !routeResp.TierSelections[TierLightweight].Available || routeResp.TierSelections[TierLightweight].Pool != "nvidia-free" {
		t.Fatalf("expected lightweight nvidia-free, got %+v", routeResp.TierSelections[TierLightweight])
	}
	if len(routeResp.Tiers[TierLightweight]) == 0 || routeResp.Tiers[TierLightweight][0].ID != "openrouter/nvidia/nemotron-3.5-lightning:free" {
		t.Fatalf("expected exact nemotron candidate in Tiers[lightweight], got %+v", routeResp.Tiers[TierLightweight])
	}

	// 2b. GET /api/route with tier, pool, exclude
	piReq := httptest.NewRequest(http.MethodGet, "/api/route?harnesses=pi&tier=lightweight&exclude=openrouter/nvidia/nemotron-3.5-lightning:free", nil)
	piRR := httptest.NewRecorder()
	mux.ServeHTTP(piRR, piReq)
	if piRR.Code != http.StatusOK {
		t.Fatalf("GET /api/route filtered returned %d: %s", piRR.Code, piRR.Body.String())
	}
	var piResp RouteResponse
	if err := json.Unmarshal(piRR.Body.Bytes(), &piResp); err != nil {
		t.Fatal(err)
	}
	if len(piResp.Tiers) != 1 || len(piResp.Tiers[TierLightweight]) == 0 || piResp.Tiers[TierLightweight][0].Model == "nvidia/nemotron-3.5-lightning:free" {
		t.Fatalf("expected filtered lightweight excluding lightning:free, got %+v", piResp.Tiers)
	}

	// 2c. GET /api/route?harnesses=pi&exclude_free=true skips all free pools & free models
	noFreeReq := httptest.NewRequest(http.MethodGet, "/api/route?harnesses=pi&exclude_free=true", nil)
	noFreeRR := httptest.NewRecorder()
	mux.ServeHTTP(noFreeRR, noFreeReq)
	if noFreeRR.Code != http.StatusOK {
		t.Fatalf("GET /api/route exclude_free=true returned %d: %s", noFreeRR.Code, noFreeRR.Body.String())
	}
	var noFreeResp RouteResponse
	if err := json.Unmarshal(noFreeRR.Body.Bytes(), &noFreeResp); err != nil {
		t.Fatal(err)
	}
	for _, tier := range AllTiers {
		if noFreeResp.TierSelections[tier].Pool == "nvidia-free" {
			t.Fatalf("expected tier %s to skip nvidia-free with exclude_free=true", tier)
		}
		for _, c := range noFreeResp.Tiers[tier] {
			if c.Billing == "free" || c.Overlay == "@preset/code-nvidia" {
				t.Fatalf("expected no free candidate in tier %s, got %+v", tier, c)
			}
		}
	}

	// 3. POST /api/route/feedback (linear-t3-dispatcher payload shape)
	fbPayload := `{"pool":"nvidia-free","provider":"nvidia","harness":"pi","model":"nvidia/nemotron-3-ultra-550b-a55b:free","event":"startup_failure","outcome":"error","error":"timeout","session":"s1","issue":"ENG-7"}`
	fbReq := httptest.NewRequest(http.MethodPost, "/api/route/feedback", strings.NewReader(fbPayload))
	fbRR := httptest.NewRecorder()
	mux.ServeHTTP(fbRR, fbReq)
	if fbRR.Code != http.StatusOK {
		t.Fatalf("POST /api/route/feedback returned %d: %s", fbRR.Code, fbRR.Body.String())
	}

	// 3b. POST /api/route/feedback (pi-onwatch-jev camelCase payload without explicit pool)
	piFB := `{"harness":"pi","provider":"openrouter","model":"google/gemini-4-hydrogen","outcome":"demoted","statusCode":500,"ttftMs":310,"latencyMs":920,"reason":"user ran /bad"}`
	piFBReq := httptest.NewRequest(http.MethodPost, "/api/route/feedback", strings.NewReader(piFB))
	piFBRR := httptest.NewRecorder()
	mux.ServeHTTP(piFBRR, piFBReq)
	if piFBRR.Code != http.StatusOK {
		t.Fatalf("POST /api/route/feedback (pi camelCase) returned %d: %s", piFBRR.Code, piFBRR.Body.String())
	}
	var piFBRes struct {
		OK     bool          `json:"ok"`
		Event  FeedbackEvent `json:"event"`
		Health PoolHealth    `json:"health"`
	}
	_ = json.Unmarshal(piFBRR.Body.Bytes(), &piFBRes)
	if piFBRes.Event.PoolID != "google-ai-studio" || piFBRes.Event.Outcome != "bad" || piFBRes.Event.TTFTMs != 310 || piFBRes.Health.CooldownUntil == nil {
		t.Fatalf("unexpected normalized pi feedback response: %+v", piFBRes)
	}

	// 4. GET /api/routing/state
	stReq := httptest.NewRequest(http.MethodGet, "/api/routing/state", nil)
	stRR := httptest.NewRecorder()
	mux.ServeHTTP(stRR, stReq)
	if stRR.Code != http.StatusOK {
		t.Fatalf("GET /api/routing/state returned %d", stRR.Code)
	}
	if !strings.Contains(stRR.Body.String(), "ENG-7") {
		t.Fatalf("expected recent_feedback to contain ENG-7")
	}

	// 5. PUT /api/routing/pools/google-ai-studio
	poolUpdate := `{"display_name":"Google AI Studio","category":"allowance","enabled":true,"cap_usd":20,"reserve_usd":2,"spent_usd":3.5,"reset_day":1,"reset_cycle":"monthly","pace_slack":0.15,"weight":99}`
	pReq := httptest.NewRequest(http.MethodPut, "/api/routing/pools/google-ai-studio", strings.NewReader(poolUpdate))
	pRR := httptest.NewRecorder()
	mux.ServeHTTP(pRR, pReq)
	if pRR.Code != http.StatusOK {
		t.Fatalf("PUT pool returned %d: %s", pRR.Code, pRR.Body.String())
	}

	// 5b. POST /api/routing/pools/google-ai-studio/reset-cycle
	rcReq := httptest.NewRequest(http.MethodPost, "/api/routing/pools/google-ai-studio/reset-cycle", nil)
	rcRR := httptest.NewRecorder()
	mux.ServeHTTP(rcRR, rcReq)
	if rcRR.Code != http.StatusOK {
		t.Fatalf("POST reset-cycle returned %d: %s", rcRR.Code, rcRR.Body.String())
	}
	pGoogle, _ := svc.Store.GetPool("google-ai-studio")
	if pGoogle.SpentUSD != 0 {
		t.Fatalf("expected spent_usd reset to 0, got %f", pGoogle.SpentUSD)
	}

	// 6. POST /api/routing/models -> PUT -> DELETE
	mBody := `{"tier":"workhorse","pool_id":"chutes","harness":"pi","provider":"chutes","overlay":"@preset/code-chutes","model_id":"test/model-1","billing":"byok","description":"test","priority":130,"enabled":true}`
	mReq := httptest.NewRequest(http.MethodPost, "/api/routing/models", strings.NewReader(mBody))
	mRR := httptest.NewRecorder()
	mux.ServeHTTP(mRR, mReq)
	if mRR.Code != http.StatusOK {
		t.Fatalf("POST model returned %d: %s", mRR.Code, mRR.Body.String())
	}
	var mRes struct {
		Model RouteModel `json:"model"`
	}
	_ = json.Unmarshal(mRR.Body.Bytes(), &mRes)
	if mRes.Model.ID <= 0 {
		t.Fatalf("expected positive model id, got %d", mRes.Model.ID)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/routing/models/"+itoa64(mRes.Model.ID), nil)
	delRR := httptest.NewRecorder()
	mux.ServeHTTP(delRR, delReq)
	if delRR.Code != http.StatusOK {
		t.Fatalf("DELETE model returned %d", delRR.Code)
	}

	// 7. GET & POST /api/route/catalog
	catReq := httptest.NewRequest(http.MethodGet, "/api/route/catalog", nil)
	catRR := httptest.NewRecorder()
	mux.ServeHTTP(catRR, catReq)
	if catRR.Code != http.StatusOK {
		t.Fatalf("GET /api/route/catalog returned %d", catRR.Code)
	}
	impReq := httptest.NewRequest(http.MethodPost, "/api/route/catalog", bytes.NewReader(catRR.Body.Bytes()))
	impReq.Header.Set("Content-Type", "application/yaml")
	impRR := httptest.NewRecorder()
	mux.ServeHTTP(impRR, impReq)
	if impRR.Code != http.StatusOK {
		t.Fatalf("POST /api/route/catalog returned %d: %s", impRR.Code, impRR.Body.String())
	}
}

func itoa64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
