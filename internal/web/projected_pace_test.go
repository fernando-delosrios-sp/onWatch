package web

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestComputeLinearProjectedPace(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC) // 30 days

	tests := []struct {
		name          string
		util          float64
		now           time.Time
		wantProjected float64
		wantDelta     float64
		wantStatus    string
	}{
		{
			name:          "at cycle start",
			util:          0,
			now:           start,
			wantProjected: 0,
			wantDelta:     0,
			wantStatus:    PaceStatusGreen,
		},
		{
			name:          "at midpoint under pace",
			util:          35,
			now:           start.Add(15 * 24 * time.Hour),
			wantProjected: 50,
			wantDelta:     -15,
			wantStatus:    PaceStatusGreen,
		},
		{
			name:          "at midpoint within 5 percent tolerance",
			util:          54.5,
			now:           start.Add(15 * 24 * time.Hour),
			wantProjected: 50,
			wantDelta:     4.5,
			wantStatus:    PaceStatusGreen,
		},
		{
			name:          "at midpoint moderately ahead (yellow)",
			util:          60,
			now:           start.Add(15 * 24 * time.Hour),
			wantProjected: 50,
			wantDelta:     10,
			wantStatus:    PaceStatusYellow,
		},
		{
			name:          "significantly over pace (red)",
			util:          82.3,
			now:           start.Add(16*24*time.Hour + 14*time.Hour), // ~55.3% of 30d
			wantProjected: 55.3,
			wantDelta:     27.0,
			wantStatus:    PaceStatusRed,
		},
		{
			name:          "at cycle end",
			util:          95,
			now:           end,
			wantProjected: 100,
			wantDelta:     -5,
			wantStatus:    PaceStatusGreen,
		},
		{
			name:          "after cycle end clamps to 100",
			util:          100,
			now:           end.Add(2 * time.Hour),
			wantProjected: 100,
			wantDelta:     0,
			wantStatus:    PaceStatusGreen,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := ComputeLinearProjectedPace(tc.util, start, end, tc.now)
			if !ok {
				t.Fatalf("expected ok=true")
			}
			if info.ProjectedPace != tc.wantProjected {
				t.Errorf("ProjectedPace = %.1f, want %.1f", info.ProjectedPace, tc.wantProjected)
			}
			if info.PaceDelta != tc.wantDelta {
				t.Errorf("PaceDelta = %.1f, want %.1f", info.PaceDelta, tc.wantDelta)
			}
			if info.PaceStatus != tc.wantStatus {
				t.Errorf("PaceStatus = %q, want %q", info.PaceStatus, tc.wantStatus)
			}
		})
	}
}

func TestInferCycleWindow(t *testing.T) {
	resetsAt := time.Date(2026, 12, 1, 1, 0, 0, 0, time.UTC)

	t.Run("cursor monthly", func(t *testing.T) {
		start, end, ok := InferCycleWindow("cursor", "total_usage", resetsAt, 0, false)
		if !ok {
			t.Fatal("expected ok=true")
		}
		wantStart := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
		if !start.Equal(wantStart) {
			t.Errorf("start = %v, want %v", start, wantStart)
		}
		if !end.Equal(resetsAt) {
			t.Errorf("end = %v, want %v", end, resetsAt)
		}
	})

	t.Run("codex windowSeconds", func(t *testing.T) {
		start, _, ok := InferCycleWindow("codex", "five_hour", resetsAt, 18000, false)
		if !ok {
			t.Fatal("expected ok=true")
		}
		wantStart := resetsAt.Add(-5 * time.Hour)
		if !start.Equal(wantStart) {
			t.Errorf("start = %v, want %v", start, wantStart)
		}
	})

	t.Run("anthropic seven_day", func(t *testing.T) {
		start, _, ok := InferCycleWindow("anthropic", "seven_day", resetsAt, 0, false)
		if !ok {
			t.Fatal("expected ok=true")
		}
		wantStart := resetsAt.Add(-7 * 24 * time.Hour)
		if !start.Equal(wantStart) {
			t.Errorf("start = %v, want %v", start, wantStart)
		}
	})

	t.Run("synthetic search 1h", func(t *testing.T) {
		start, _, ok := InferCycleWindow("synthetic", "search", resetsAt, 0, false)
		if !ok {
			t.Fatal("expected ok=true")
		}
		wantStart := resetsAt.Add(-1 * time.Hour)
		if !start.Equal(wantStart) {
			t.Errorf("start = %v, want %v", start, wantStart)
		}
	})
}

func TestEnrichSubscriptionPace(t *testing.T) {
	now := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	resetsAt := time.Date(2026, 12, 1, 1, 0, 0, 0, time.UTC) // midpoint of Nov 1 .. Dec 1

	t.Run("cursor quotas array", func(t *testing.T) {
		resp := map[string]interface{}{
			"quotas": []interface{}{
				map[string]interface{}{
					"name":        "total_usage",
					"utilization": 82.3,
					"resetsAt":    resetsAt.Format(time.RFC3339),
				},
				map[string]interface{}{
					"name":        "on_demand",
					"utilization": 10.0,
					"unlimited":   true,
					"resetsAt":    resetsAt.Format(time.RFC3339),
				},
			},
		}

		EnrichSubscriptionPace("cursor", resp, now)

		q0 := resp["quotas"].([]interface{})[0].(map[string]interface{})
		if q0["projectedPace"] != 50.0 {
			t.Errorf("projectedPace = %v, want 50.0", q0["projectedPace"])
		}
		if q0["paceDelta"] != 32.3 {
			t.Errorf("paceDelta = %v, want 32.3", q0["paceDelta"])
		}
		if q0["paceStatus"] != PaceStatusRed {
			t.Errorf("paceStatus = %v, want red", q0["paceStatus"])
		}
		if q0["cycleStart"] != "2026-11-01T01:00:00Z" {
			t.Errorf("cycleStart = %v, want 2026-11-01T01:00:00Z", q0["cycleStart"])
		}

		q1 := resp["quotas"].([]interface{})[1].(map[string]interface{})
		if _, exists := q1["paceStatus"]; exists {
			t.Errorf("unlimited quota should not have paceStatus")
		}
	})

	t.Run("synthetic named maps", func(t *testing.T) {
		fiveHourReset := now.Add(2*time.Hour + 30*time.Minute) // 50% of 5h
		resp := map[string]interface{}{
			"subscription": map[string]interface{}{
				"percent":  40.0,
				"renewsAt": fiveHourReset.Format(time.RFC3339),
			},
		}
		EnrichSubscriptionPace("synthetic", resp, now)
		sub := resp["subscription"].(map[string]interface{})
		if sub["projectedPace"] != 50.0 {
			t.Errorf("projectedPace = %v, want 50.0", sub["projectedPace"])
		}
		if sub["paceStatus"] != PaceStatusGreen {
			t.Errorf("paceStatus = %v, want green", sub["paceStatus"])
		}
	})

	t.Run("balance provider skipped", func(t *testing.T) {
		resp := map[string]interface{}{
			"quotas": []interface{}{
				map[string]interface{}{
					"name":        "available",
					"utilization": 50.0,
					"resetsAt":    resetsAt.Format(time.RFC3339),
				},
			},
		}
		EnrichSubscriptionPace("moonshot", resp, now)
		q0 := resp["quotas"].([]interface{})[0].(map[string]interface{})
		if _, exists := q0["paceStatus"]; exists {
			t.Errorf("balance provider should not be enriched")
		}
	})
}

func TestProjectedPaceFrontendHelpers(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}

	script := `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const src = fs.readFileSync('static/app.js', 'utf8');
const startIdx = src.indexOf('function extractQuotaPaceInfo');
const endIdx = src.indexOf('function renderProviderKPIHTML');
assert(startIdx > 0 && endIdx > startIdx, 'pace helpers not found in app.js');

const helpersSrc = src.slice(startIdx, endIdx);
const ctx = {
  State: {
    currentProviderPayloads: {},
    hiddenQuotas: new Set(),
  },
  isBalanceProvider: (p) => p === 'moonshot' || p === 'deepseek',
  escapeHTML: (s) => String(s),
};
vm.runInNewContext(helpersSrc, ctx);

const quota = {
  name: 'total_usage',
  projectedPace: 55.3,
  paceDelta: 27.0,
  paceStatus: 'red',
  cycleStart: '2026-11-01T00:00:00Z',
  cycleEnd: '2026-12-01T00:00:00Z',
};

const marker = ctx.paceMarkerHTML(quota);
assert(marker.includes('progress-pace-marker'));
assert(marker.includes('left: 55.3%'));

const row = ctx.paceRowHTML(quota);
assert(row.includes('data-pace="red"'));
assert(row.includes('Over pace'));
assert(row.includes('vs 55.3% proj (+27.0%)'));

ctx.State.currentProviderPayloads.cursor = { quotas: [quota] };
const usageDs = {
  label: 'Total Usage',
  data: [
    { x: new Date('2026-11-01T00:00:00Z'), y: 0 },
    { x: new Date('2026-11-16T00:00:00Z'), y: 65 },
    { x: new Date('2026-12-01T00:00:00Z'), y: 90 },
  ],
};
const projDs = ctx.buildProjectedPaceDataset('cursor', [usageDs]);
assert(projDs, 'expected projected dataset');
assert.equal(projDs.label, 'Projected Use');
assert.equal(projDs.data[0].y, 0);
assert.equal(projDs.data[1].y, 50);
assert.equal(projDs.data[2].y, 100);
assert.equal(ctx.buildProjectedPaceDataset('moonshot', [usageDs]), null);

const antigravityQuotas = [
  { modelId: 'gemini-5h', displayName: 'Gemini 5h', cycleStart: '2026-11-17T10:00:00Z', cycleEnd: '2026-11-17T15:00:00Z' },
  { modelId: '3p-5h', displayName: '3P 5h', cycleStart: '2026-11-17T10:00:00Z', cycleEnd: '2026-11-17T15:00:00Z' },
  { modelId: 'gemini-weekly', displayName: 'Gemini Weekly', cycleStart: '2026-11-12T10:00:00Z', cycleEnd: '2026-11-19T10:00:00Z' },
];
const groups = ctx.groupQuotasByCycle(antigravityQuotas);
assert.equal(groups.length, 2);
assert.equal(groups[0].durationLabel, '5-Hour Cycle');
assert.equal(groups[0].quotas.length, 2);
assert.equal(groups[1].durationLabel, '7-Day Cycle');
assert.equal(groups[1].quotas.length, 1);

ctx.processDataWithGaps = (pts) => ({ data: pts, gapSegments: new Set(), pointRadii: pts.map(() => 0) });
ctx.getSegmentStyle = () => ({});
const grokHistory = [
  { capturedAt: '2026-11-12T19:36:07Z', credits: 0 },
  { capturedAt: '2026-11-17T14:22:58Z', credits: 87 },
];
const grokGroup = {
  cycleStartMs: Date.parse('2026-11-12T18:56:09Z'),
  cycleEndMs: Date.parse('2026-11-19T18:56:09Z'),
  durationLabel: '7-Day Cycle',
  rangeKey: '7d',
  quotas: [{ name: 'credits', displayName: 'Credits', cycleStart: '2026-11-12T18:56:09Z', cycleEnd: '2026-11-19T18:56:09Z' }],
};
const cycleDatasets = ctx.buildCycleGroupDatasets('grok', grokHistory, grokGroup);
assert.equal(cycleDatasets.length, 2);
const cycleProj = cycleDatasets[1];
assert.equal(cycleProj.label, 'Projected Use');
assert.equal(cycleProj._isProjectedPace, true);
assert.equal(cycleProj.data[0].x.getTime(), grokGroup.cycleStartMs);
assert.equal(cycleProj.data[0].y, 0);

const rcrStart = src.indexOf('function recordChartRendered');
const rcrEnd = src.indexOf('function historyRequestIsCurrent');
assert(rcrStart > 0 && rcrEnd > rcrStart, 'recordChartRendered not found');
const rcrCtx = {
  State: {
    chart: {
      data: { datasets: cycleDatasets },
      options: { scales: { y: { max: 100 } } },
      update: () => {},
    },
    currentProviderPayloads: { grok: { quotas: grokGroup.quotas } },
    hiddenQuotas: new Set(),
  },
  chartAccountKey: () => null,
  chartAccountLabel: () => '',
  clearChartStale: () => {},
  syncChartProjectedPace: () => { throw new Error('syncChartProjectedPace should not run for cycle charts'); },
};
vm.runInNewContext(src.slice(rcrStart, rcrEnd), rcrCtx);
rcrCtx.recordChartRendered('grok', 'cycle', { skipProjectedPace: true });
assert.equal(rcrCtx.State.chart.data.datasets[1].data[0].y, 0);

`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
