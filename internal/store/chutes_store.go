package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

// ChutesResetCycle represents a Chutes usage reset cycle.
type ChutesResetCycle struct {
	ID         int64
	QuotaType  string
	CycleStart time.Time
	CycleEnd   *time.Time
	PeakUsage  float64
	TotalDelta float64
}

// InsertChutesSnapshot inserts a Chutes usage snapshot.
func (s *Store) InsertChutesSnapshot(snapshot *api.ChutesSnapshot) (int64, error) {
	var resetsVal interface{}
	if snapshot.ResetsAt != nil {
		resetsVal = snapshot.ResetsAt.UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.Exec(
		`INSERT INTO chutes_snapshots
		(captured_at, used_usd, limit_usd, used_percent, balance_usd, resets_at, model_count, raw_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshot.CapturedAt.UTC().Format(time.RFC3339Nano),
		snapshot.UsedUSD,
		snapshot.LimitUSD,
		snapshot.UsedPercent,
		snapshot.BalanceUSD,
		resetsVal,
		snapshot.ModelCount,
		snapshot.RawJSON,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert chutes snapshot: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get chutes snapshot id: %w", err)
	}
	return id, nil
}

// QueryLatestChutes returns the most recent Chutes snapshot.
func (s *Store) QueryLatestChutes() (*api.ChutesSnapshot, error) {
	var snap api.ChutesSnapshot
	var capturedAt string
	var resetsAt sql.NullString
	var rawJSON sql.NullString

	err := s.db.QueryRow(
		`SELECT id, captured_at, used_usd, limit_usd, used_percent, balance_usd, resets_at, model_count, raw_json
		FROM chutes_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(
		&snap.ID, &capturedAt, &snap.UsedUSD, &snap.LimitUSD, &snap.UsedPercent,
		&snap.BalanceUSD, &resetsAt, &snap.ModelCount, &rawJSON,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest chutes: %w", err)
	}
	snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	if resetsAt.Valid && resetsAt.String != "" {
		if t, err := time.Parse(time.RFC3339Nano, resetsAt.String); err == nil {
			snap.ResetsAt = &t
		}
	}
	snap.RawJSON = rawJSON.String
	return &snap, nil
}

// QueryChutesRange returns Chutes snapshots within [start, end] (bounded to 500).
func (s *Store) QueryChutesRange(start, end time.Time) ([]*api.ChutesSnapshot, error) {
	rows, err := s.db.Query(
		`SELECT id, captured_at, used_usd, limit_usd, used_percent, balance_usd, resets_at, model_count, raw_json
		FROM chutes_snapshots
		WHERE captured_at >= ? AND captured_at <= ?
		ORDER BY captured_at ASC LIMIT 500`,
		start.UTC().Format(time.RFC3339Nano),
		end.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query chutes range: %w", err)
	}
	defer rows.Close()

	var out []*api.ChutesSnapshot
	for rows.Next() {
		var snap api.ChutesSnapshot
		var capturedAt string
		var resetsAt, rawJSON sql.NullString
		if err := rows.Scan(&snap.ID, &capturedAt, &snap.UsedUSD, &snap.LimitUSD, &snap.UsedPercent, &snap.BalanceUSD, &resetsAt, &snap.ModelCount, &rawJSON); err != nil {
			return nil, fmt.Errorf("failed to scan chutes snapshot: %w", err)
		}
		snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		if resetsAt.Valid && resetsAt.String != "" {
			if t, err := time.Parse(time.RFC3339Nano, resetsAt.String); err == nil {
				snap.ResetsAt = &t
			}
		}
		snap.RawJSON = rawJSON.String
		out = append(out, &snap)
	}
	return out, rows.Err()
}

// CreateChutesCycle creates a new Chutes reset cycle.
func (s *Store) CreateChutesCycle(quotaType string, cycleStart time.Time) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO chutes_reset_cycles (quota_type, cycle_start) VALUES (?, ?)`,
		quotaType, cycleStart.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create chutes cycle: %w", err)
	}
	return res.LastInsertId()
}

// QueryActiveChutesCycle returns the active cycle for quotaType.
func (s *Store) QueryActiveChutesCycle(quotaType string) (*ChutesResetCycle, error) {
	var c ChutesResetCycle
	var startStr string
	err := s.db.QueryRow(
		`SELECT id, quota_type, cycle_start, peak_usage, total_delta
		FROM chutes_reset_cycles WHERE quota_type = ? AND cycle_end IS NULL ORDER BY id DESC LIMIT 1`,
		quotaType,
	).Scan(&c.ID, &c.QuotaType, &startStr, &c.PeakUsage, &c.TotalDelta)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active chutes cycle: %w", err)
	}
	c.CycleStart, _ = time.Parse(time.RFC3339Nano, startStr)
	return &c, nil
}

// UpdateChutesCycle updates peak_usage and total_delta.
func (s *Store) UpdateChutesCycle(id int64, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE chutes_reset_cycles SET peak_usage = ?, total_delta = ? WHERE id = ?`,
		peak, delta, id,
	)
	return err
}

// CloseChutesCycle closes an active cycle.
func (s *Store) CloseChutesCycle(id int64, end time.Time, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE chutes_reset_cycles SET cycle_end = ?, peak_usage = ?, total_delta = ? WHERE id = ?`,
		end.UTC().Format(time.RFC3339Nano), peak, delta, id,
	)
	return err
}

// QueryChutesCycleHistory returns up to limit closed cycles.
func (s *Store) QueryChutesCycleHistory(quotaType string, limit int) ([]*ChutesResetCycle, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, quota_type, cycle_start, cycle_end, peak_usage, total_delta
		FROM chutes_reset_cycles WHERE quota_type = ? AND cycle_end IS NOT NULL
		ORDER BY cycle_start DESC LIMIT ?`,
		quotaType, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query chutes cycle history: %w", err)
	}
	defer rows.Close()

	var out []*ChutesResetCycle
	for rows.Next() {
		var c ChutesResetCycle
		var startStr, endStr string
		if err := rows.Scan(&c.ID, &c.QuotaType, &startStr, &endStr, &c.PeakUsage, &c.TotalDelta); err != nil {
			return nil, err
		}
		c.CycleStart, _ = time.Parse(time.RFC3339Nano, startStr)
		if t, err := time.Parse(time.RFC3339Nano, endStr); err == nil {
			c.CycleEnd = &t
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}
