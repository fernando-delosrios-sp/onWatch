package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

// GoogleAIStudioResetCycle represents a Google AI Studio tracking cycle.
type GoogleAIStudioResetCycle struct {
	ID         int64
	QuotaType  string
	CycleStart time.Time
	CycleEnd   *time.Time
	PeakUsage  float64
	TotalDelta float64
}

// InsertGoogleAIStudioSnapshot inserts a Google AI Studio snapshot.
func (s *Store) InsertGoogleAIStudioSnapshot(snapshot *api.GoogleAIStudioSnapshot) (int64, error) {
	keyValid := 0
	if snapshot.KeyValid {
		keyValid = 1
	}
	res, err := s.db.Exec(
		`INSERT INTO google_aistudio_snapshots
		(captured_at, project, requests_24h, cap_usd, spent_usd, model_count, key_valid, raw_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshot.CapturedAt.UTC().Format(time.RFC3339Nano),
		snapshot.Project,
		snapshot.Requests24h,
		snapshot.CapUSD,
		snapshot.SpentUSD,
		snapshot.ModelCount,
		keyValid,
		snapshot.RawJSON,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert google_aistudio snapshot: %w", err)
	}
	return res.LastInsertId()
}

// QueryLatestGoogleAIStudio returns the most recent Google AI Studio snapshot.
func (s *Store) QueryLatestGoogleAIStudio() (*api.GoogleAIStudioSnapshot, error) {
	var snap api.GoogleAIStudioSnapshot
	var capturedAt string
	var project, rawJSON sql.NullString
	var keyValid int

	err := s.db.QueryRow(
		`SELECT id, captured_at, project, requests_24h, cap_usd, spent_usd, model_count, key_valid, raw_json
		FROM google_aistudio_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(&snap.ID, &capturedAt, &project, &snap.Requests24h, &snap.CapUSD, &snap.SpentUSD, &snap.ModelCount, &keyValid, &rawJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest google_aistudio: %w", err)
	}
	snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	snap.Project = project.String
	snap.KeyValid = keyValid == 1
	snap.RawJSON = rawJSON.String
	return &snap, nil
}

// QueryGoogleAIStudioRange returns Google AI Studio snapshots within [start, end] (bounded to 500).
func (s *Store) QueryGoogleAIStudioRange(start, end time.Time) ([]*api.GoogleAIStudioSnapshot, error) {
	rows, err := s.db.Query(
		`SELECT id, captured_at, project, requests_24h, cap_usd, spent_usd, model_count, key_valid, raw_json
		FROM google_aistudio_snapshots
		WHERE captured_at >= ? AND captured_at <= ?
		ORDER BY captured_at ASC LIMIT 500`,
		start.UTC().Format(time.RFC3339Nano),
		end.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query google_aistudio range: %w", err)
	}
	defer rows.Close()

	var out []*api.GoogleAIStudioSnapshot
	for rows.Next() {
		var snap api.GoogleAIStudioSnapshot
		var capturedAt string
		var project, rawJSON sql.NullString
		var keyValid int
		if err := rows.Scan(&snap.ID, &capturedAt, &project, &snap.Requests24h, &snap.CapUSD, &snap.SpentUSD, &snap.ModelCount, &keyValid, &rawJSON); err != nil {
			return nil, err
		}
		snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		snap.Project = project.String
		snap.KeyValid = keyValid == 1
		snap.RawJSON = rawJSON.String
		out = append(out, &snap)
	}
	return out, rows.Err()
}

// CreateGoogleAIStudioCycle creates a new Google AI Studio cycle.
func (s *Store) CreateGoogleAIStudioCycle(quotaType string, cycleStart time.Time) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO google_aistudio_reset_cycles (quota_type, cycle_start) VALUES (?, ?)`,
		quotaType, cycleStart.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create google_aistudio cycle: %w", err)
	}
	return res.LastInsertId()
}

// QueryActiveGoogleAIStudioCycle returns the active cycle for quotaType.
func (s *Store) QueryActiveGoogleAIStudioCycle(quotaType string) (*GoogleAIStudioResetCycle, error) {
	var c GoogleAIStudioResetCycle
	var startStr string
	err := s.db.QueryRow(
		`SELECT id, quota_type, cycle_start, peak_usage, total_delta
		FROM google_aistudio_reset_cycles WHERE quota_type = ? AND cycle_end IS NULL ORDER BY id DESC LIMIT 1`,
		quotaType,
	).Scan(&c.ID, &c.QuotaType, &startStr, &c.PeakUsage, &c.TotalDelta)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active google_aistudio cycle: %w", err)
	}
	c.CycleStart, _ = time.Parse(time.RFC3339Nano, startStr)
	return &c, nil
}

// UpdateGoogleAIStudioCycle updates peak_usage and total_delta.
func (s *Store) UpdateGoogleAIStudioCycle(id int64, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE google_aistudio_reset_cycles SET peak_usage = ?, total_delta = ? WHERE id = ?`,
		peak, delta, id,
	)
	return err
}

// CloseGoogleAIStudioCycle closes an active cycle.
func (s *Store) CloseGoogleAIStudioCycle(id int64, end time.Time, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE google_aistudio_reset_cycles SET cycle_end = ?, peak_usage = ?, total_delta = ? WHERE id = ?`,
		end.UTC().Format(time.RFC3339Nano), peak, delta, id,
	)
	return err
}

// QueryGoogleAIStudioCycleHistory returns up to limit closed cycles.
func (s *Store) QueryGoogleAIStudioCycleHistory(quotaType string, limit int) ([]*GoogleAIStudioResetCycle, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, quota_type, cycle_start, cycle_end, peak_usage, total_delta
		FROM google_aistudio_reset_cycles WHERE quota_type = ? AND cycle_end IS NOT NULL
		ORDER BY cycle_start DESC LIMIT ?`,
		quotaType, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query google_aistudio cycle history: %w", err)
	}
	defer rows.Close()

	var out []*GoogleAIStudioResetCycle
	for rows.Next() {
		var c GoogleAIStudioResetCycle
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
