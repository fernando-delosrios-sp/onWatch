package routing

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Store manages all routing_* SQLite persistence on top of the shared *sql.DB.
type Store struct {
	db *sql.DB
}

// NewStore creates a routing Store, runs idempotent table creation, and seeds defaults if empty.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("routing store: nil db")
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := s.seedDefaultsIfEmpty(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	schema := `
		CREATE TABLE IF NOT EXISTS routing_pools (
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			category TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			cap_usd REAL NOT NULL DEFAULT 0,
			reserve_usd REAL NOT NULL DEFAULT 0,
			spent_usd REAL NOT NULL DEFAULT 0,
			reset_day INTEGER NOT NULL DEFAULT 0,
			expires_at TEXT,
			window_hours INTEGER NOT NULL DEFAULT 0,
			pace_slack REAL NOT NULL DEFAULT 0.10,
			weight INTEGER NOT NULL DEFAULT 100,
			probe_source TEXT NOT NULL DEFAULT 'manual',
			cycle_start TEXT,
			notes TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS routing_models (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tier TEXT NOT NULL,
			pool_id TEXT NOT NULL,
			harness TEXT NOT NULL,
			provider TEXT NOT NULL,
			overlay TEXT NOT NULL DEFAULT '',
			model_id TEXT NOT NULL,
			billing TEXT NOT NULL DEFAULT 'byok',
			description TEXT NOT NULL DEFAULT '',
			priority INTEGER NOT NULL DEFAULT 100,
			override_pi_preference INTEGER NOT NULL DEFAULT 0,
			input_cost_per_1m REAL NOT NULL DEFAULT 0,
			output_cost_per_1m REAL NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			FOREIGN KEY (pool_id) REFERENCES routing_pools(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_routing_models_tier_pool ON routing_models(tier, pool_id, enabled);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_routing_models_unique ON routing_models(tier, pool_id, harness, model_id, overlay);

		CREATE TABLE IF NOT EXISTS routing_excluded_models (
			model_id TEXT PRIMARY KEY
		);

		CREATE TABLE IF NOT EXISTS routing_pool_health (
			pool_id TEXT PRIMARY KEY,
			cooldown_until TEXT,
			consecutive_errors INTEGER NOT NULL DEFAULT 0,
			last_429_at TEXT,
			last_bad_at TEXT,
			last_ok_at TEXT,
			avg_ttft_ms REAL NOT NULL DEFAULT 0,
			error_rate REAL NOT NULL DEFAULT 0,
			health_score REAL NOT NULL DEFAULT 100,
			last_reason TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			FOREIGN KEY (pool_id) REFERENCES routing_pools(id) ON DELETE CASCADE
		);

		CREATE TABLE IF NOT EXISTS routing_feedback (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pool_id TEXT NOT NULL,
			model_id TEXT NOT NULL DEFAULT '',
			harness TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			tier TEXT NOT NULL DEFAULT '',
			event TEXT NOT NULL DEFAULT '',
			outcome TEXT NOT NULL,
			status_code INTEGER NOT NULL DEFAULT 0,
			ttft_ms REAL NOT NULL DEFAULT 0,
			latency_ms REAL NOT NULL DEFAULT 0,
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT '',
			session TEXT NOT NULL DEFAULT '',
			issue TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_routing_feedback_pool_time ON routing_feedback(pool_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_routing_feedback_created ON routing_feedback(created_at DESC);

		CREATE TABLE IF NOT EXISTS routing_probe_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			probe_source TEXT NOT NULL,
			pool_id TEXT NOT NULL,
			used_usd REAL NOT NULL DEFAULT 0,
			limit_usd REAL NOT NULL DEFAULT 0,
			used_percent REAL NOT NULL DEFAULT 0,
			resets_at TEXT,
			extra_json TEXT NOT NULL DEFAULT '',
			captured_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_routing_probes_pool_time ON routing_probe_snapshots(pool_id, captured_at DESC);

		CREATE TABLE IF NOT EXISTS routing_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("routing migrate: %w", err)
	}
	return nil
}

func (s *Store) seedDefaultsIfEmpty() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM routing_pools`).Scan(&count); err != nil {
		return fmt.Errorf("routing check seed: %w", err)
	}
	if count > 0 {
		return nil
	}

	now := time.Now().UTC()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, p := range defaultSeedPools(now, cycleStart) {
		if err := upsertPoolTx(tx, p); err != nil {
			return fmt.Errorf("seed pool %s: %w", p.ID, err)
		}
		if err := ensureHealthTx(tx, p.ID, now); err != nil {
			return fmt.Errorf("seed health %s: %w", p.ID, err)
		}
	}

	for _, m := range defaultSeedModels() {
		if err := insertModelTx(tx, m); err != nil {
			return fmt.Errorf("seed model %s/%s: %w", m.PoolID, m.ModelID, err)
		}
	}

	for _, ex := range defaultExcludedModels() {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO routing_excluded_models (model_id) VALUES (?)`, ex); err != nil {
			return fmt.Errorf("seed excluded %s: %w", ex, err)
		}
	}

	return tx.Commit()
}

func upsertPoolTx(tx *sql.Tx, p Pool) error {
	var expiresAt, cycleStart interface{}
	if p.ExpiresAt != nil {
		expiresAt = p.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if p.CycleStart != nil {
		cycleStart = p.CycleStart.UTC().Format(time.RFC3339Nano)
	}
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	_, err := tx.Exec(`
		INSERT INTO routing_pools (
			id, display_name, category, enabled, cap_usd, reserve_usd, spent_usd,
			reset_day, expires_at, window_hours, pace_slack, weight, probe_source,
			cycle_start, notes, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			display_name = excluded.display_name,
			category = excluded.category,
			enabled = excluded.enabled,
			cap_usd = excluded.cap_usd,
			reserve_usd = excluded.reserve_usd,
			spent_usd = excluded.spent_usd,
			reset_day = excluded.reset_day,
			expires_at = excluded.expires_at,
			window_hours = excluded.window_hours,
			pace_slack = excluded.pace_slack,
			weight = excluded.weight,
			probe_source = excluded.probe_source,
			cycle_start = excluded.cycle_start,
			notes = excluded.notes,
			updated_at = excluded.updated_at
	`,
		p.ID, p.DisplayName, string(p.Category), enabled,
		p.CapUSD, p.ReserveUSD, p.SpentUSD, p.ResetDay,
		expiresAt, p.WindowHours, p.PaceSlack, p.Weight, p.ProbeSource,
		cycleStart, p.Notes, p.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func ensureHealthTx(tx *sql.Tx, poolID string, now time.Time) error {
	_, err := tx.Exec(`
		INSERT OR IGNORE INTO routing_pool_health (
			pool_id, consecutive_errors, avg_ttft_ms, error_rate, health_score, last_reason, updated_at
		) VALUES (?, 0, 0, 0, 100, '', ?)
	`, poolID, now.UTC().Format(time.RFC3339Nano))
	return err
}

func insertModelTx(tx *sql.Tx, m RouteModel) error {
	overridePi := 0
	if m.OverridePiPreference {
		overridePi = 1
	}
	enabled := 0
	if m.Enabled {
		enabled = 1
	}
	_, err := tx.Exec(`
		INSERT INTO routing_models (
			tier, pool_id, harness, provider, overlay, model_id, billing,
			description, priority, override_pi_preference, input_cost_per_1m,
			output_cost_per_1m, enabled
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tier, pool_id, harness, model_id, overlay) DO UPDATE SET
			provider = excluded.provider,
			billing = excluded.billing,
			description = excluded.description,
			priority = excluded.priority,
			override_pi_preference = excluded.override_pi_preference,
			input_cost_per_1m = excluded.input_cost_per_1m,
			output_cost_per_1m = excluded.output_cost_per_1m,
			enabled = excluded.enabled
	`,
		string(m.Tier), m.PoolID, m.Harness, m.Provider, m.Overlay, m.ModelID,
		m.Billing, m.Description, m.Priority, overridePi, m.InputCostPer1M,
		m.OutputCostPer1M, enabled,
	)
	return err
}

// ListPools returns all configured pools ordered deterministically.
func (s *Store) ListPools() ([]Pool, error) {
	rows, err := s.db.Query(`
		SELECT id, display_name, category, enabled, cap_usd, reserve_usd, spent_usd,
		       reset_day, expires_at, window_hours, pace_slack, weight, probe_source,
		       cycle_start, notes, updated_at
		FROM routing_pools
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list pools: %w", err)
	}
	defer rows.Close()

	var out []Pool
	for rows.Next() {
		var p Pool
		var cat, updatedAt string
		var enabled int
		var expiresAt, cycleStart sql.NullString
		if err := rows.Scan(
			&p.ID, &p.DisplayName, &cat, &enabled,
			&p.CapUSD, &p.ReserveUSD, &p.SpentUSD, &p.ResetDay,
			&expiresAt, &p.WindowHours, &p.PaceSlack, &p.Weight, &p.ProbeSource,
			&cycleStart, &p.Notes, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pool: %w", err)
		}
		p.Category = PoolCategory(cat)
		p.Enabled = enabled != 0
		p.ExpiresAt = parseNullTime(expiresAt)
		p.CycleStart = parseNullTime(cycleStart)
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPool returns a single pool by ID.
func (s *Store) GetPool(id string) (*Pool, error) {
	var p Pool
	var cat, updatedAt string
	var enabled int
	var expiresAt, cycleStart sql.NullString
	err := s.db.QueryRow(`
		SELECT id, display_name, category, enabled, cap_usd, reserve_usd, spent_usd,
		       reset_day, expires_at, window_hours, pace_slack, weight, probe_source,
		       cycle_start, notes, updated_at
		FROM routing_pools WHERE id = ?
	`, id).Scan(
		&p.ID, &p.DisplayName, &cat, &enabled,
		&p.CapUSD, &p.ReserveUSD, &p.SpentUSD, &p.ResetDay,
		&expiresAt, &p.WindowHours, &p.PaceSlack, &p.Weight, &p.ProbeSource,
		&cycleStart, &p.Notes, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pool %s: %w", id, err)
	}
	p.Category = PoolCategory(cat)
	p.Enabled = enabled != 0
	p.ExpiresAt = parseNullTime(expiresAt)
	p.CycleStart = parseNullTime(cycleStart)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &p, nil
}

// UpsertPool creates or updates a pool and ensures its health row exists.
func (s *Store) UpsertPool(p Pool) error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("pool id required")
	}
	if !ValidCategory(p.Category) {
		return fmt.Errorf("invalid category %q", p.Category)
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := upsertPoolTx(tx, p); err != nil {
		return err
	}
	if err := ensureHealthTx(tx, p.ID, p.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// AddPoolSpend increments spent_usd for a pool, rolling over monthly cycle_start if reset_day passed.
func (s *Store) AddPoolSpend(poolID string, deltaUSD float64, now time.Time) error {
	if deltaUSD <= 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var resetDay int
	var spent float64
	var cycleStartStr sql.NullString
	err = tx.QueryRow(`SELECT reset_day, spent_usd, cycle_start FROM routing_pools WHERE id = ?`, poolID).
		Scan(&resetDay, &spent, &cycleStartStr)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	cycleStart := parseNullTime(cycleStartStr)
	if resetDay >= 1 && resetDay <= 31 {
		curStart, _ := MonthlyCycleBounds(now.UTC(), resetDay)
		if cycleStart == nil || cycleStart.Before(curStart) {
			spent = 0
			cs := curStart
			cycleStart = &cs
		}
	}
	spent += deltaUSD

	var csArg interface{}
	if cycleStart != nil {
		csArg = cycleStart.UTC().Format(time.RFC3339Nano)
	}
	_, err = tx.Exec(`UPDATE routing_pools SET spent_usd = ?, cycle_start = ?, updated_at = ? WHERE id = ?`,
		spent, csArg, now.UTC().Format(time.RFC3339Nano), poolID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ListModels returns all configured model routes ordered by tier, priority DESC, id ASC.
func (s *Store) ListModels() ([]RouteModel, error) {
	rows, err := s.db.Query(`
		SELECT id, tier, pool_id, harness, provider, overlay, model_id, billing,
		       description, priority, override_pi_preference, input_cost_per_1m,
		       output_cost_per_1m, enabled
		FROM routing_models
		ORDER BY tier ASC, priority DESC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()

	var out []RouteModel
	for rows.Next() {
		var m RouteModel
		var tier string
		var overridePi, enabled int
		if err := rows.Scan(
			&m.ID, &tier, &m.PoolID, &m.Harness, &m.Provider, &m.Overlay,
			&m.ModelID, &m.Billing, &m.Description, &m.Priority,
			&overridePi, &m.InputCostPer1M, &m.OutputCostPer1M, &enabled,
		); err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}
		m.Tier = Tier(tier)
		m.OverridePiPreference = overridePi != 0
		m.Enabled = enabled != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpsertModel inserts or updates a route model entry.
func (s *Store) UpsertModel(m RouteModel) (int64, error) {
	if !ValidTier(m.Tier) {
		return 0, fmt.Errorf("invalid tier %q", m.Tier)
	}
	if strings.TrimSpace(m.PoolID) == "" || strings.TrimSpace(m.Harness) == "" || strings.TrimSpace(m.ModelID) == "" {
		return 0, fmt.Errorf("pool_id, harness, and model_id are required")
	}
	if m.Billing == "" {
		m.Billing = "byok"
	}
	overridePi := 0
	if m.OverridePiPreference {
		overridePi = 1
	}
	enabled := 0
	if m.Enabled {
		enabled = 1
	}
	if m.ID > 0 {
		_, err := s.db.Exec(`
			UPDATE routing_models
			SET tier = ?, pool_id = ?, harness = ?, provider = ?, overlay = ?,
			    model_id = ?, billing = ?, description = ?, priority = ?,
			    override_pi_preference = ?, input_cost_per_1m = ?, output_cost_per_1m = ?,
			    enabled = ?
			WHERE id = ?
		`,
			string(m.Tier), m.PoolID, m.Harness, m.Provider, m.Overlay,
			m.ModelID, m.Billing, m.Description, m.Priority,
			overridePi, m.InputCostPer1M, m.OutputCostPer1M, enabled, m.ID,
		)
		return m.ID, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := insertModelTx(tx, m); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(`
		SELECT id FROM routing_models WHERE tier = ? AND pool_id = ? AND harness = ? AND model_id = ? AND overlay = ?
	`, string(m.Tier), m.PoolID, m.Harness, m.ModelID, m.Overlay).Scan(&id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// DeleteModel deletes a route model entry by ID.
func (s *Store) DeleteModel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM routing_models WHERE id = ?`, id)
	return err
}

// ListExcludedModels returns all excluded model IDs sorted.
func (s *Store) ListExcludedModels() ([]string, error) {
	rows, err := s.db.Query(`SELECT model_id FROM routing_excluded_models ORDER BY model_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetExcludedModels replaces the excluded models list.
func (s *Store) SetExcludedModels(ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM routing_excluded_models`); err != nil {
		return err
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO routing_excluded_models (model_id) VALUES (?)`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReplaceCatalogModels replaces all models (and optionally pools & excluded models) atomically from YAML import.
func (s *Store) ReplaceCatalog(pools []Pool, models []RouteModel, excluded []string) error {
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, p := range pools {
		if p.UpdatedAt.IsZero() {
			p.UpdatedAt = now
		}
		if err := upsertPoolTx(tx, p); err != nil {
			return err
		}
		if err := ensureHealthTx(tx, p.ID, now); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`DELETE FROM routing_models`); err != nil {
		return err
	}
	for _, m := range models {
		if err := insertModelTx(tx, m); err != nil {
			return err
		}
	}

	if excluded != nil {
		if _, err := tx.Exec(`DELETE FROM routing_excluded_models`); err != nil {
			return err
		}
		for _, id := range excluded {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO routing_excluded_models (model_id) VALUES (?)`, id); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// GetPoolHealthMap returns all PoolHealth records keyed by pool ID.
func (s *Store) GetPoolHealthMap() (map[string]PoolHealth, error) {
	rows, err := s.db.Query(`
		SELECT pool_id, cooldown_until, consecutive_errors, last_429_at, last_bad_at,
		       last_ok_at, avg_ttft_ms, error_rate, health_score, last_reason, updated_at
		FROM routing_pool_health
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]PoolHealth)
	for rows.Next() {
		var h PoolHealth
		var cd, l429, lbad, lok sql.NullString
		var updated string
		if err := rows.Scan(
			&h.PoolID, &cd, &h.ConsecutiveErrors, &l429, &lbad, &lok,
			&h.AvgTTFTMs, &h.ErrorRate, &h.HealthScore, &h.LastReason, &updated,
		); err != nil {
			return nil, err
		}
		h.CooldownUntil = parseNullTime(cd)
		h.Last429At = parseNullTime(l429)
		h.LastBadAt = parseNullTime(lbad)
		h.LastOKAt = parseNullTime(lok)
		h.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out[h.PoolID] = h
	}
	return out, rows.Err()
}

// UpsertPoolHealth persists health state for a pool.
func (s *Store) UpsertPoolHealth(h PoolHealth) error {
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO routing_pool_health (
			pool_id, cooldown_until, consecutive_errors, last_429_at, last_bad_at,
			last_ok_at, avg_ttft_ms, error_rate, health_score, last_reason, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pool_id) DO UPDATE SET
			cooldown_until = excluded.cooldown_until,
			consecutive_errors = excluded.consecutive_errors,
			last_429_at = excluded.last_429_at,
			last_bad_at = excluded.last_bad_at,
			last_ok_at = excluded.last_ok_at,
			avg_ttft_ms = excluded.avg_ttft_ms,
			error_rate = excluded.error_rate,
			health_score = excluded.health_score,
			last_reason = excluded.last_reason,
			updated_at = excluded.updated_at
	`,
		h.PoolID, formatNullTime(h.CooldownUntil), h.ConsecutiveErrors,
		formatNullTime(h.Last429At), formatNullTime(h.LastBadAt), formatNullTime(h.LastOKAt),
		h.AvgTTFTMs, h.ErrorRate, h.HealthScore, h.LastReason, h.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// InsertFeedback records a feedback event in routing_feedback (bounded pruning keeps last 1000 rows).
func (s *Store) InsertFeedback(ev FeedbackEvent) (int64, error) {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.Exec(`
		INSERT INTO routing_feedback (
			pool_id, model_id, harness, provider, tier, event, outcome,
			status_code, ttft_ms, latency_ms, prompt_tokens, completion_tokens,
			cached_tokens, cost_usd, note, session, issue, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ev.PoolID, ev.ModelID, ev.Harness, ev.Provider, ev.Tier, ev.Event, ev.Outcome,
		ev.StatusCode, ev.TTFTMs, ev.LatencyMs, ev.PromptTokens, ev.CompletionTokens,
		ev.CachedTokens, ev.CostUSD, ev.Note, ev.Session, ev.Issue,
		ev.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	// Keep feedback history bounded to prevent unbounded DB growth.
	_, _ = s.db.Exec(`DELETE FROM routing_feedback WHERE id NOT IN (SELECT id FROM routing_feedback ORDER BY id DESC LIMIT 1000)`)
	return id, nil
}

// ListRecentFeedback returns up to limit recent feedback events (max 200).
func (s *Store) ListRecentFeedback(limit int) ([]FeedbackEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, pool_id, model_id, harness, provider, tier, event, outcome,
		       status_code, ttft_ms, latency_ms, prompt_tokens, completion_tokens,
		       cached_tokens, cost_usd, note, session, issue, created_at
		FROM routing_feedback
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FeedbackEvent
	for rows.Next() {
		var ev FeedbackEvent
		var createdAt string
		if err := rows.Scan(
			&ev.ID, &ev.PoolID, &ev.ModelID, &ev.Harness, &ev.Provider, &ev.Tier,
			&ev.Event, &ev.Outcome, &ev.StatusCode, &ev.TTFTMs, &ev.LatencyMs,
			&ev.PromptTokens, &ev.CompletionTokens, &ev.CachedTokens, &ev.CostUSD,
			&ev.Note, &ev.Session, &ev.Issue, &createdAt,
		); err != nil {
			return nil, err
		}
		ev.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// InsertProbeSnapshot stores a probe snapshot and prunes older rows per pool (keeps latest 200).
func (s *Store) InsertProbeSnapshot(snap ProbeSnapshot) error {
	if snap.CapturedAt.IsZero() {
		snap.CapturedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO routing_probe_snapshots (
			probe_source, pool_id, used_usd, limit_usd, used_percent, resets_at, extra_json, captured_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		snap.ProbeSource, snap.PoolID, snap.UsedUSD, snap.LimitUSD, snap.UsedPercent,
		formatNullTime(snap.ResetsAt), snap.ExtraJSON, snap.CapturedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`
		DELETE FROM routing_probe_snapshots
		WHERE pool_id = ? AND id NOT IN (
			SELECT id FROM routing_probe_snapshots WHERE pool_id = ? ORDER BY id DESC LIMIT 200
		)
	`, snap.PoolID, snap.PoolID)
	return nil
}

// QueryLatestProbeSnapshot returns the latest probe snapshot for a pool.
func (s *Store) QueryLatestProbeSnapshot(poolID string) (*ProbeSnapshot, error) {
	var snap ProbeSnapshot
	var resetsAt sql.NullString
	var capturedAt string
	err := s.db.QueryRow(`
		SELECT id, probe_source, pool_id, used_usd, limit_usd, used_percent, resets_at, extra_json, captured_at
		FROM routing_probe_snapshots
		WHERE pool_id = ?
		ORDER BY id DESC
		LIMIT 1
	`, poolID).Scan(
		&snap.ID, &snap.ProbeSource, &snap.PoolID, &snap.UsedUSD, &snap.LimitUSD,
		&snap.UsedPercent, &resetsAt, &snap.ExtraJSON, &capturedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snap.ResetsAt = parseNullTime(resetsAt)
	snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	return &snap, nil
}

// GetSetting reads a routing_settings value.
func (s *Store) GetSetting(key string) (string, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM routing_settings WHERE key = ?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetSetting upserts a routing_settings value.
func (s *Store) SetSetting(key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`
		INSERT INTO routing_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, key, value, now)
	return err
}

func parseNullTime(ns sql.NullString) *time.Time {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, ns.String); err == nil {
		u := t.UTC()
		return &u
	}
	if t, err := time.Parse(time.RFC3339, ns.String); err == nil {
		u := t.UTC()
		return &u
	}
	return nil
}

func formatNullTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func defaultSeedPools(now, cycleStart time.Time) []Pool {
	cs := cycleStart
	return []Pool{
		{
			ID: "nvidia-free", DisplayName: "NVIDIA Free", Category: CategoryFree, Enabled: true,
			PaceSlack: 0.10, Weight: 100, ProbeSource: "feedback_health",
			Notes: "$0 free pool via OpenRouter @preset/code-nvidia (health-gated)", UpdatedAt: now,
		},
		{
			ID: "antigravity-3p", DisplayName: "Antigravity Claude+GPT", Category: CategoryMonthlyCooldown, Enabled: true,
			WindowHours: 168, PaceSlack: 0.10, Weight: 100, ProbeSource: "antigravity_3p",
			Notes: "Antigravity 3P (Claude Opus/Sonnet 5.5, GPT-OSS) 5h & 7d windows", UpdatedAt: now,
		},
		{
			ID: "antigravity-gemini", DisplayName: "Antigravity Gemini", Category: CategoryMonthlyCooldown, Enabled: true,
			WindowHours: 168, PaceSlack: 0.10, Weight: 95, ProbeSource: "antigravity_gemini",
			Notes: "Antigravity Gemini Pro/Argon/Hydrogen 5h & 7d windows", UpdatedAt: now,
		},
		{
			ID: "antigravity-flash", DisplayName: "Antigravity Flash", Category: CategoryMonthlyCooldown, Enabled: true,
			WindowHours: 168, PaceSlack: 0.10, Weight: 90, ProbeSource: "antigravity_flash",
			Notes: "Antigravity Gemini Flash window", UpdatedAt: now,
		},
		{
			ID: "xai", DisplayName: "xAI / Grok", Category: CategoryMonthlyCooldown, Enabled: true,
			WindowHours: 720, PaceSlack: 0.10, Weight: 90, ProbeSource: "grok",
			Notes: "Grok subscription (100% combined Grok Build + OpenRouter BYOK)", UpdatedAt: now,
		},
		{
			ID: "claude", DisplayName: "Claude Subscription", Category: CategoryMonthlyCooldown, Enabled: true,
			WindowHours: 168, PaceSlack: 0.10, Weight: 85, ProbeSource: "anthropic",
			Notes: "Anthropic Claude Pro/Max 5h & 7d windows", UpdatedAt: now,
		},
		{
			ID: "codex", DisplayName: "Codex / ChatGPT", Category: CategoryMonthlyCooldown, Enabled: true,
			WindowHours: 168, PaceSlack: 0.10, Weight: 85, ProbeSource: "codex",
			Notes: "OpenAI Codex 5h & 7d windows", UpdatedAt: now,
		},
		{
			ID: "cursor", DisplayName: "Cursor", Category: CategoryMonthlyNoCooldown, Enabled: true,
			CapUSD: 500, ReserveUSD: 0, ResetDay: 1, PaceSlack: 0.10, Weight: 90, ProbeSource: "cursor",
			CycleStart: &cs, Notes: "Cursor $500/mo cap + reserve, resets day 1", UpdatedAt: now,
		},
		{
			ID: "google-ai-studio", DisplayName: "Google AI Studio", Category: CategoryMonthlyNoCooldown, Enabled: true,
			CapUSD: 15, ReserveUSD: 0, ResetDay: 1, PaceSlack: 0.10, Weight: 85, ProbeSource: "google_ai_studio",
			CycleStart: &cs, Notes: "$15/mo credit voucher via @preset/code-google", UpdatedAt: now,
		},
		{
			ID: "chutes", DisplayName: "Chutes PAYG", Category: CategoryPAYG, Enabled: true,
			CapUSD: 5, ReserveUSD: 0, ResetDay: 1, PaceSlack: 0.10, Weight: 100, ProbeSource: "chutes",
			CycleStart: &cs, Notes: "Chutes $5 PAYG cap with prompt caching via @preset/code-chutes", UpdatedAt: now,
		},
		{
			ID: "openrouter-payg", DisplayName: "OpenRouter PAYG", Category: CategoryPAYG, Enabled: true,
			CapUSD: 5, ReserveUSD: 0, ResetDay: 1, PaceSlack: 0.10, Weight: 80, ProbeSource: "openrouter",
			CycleStart: &cs, Notes: "$5/mo PAYG cap on t3-code-main via @preset/code-payg", UpdatedAt: now,
		},
	}
}

func defaultSeedModels() []RouteModel {
	return []RouteModel{
		// --- Frontier ---
		{Tier: TierFrontier, PoolID: "antigravity-3p", Harness: "pi", Provider: "antigravity", ModelID: "claude-opus-5-5", Billing: "byok", Description: "Top-tier Claude Opus 5.5 for deep architecture, complex debugging, and multi-file refactors", Priority: 120, Enabled: true},
		{Tier: TierFrontier, PoolID: "antigravity-3p", Harness: "pi", Provider: "antigravity", ModelID: "claude-sonnet-5-5", Billing: "byok", Description: "Fast frontier coding and code review via Antigravity", Priority: 100, Enabled: true},
		{Tier: TierFrontier, PoolID: "antigravity-3p", Harness: "antigravity", Provider: "antigravity", ModelID: "claude-opus-5-5", Billing: "byok", Description: "Native Antigravity Claude Opus 5.5", Priority: 120, Enabled: true},
		{Tier: TierFrontier, PoolID: "antigravity-3p", Harness: "antigravity", Provider: "antigravity", ModelID: "claude-sonnet-5-5", Billing: "byok", Description: "Native Antigravity Claude Sonnet 5.5", Priority: 100, Enabled: true},

		{Tier: TierFrontier, PoolID: "antigravity-gemini", Harness: "pi", Provider: "antigravity", ModelID: "gemini-4-argon", Billing: "byok", Description: "Gemini 4 Argon for deep reasoning and long-context codebase synthesis", Priority: 110, Enabled: true},
		{Tier: TierFrontier, PoolID: "antigravity-gemini", Harness: "antigravity", Provider: "antigravity", ModelID: "gemini-4-argon", Billing: "byok", Description: "Native Antigravity Gemini 4 Argon", Priority: 110, Enabled: true},

		{Tier: TierFrontier, PoolID: "xai", Harness: "pi", Provider: "openrouter", Overlay: "@preset/code-xai", ModelID: "x-ai/grok-4.7", Billing: "byok", Description: "Frontier Grok 4.7 reasoning & coding via xAI BYOK", Priority: 110, Enabled: true},
		{Tier: TierFrontier, PoolID: "xai", Harness: "pi", Provider: "openrouter", Overlay: "@preset/code-xai", ModelID: "x-ai/grok-4.6", Billing: "byok", Description: "Grok 4.6 fallback within xAI BYOK", Priority: 90, Enabled: true},
		{Tier: TierFrontier, PoolID: "xai", Harness: "grok", Provider: "grok", ModelID: "grok-4.7", Billing: "byok", Description: "Native Grok CLI Grok 4.7", Priority: 110, Enabled: true},
		{Tier: TierFrontier, PoolID: "xai", Harness: "grok", Provider: "grok", ModelID: "grok-4.6", Billing: "byok", Description: "Native Grok CLI Grok 4.6", Priority: 90, Enabled: true},

		{Tier: TierFrontier, PoolID: "claude", Harness: "pi", Provider: "anthropic", ModelID: "claude-opus-5-5", Billing: "byok", Description: "Claude Opus 5.5 direct subscription", Priority: 110, Enabled: true},
		{Tier: TierFrontier, PoolID: "claude", Harness: "claudeAgent", Provider: "claudeAgent", ModelID: "claude-opus-5-5", Billing: "byok", Description: "Native Claude Code Opus 5.5", Priority: 110, Enabled: true},
		{Tier: TierFrontier, PoolID: "claude", Harness: "claudeAgent", Provider: "claudeAgent", ModelID: "claude-sonnet-5-5", Billing: "byok", Description: "Native Claude Code Sonnet 5.5", Priority: 95, Enabled: true},

		{Tier: TierFrontier, PoolID: "codex", Harness: "pi", Provider: "openai", ModelID: "gpt-5.3-codex", Billing: "byok", Description: "Codex 5.3 frontier software engineering", Priority: 110, Enabled: true},
		{Tier: TierFrontier, PoolID: "codex", Harness: "codex", Provider: "codex", ModelID: "gpt-5.3-codex", Billing: "byok", Description: "Native Codex CLI 5.3", Priority: 110, Enabled: true},

		{Tier: TierFrontier, PoolID: "cursor", Harness: "cursor", Provider: "cursor", ModelID: "claude-opus-5-5", Billing: "byok", Description: "Cursor Claude Opus 5.5 for complex implementation", Priority: 120, Enabled: true},
		{Tier: TierFrontier, PoolID: "cursor", Harness: "cursor", Provider: "cursor", ModelID: "gpt-5.6-sol", Billing: "byok", Description: "Cursor GPT-5.6 Sol for deep verification and math/logic", Priority: 110, Enabled: true},
		{Tier: TierFrontier, PoolID: "cursor", Harness: "cursor", Provider: "cursor", ModelID: "grok-4.7", Billing: "byok", Description: "Cursor Grok 4.7", Priority: 100, Enabled: true},
		{Tier: TierFrontier, PoolID: "cursor", Harness: "cursor", Provider: "cursor", ModelID: "gemini-4-carbon", Billing: "byok", Description: "Cursor Gemini 4 Carbon for large multi-file context", Priority: 95, Enabled: true},

		{Tier: TierFrontier, PoolID: "google-ai-studio", Harness: "pi", Provider: "google-ai-studio", Overlay: "@preset/code-google", ModelID: "google/gemini-4-carbon", Billing: "byok", Description: "1M+ context Gemini 4 Carbon via Google AI Studio voucher", Priority: 110, InputCostPer1M: 1.25, OutputCostPer1M: 10.0, Enabled: true},

		{Tier: TierFrontier, PoolID: "chutes", Harness: "pi", Provider: "chutes", Overlay: "@preset/code-chutes", ModelID: "z-ai/glm-5.1", Billing: "byok", Description: "Frontier open-weights GLM-5.1 on Chutes with prompt caching", Priority: 110, InputCostPer1M: 0.50, OutputCostPer1M: 2.00, Enabled: true},

		{Tier: TierFrontier, PoolID: "openrouter-payg", Harness: "pi", Provider: "openrouter", Overlay: "@preset/code-payg", ModelID: "openai/gpt-6.1-terra", Billing: "payg", Description: "GPT-6.1 Terra balanced frontier PAYG", Priority: 110, InputCostPer1M: 2.00, OutputCostPer1M: 8.00, Enabled: true},
		{Tier: TierFrontier, PoolID: "openrouter-payg", Harness: "pi", Provider: "openrouter", Overlay: "@preset/code-payg", ModelID: "openai/gpt-6.1-sol-pro", Billing: "payg", Description: "GPT-6.1 Sol Pro hardest problems PAYG", Priority: 100, InputCostPer1M: 5.00, OutputCostPer1M: 15.00, Enabled: true},

		// --- Workhorse ---
		{Tier: TierWorkhorse, PoolID: "nvidia-free", Harness: "pi", Provider: "nvidia", Overlay: "@preset/code-nvidia", ModelID: "nvidia/nemotron-3-ultra-550b-a55b:free", Billing: "free", Description: "$0 Nemotron Ultra 550B for everyday coding and tests", Priority: 110, Enabled: true},

		{Tier: TierWorkhorse, PoolID: "antigravity-gemini", Harness: "pi", Provider: "antigravity", ModelID: "gemini-4-hydrogen", Billing: "byok", Description: "Gemini 4 Hydrogen fast balanced coding workhorse", Priority: 110, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "antigravity-gemini", Harness: "antigravity", Provider: "antigravity", ModelID: "gemini-4-hydrogen", Billing: "byok", Description: "Native Antigravity Gemini 4 Hydrogen", Priority: 110, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "antigravity-3p", Harness: "pi", Provider: "antigravity", ModelID: "claude-sonnet-5-5", Billing: "byok", Description: "Claude Sonnet 5.5 via Antigravity 3P", Priority: 105, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "antigravity-3p", Harness: "antigravity", Provider: "antigravity", ModelID: "claude-sonnet-5-5", Billing: "byok", Description: "Native Antigravity Claude Sonnet 5.5", Priority: 105, Enabled: true},

		{Tier: TierWorkhorse, PoolID: "xai", Harness: "pi", Provider: "openrouter", Overlay: "@preset/code-xai", ModelID: "x-ai/grok-build-0.1", Billing: "byok", Description: "Fast agentic coding Grok Build 0.1 via xAI BYOK", Priority: 115, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "xai", Harness: "pi", Provider: "openrouter", Overlay: "@preset/code-xai", ModelID: "x-ai/grok-4.3", Billing: "byok", Description: "Grok 4.3 1M context window via xAI BYOK", Priority: 100, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "xai", Harness: "grok", Provider: "grok", ModelID: "grok-4.7-build-fast", Billing: "byok", Description: "Native Grok CLI fast build workhorse", Priority: 110, Enabled: true},

		{Tier: TierWorkhorse, PoolID: "claude", Harness: "pi", Provider: "anthropic", ModelID: "claude-sonnet-5-5", Billing: "byok", Description: "Claude Sonnet 5.5 direct subscription", Priority: 110, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "claude", Harness: "claudeAgent", Provider: "claudeAgent", ModelID: "claude-sonnet-5-5", Billing: "byok", Description: "Native Claude Code Sonnet 5.5", Priority: 110, Enabled: true},

		{Tier: TierWorkhorse, PoolID: "codex", Harness: "pi", Provider: "openai", ModelID: "gpt-5.3-codex", Billing: "byok", Description: "Codex 5.3 daily workhorse", Priority: 100, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "codex", Harness: "codex", Provider: "codex", ModelID: "gpt-5.3-codex", Billing: "byok", Description: "Native Codex CLI 5.3", Priority: 100, Enabled: true},

		{Tier: TierWorkhorse, PoolID: "cursor", Harness: "cursor", Provider: "cursor", ModelID: "composer-2.5", Billing: "byok", Description: "Cursor Composer 2.5 fast multi-file edits", Priority: 115, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "cursor", Harness: "cursor", Provider: "cursor", ModelID: "claude-sonnet-5-5", Billing: "byok", Description: "Cursor Claude Sonnet 5.5", Priority: 105, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "cursor", Harness: "cursor", Provider: "cursor", ModelID: "gpt-5.5", Billing: "byok", Description: "Cursor GPT-5.5", Priority: 95, Enabled: true},

		{Tier: TierWorkhorse, PoolID: "google-ai-studio", Harness: "pi", Provider: "google-ai-studio", Overlay: "@preset/code-google", ModelID: "google/gemini-4-hydrogen", Billing: "byok", Description: "Gemini 4 Hydrogen 1M context via Google AI Studio voucher", Priority: 115, InputCostPer1M: 0.30, OutputCostPer1M: 2.50, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "google-ai-studio", Harness: "pi", Provider: "google-ai-studio", Overlay: "@preset/code-google", ModelID: "google/gemini-3.8-flash", Billing: "byok", Description: "Gemini 3.8 Flash fast coding via Google AI Studio voucher", Priority: 100, InputCostPer1M: 0.15, OutputCostPer1M: 0.60, Enabled: true},

		{Tier: TierWorkhorse, PoolID: "chutes", Harness: "pi", Provider: "chutes", Overlay: "@preset/code-chutes", ModelID: "moonshotai/kimi-k2.6", Billing: "byok", Description: "Kimi K2.6 strong agentic coding on Chutes", Priority: 110, InputCostPer1M: 0.25, OutputCostPer1M: 1.00, Enabled: true},
		{Tier: TierWorkhorse, PoolID: "chutes", Harness: "pi", Provider: "chutes", Overlay: "@preset/code-chutes", ModelID: "deepseek/deepseek-v4-flash-0731", Billing: "byok", Description: "DeepSeek V4 Flash fast value coding on Chutes", Priority: 100, InputCostPer1M: 0.14, OutputCostPer1M: 0.42, Enabled: true},

		{Tier: TierWorkhorse, PoolID: "openrouter-payg", Harness: "pi", Provider: "openrouter", Overlay: "@preset/code-payg", ModelID: "deepseek/deepseek-v4.1-flash-1017", Billing: "payg", Description: "DeepSeek V4.1 Flash cheap PAYG workhorse", Priority: 110, InputCostPer1M: 0.20, OutputCostPer1M: 0.60, Enabled: true},

		// --- Lightweight ---
		{Tier: TierLightweight, PoolID: "nvidia-free", Harness: "pi", Provider: "nvidia", Overlay: "@preset/code-nvidia", ModelID: "nvidia/nemotron-3.5-lightning:free", Billing: "free", Description: "$0 fast Nemotron Lightning for boilerplate, linting, and subagents", Priority: 120, Enabled: true},
		{Tier: TierLightweight, PoolID: "nvidia-free", Harness: "pi", Provider: "nvidia", Overlay: "@preset/code-nvidia", ModelID: "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free", Billing: "free", Description: "$0 compact reasoning model for quick tasks", Priority: 100, Enabled: true},

		{Tier: TierLightweight, PoolID: "antigravity-flash", Harness: "pi", Provider: "antigravity", ModelID: "gemini-3.8-flash", Billing: "byok", Description: "Fast Gemini 3.8 Flash via Antigravity", Priority: 110, Enabled: true},
		{Tier: TierLightweight, PoolID: "antigravity-flash", Harness: "antigravity", Provider: "antigravity", ModelID: "gemini-3.8-flash", Billing: "byok", Description: "Native Antigravity Gemini 3.8 Flash", Priority: 110, Enabled: true},

		{Tier: TierLightweight, PoolID: "xai", Harness: "pi", Provider: "openrouter", Overlay: "@preset/code-xai", ModelID: "x-ai/grok-build-0.1", Billing: "byok", Description: "Fast Grok Build 0.1 for lightweight tasks", Priority: 100, Enabled: true},
		{Tier: TierLightweight, PoolID: "xai", Harness: "grok", Provider: "grok", ModelID: "grok-4.7-build-fast", Billing: "byok", Description: "Native Grok CLI fast build", Priority: 100, Enabled: true},

		{Tier: TierLightweight, PoolID: "claude", Harness: "pi", Provider: "anthropic", ModelID: "claude-haiku-5-5", Billing: "byok", Description: "Claude Haiku 5.5 quick edits", Priority: 100, Enabled: true},
		{Tier: TierLightweight, PoolID: "claude", Harness: "claudeAgent", Provider: "claudeAgent", ModelID: "claude-haiku-5-5", Billing: "byok", Description: "Native Claude Code Haiku 5.5", Priority: 100, Enabled: true},

		{Tier: TierLightweight, PoolID: "codex", Harness: "pi", Provider: "openai", ModelID: "gpt-5.4-mini", Billing: "byok", Description: "GPT-5.4 Mini quick tasks", Priority: 100, Enabled: true},
		{Tier: TierLightweight, PoolID: "codex", Harness: "codex", Provider: "codex", ModelID: "gpt-5.4-mini", Billing: "byok", Description: "Native Codex GPT-5.4 Mini", Priority: 100, Enabled: true},

		{Tier: TierLightweight, PoolID: "cursor", Harness: "cursor", Provider: "cursor", ModelID: "gemini-3.8-flash", Billing: "byok", Description: "Cursor Gemini 3.8 Flash", Priority: 110, Enabled: true},
		{Tier: TierLightweight, PoolID: "cursor", Harness: "cursor", Provider: "cursor", ModelID: "gpt-5.4-mini", Billing: "byok", Description: "Cursor GPT-5.4 Mini", Priority: 100, Enabled: true},

		{Tier: TierLightweight, PoolID: "google-ai-studio", Harness: "pi", Provider: "google-ai-studio", Overlay: "@preset/code-google", ModelID: "google/gemini-3.8-flash", Billing: "byok", Description: "Fast Gemini 3.8 Flash via Google AI Studio voucher", Priority: 110, InputCostPer1M: 0.15, OutputCostPer1M: 0.60, Enabled: true},
		{Tier: TierLightweight, PoolID: "google-ai-studio", Harness: "pi", Provider: "google-ai-studio", Overlay: "@preset/code-google", ModelID: "google/gemma-4-31b-it:free", Billing: "free", Description: "Free Gemma 4 31B on Google AI Studio", Priority: 90, Enabled: true},

		{Tier: TierLightweight, PoolID: "chutes", Harness: "pi", Provider: "chutes", Overlay: "@preset/code-chutes", ModelID: "qwen/qwen3.8-27b", Billing: "byok", Description: "Fast Qwen 3.8 27B open-weights on Chutes", Priority: 110, InputCostPer1M: 0.08, OutputCostPer1M: 0.24, Enabled: true},

		{Tier: TierLightweight, PoolID: "openrouter-payg", Harness: "pi", Provider: "openrouter", Overlay: "@preset/code-payg", ModelID: "deepseek/deepseek-v4.1-flash-1017", Billing: "payg", Description: "DeepSeek V4.1 Flash PAYG fallback", Priority: 100, InputCostPer1M: 0.20, OutputCostPer1M: 0.60, Enabled: true},
	}
}

func defaultExcludedModels() []string {
	return []string{
		"x-ai/grok-4.20",
		"anthropic/claude-opus-5.5",
		"anthropic/claude-haiku-5.5",
		"openai/gpt-6-luna",
		"deepseek/deepseek-v4-pro",
		"z-ai/glm-5.3-flashx",
	}
}
