package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

// NvidiaResetCycle represents an NVIDIA NIM tracking cycle.
type NvidiaResetCycle struct {
	ID         int64
	QuotaType  string
	CycleStart time.Time
	CycleEnd   *time.Time
	PeakUsage  float64
	TotalDelta float64
}

// InsertNvidiaSnapshot inserts an NVIDIA NIM snapshot.
func (s *Store) InsertNvidiaSnapshot(snapshot *api.NvidiaSnapshot) (int64, error) {
	keyValid := 0
	if snapshot.KeyValid {
		keyValid = 1
	}
	res, err := s.db.Exec(
		`INSERT INTO nvidia_snapshots
		(captured_at, model_count, rate_limit_remaining, rate_limit_limit, key_valid, raw_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		snapshot.CapturedAt.UTC().Format(time.RFC3339Nano),
		snapshot.ModelCount,
		snapshot.RateLimitRemaining,
		snapshot.RateLimitLimit,
		keyValid,
		snapshot.RawJSON,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert nvidia snapshot: %w", err)
	}
	return res.LastInsertId()
}

// QueryLatestNvidia returns the most recent NVIDIA NIM snapshot.
func (s *Store) QueryLatestNvidia() (*api.NvidiaSnapshot, error) {
	var snap api.NvidiaSnapshot
	var capturedAt string
	var keyValid int
	var rawJSON sql.NullString

	err := s.db.QueryRow(
		`SELECT id, captured_at, model_count, rate_limit_remaining, rate_limit_limit, key_valid, raw_json
		FROM nvidia_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(&snap.ID, &capturedAt, &snap.ModelCount, &snap.RateLimitRemaining, &snap.RateLimitLimit, &keyValid, &rawJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest nvidia: %w", err)
	}
	snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	snap.KeyValid = keyValid == 1
	snap.RawJSON = rawJSON.String
	return &snap, nil
}

// QueryNvidiaRange returns NVIDIA NIM snapshots within [start, end] (bounded to 500).
func (s *Store) QueryNvidiaRange(start, end time.Time) ([]*api.NvidiaSnapshot, error) {
	rows, err := s.db.Query(
		`SELECT id, captured_at, model_count, rate_limit_remaining, rate_limit_limit, key_valid, raw_json
		FROM nvidia_snapshots
		WHERE captured_at >= ? AND captured_at <= ?
		ORDER BY captured_at ASC LIMIT 500`,
		start.UTC().Format(time.RFC3339Nano),
		end.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query nvidia range: %w", err)
	}
	defer rows.Close()

	var out []*api.NvidiaSnapshot
	for rows.Next() {
		var snap api.NvidiaSnapshot
		var capturedAt string
		var keyValid int
		var rawJSON sql.NullString
		if err := rows.Scan(&snap.ID, &capturedAt, &snap.ModelCount, &snap.RateLimitRemaining, &snap.RateLimitLimit, &keyValid, &rawJSON); err != nil {
			return nil, err
		}
		snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		snap.KeyValid = keyValid == 1
		snap.RawJSON = rawJSON.String
		out = append(out, &snap)
	}
	return out, rows.Err()
}

// CreateNvidiaCycle creates a new NVIDIA NIM cycle.
func (s *Store) CreateNvidiaCycle(quotaType string, cycleStart time.Time) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO nvidia_reset_cycles (quota_type, cycle_start) VALUES (?, ?)`,
		quotaType, cycleStart.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create nvidia cycle: %w", err)
	}
	return res.LastInsertId()
}

// QueryActiveNvidiaCycle returns the active cycle for quotaType.
func (s *Store) QueryActiveNvidiaCycle(quotaType string) (*NvidiaResetCycle, error) {
	var c NvidiaResetCycle
	var startStr string
	err := s.db.QueryRow(
		`SELECT id, quota_type, cycle_start, peak_usage, total_delta
		FROM nvidia_reset_cycles WHERE quota_type = ? AND cycle_end IS NULL ORDER BY id DESC LIMIT 1`,
		quotaType,
	).Scan(&c.ID, &c.QuotaType, &startStr, &c.PeakUsage, &c.TotalDelta)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active nvidia cycle: %w", err)
	}
	c.CycleStart, _ = time.Parse(time.RFC3339Nano, startStr)
	return &c, nil
}

// UpdateNvidiaCycle updates peak_usage and total_delta.
func (s *Store) UpdateNvidiaCycle(id int64, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE nvidia_reset_cycles SET peak_usage = ?, total_delta = ? WHERE id = ?`,
		peak, delta, id,
	)
	return err
}

// CloseNvidiaCycle closes an active cycle.
func (s *Store) CloseNvidiaCycle(id int64, end time.Time, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE nvidia_reset_cycles SET cycle_end = ?, peak_usage = ?, total_delta = ? WHERE id = ?`,
		end.UTC().Format(time.RFC3339Nano), peak, delta, id,
	)
	return err
}

// QueryNvidiaCycleHistory returns up to limit closed cycles.
func (s *Store) QueryNvidiaCycleHistory(quotaType string, limit int) ([]*NvidiaResetCycle, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, quota_type, cycle_start, cycle_end, peak_usage, total_delta
		FROM nvidia_reset_cycles WHERE quota_type = ? AND cycle_end IS NOT NULL
		ORDER BY cycle_start DESC LIMIT ?`,
		quotaType, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query nvidia cycle history: %w", err)
	}
	defer rows.Close()

	var out []*NvidiaResetCycle
	for rows.Next() {
		var c NvidiaResetCycle
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
