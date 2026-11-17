package routing

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func openTestRoutingDB(t *testing.T) (*store.Store, *Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rs, err := NewStore(st.DB())
	if err != nil {
		t.Fatalf("routing.NewStore: %v", err)
	}
	return st, rs
}

func TestMonthlyCycleBounds(t *testing.T) {
	// Before reset day 15 -> Nov 15 would be future, so Oct 15 .. Nov 15
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	start, end := MonthlyCycleBounds(now, 15)
	if start != time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("got start %v, want 2026-10-15", start)
	}
	if end != time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("got end %v, want 2026-11-15", end)
	}

	// Feb clamp for resetDay=31
	feb := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	s2, e2 := MonthlyCycleBounds(feb, 31)
	if s2 != time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC) || e2 != time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("feb clamp got %v .. %v", s2, e2)
	}
}

func TestAntigravityThreePoolSplitAndDisabled3P(t *testing.T) {
	st, rs := openTestRoutingDB(t)
	db := st.DB()
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	rawJSON := `{"models":[{"modelId":"3p-5h","disabled":true},{"modelId":"3p-weekly","disabled":false}]}`
	res, err := db.Exec(`
		INSERT INTO antigravity_snapshots (captured_at, email, plan_name, raw_json, model_count, source)
		VALUES (?, 'user@example.com', 'Pro', ?, 5, 'cli')
	`, now.Format(time.RFC3339), rawJSON)
	if err != nil {
		t.Fatal(err)
	}
	snapID, _ := res.LastInsertId()

	rows := []struct {
		id    string
		label string
		frac  float64
		pct   float64
		exh   int
		reset string
	}{
		{"3p-weekly", "Claude Opus 5.5", 0.0, 0.0, 1, now.Add(5 * time.Hour).Format(time.RFC3339)},
		{"3p-5h", "Claude Opus 5.5", 1.0, 100.0, 0, now.Add(3 * time.Hour).Format(time.RFC3339)},
		{"gemini-5h", "Gemini 4 Hydrogen", 0.79, 79.0, 0, now.Add(2 * time.Hour).Format(time.RFC3339)},
		{"gemini-weekly", "Gemini 4 Hydrogen", 0.60, 60.0, 0, now.Add(96 * time.Hour).Format(time.RFC3339)},
		{"gemini-argon-wtu-weekly", "Gemini 4 Argon", 0.85, 85.0, 0, now.Add(120 * time.Hour).Format(time.RFC3339)},
	}
	for _, r := range rows {
		if _, err := db.Exec(`
			INSERT INTO antigravity_model_values (snapshot_id, model_id, label, remaining_fraction, remaining_percent, is_exhausted, reset_time)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, snapID, r.id, r.label, r.frac, r.pct, r.exh, r.reset); err != nil {
			t.Fatal(err)
		}
	}

	reader := NewSQLiteProbeReader(db)

	p3p, _ := rs.GetPool("antigravity-3p")
	r3p := reader.ReadProbe(context.Background(), *p3p, now)
	if !r3p.Exhausted || r3p.UsedShare != 1.0 {
		t.Fatalf("expected antigravity-3p exhausted=true used=1.0, got exhausted=%v used=%v", r3p.Exhausted, r3p.UsedShare)
	}

	pGem, _ := rs.GetPool("antigravity-gemini")
	rGem := reader.ReadProbe(context.Background(), *pGem, now)
	if rGem.Exhausted {
		t.Fatalf("expected antigravity-gemini healthy, got exhausted")
	}
	if len(rGem.Windows) != 3 {
		t.Fatalf("expected 3 gemini windows, got %d", len(rGem.Windows))
	}
	if rGem.UsedShare < 0.39 || rGem.UsedShare > 0.41 {
		t.Fatalf("expected gemini max used share ~0.40, got %v", rGem.UsedShare)
	}

	pFlash, _ := rs.GetPool("antigravity-flash")
	rFlash := reader.ReadProbe(context.Background(), *pFlash, now)
	if rFlash.Exhausted || len(rFlash.Windows) != 2 {
		t.Fatalf("expected antigravity-flash fallback to gemini-5h/weekly (2 windows, non-exhausted), got exh=%v len=%d", rFlash.Exhausted, len(rFlash.Windows))
	}
}

func TestCursorAndGrokQuotaCountFiltering(t *testing.T) {
	st, rs := openTestRoutingDB(t)
	db := st.DB()
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	// Insert valid cursor snapshot with quota_count=1 followed by empty snapshot quota_count=0
	res1, err := db.Exec(`INSERT INTO cursor_snapshots (captured_at, account_type, plan_name, quota_count) VALUES (?, 'team', 'enterprise', 1)`, now.Add(-2*time.Minute).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	id1, _ := res1.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO cursor_quota_values (snapshot_id, quota_name, used, limit_value, utilization, format, resets_at)
		VALUES (?, 'total_usage', 390.66, 500.0, 78.132, 'dollars', '2026-12-01T00:00:00Z')
	`, id1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cursor_snapshots (captured_at, account_type, plan_name, quota_count) VALUES (?, 'team', 'enterprise', 0)`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	// Insert grok snapshot quota_count=1 followed by quota_count=0
	gRes, err := db.Exec(`INSERT INTO grok_snapshots (captured_at, quota_count) VALUES (?, 1)`, now.Add(-time.Minute).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	gID, _ := gRes.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO grok_quota_values (snapshot_id, quota_name, utilization, resets_at, status)
		VALUES (?, 'credits', 86.0, ?, 'danger')
	`, gID, now.Add(48*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO grok_snapshots (captured_at, quota_count) VALUES (?, 0)`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	reader := NewSQLiteProbeReader(db)

	pCur, _ := rs.GetPool("cursor")
	rCur := reader.ReadProbe(context.Background(), *pCur, now)
	if !rCur.HasSpendData || rCur.SpentUSD != 390.66 {
		t.Fatalf("expected cursor spent=390.66 from quota_count>0 row, got %+v", rCur)
	}

	pGrok, _ := rs.GetPool("xai")
	rGrok := reader.ReadProbe(context.Background(), *pGrok, now)
	if !rGrok.HasQuotaUsage || rGrok.UsedShare != 0.86 {
		t.Fatalf("expected grok used_share=0.86 from quota_count>0 row, got %+v", rGrok)
	}
}
