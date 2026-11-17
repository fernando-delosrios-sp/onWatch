package routing

import (
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v2"
)

// CatalogYAML mirrors the onwatch-catalog.yaml contract consumed by openrouter (or-iac import-catalog).
type CatalogYAML struct {
	SchemaVersion  int                        `yaml:"schema_version" json:"schema_version"`
	Source         string                     `yaml:"source" json:"source"`
	GeneratedAt    string                     `yaml:"generated_at" json:"generated_at"`
	Pools          []CatalogPoolYAML          `yaml:"pools,omitempty" json:"pools,omitempty"`
	Tiers          map[Tier][]CatalogModelYAML `yaml:"tiers" json:"tiers"`
	ExcludedModels []string                   `yaml:"excluded_models" json:"excluded_models"`
}

// CatalogPoolYAML represents a pool definition inside the catalog YAML.
type CatalogPoolYAML struct {
	ID          string       `yaml:"id" json:"id"`
	DisplayName string       `yaml:"display_name" json:"display_name"`
	Category    PoolCategory `yaml:"category" json:"category"`
	Enabled     bool         `yaml:"enabled" json:"enabled"`
	CapUSD      float64      `yaml:"cap_usd,omitempty" json:"cap_usd,omitempty"`
	ReserveUSD  float64      `yaml:"reserve_usd,omitempty" json:"reserve_usd,omitempty"`
	ResetDay    int          `yaml:"reset_day,omitempty" json:"reset_day,omitempty"`
	ExpiresAt   string       `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
	WindowHours int          `yaml:"window_hours,omitempty" json:"window_hours,omitempty"`
	PaceSlack   float64      `yaml:"pace_slack,omitempty" json:"pace_slack,omitempty"`
	Weight      int          `yaml:"weight,omitempty" json:"weight,omitempty"`
	ProbeSource string       `yaml:"probe_source,omitempty" json:"probe_source,omitempty"`
	Notes       string       `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// CatalogModelYAML represents one model route entry inside a tier.
type CatalogModelYAML struct {
	ID                   string  `yaml:"id" json:"id"`
	Provider             string  `yaml:"provider" json:"provider"`
	Billing              string  `yaml:"billing" json:"billing"`
	Enabled              bool    `yaml:"enabled" json:"enabled"`
	Notes                string  `yaml:"notes,omitempty" json:"notes,omitempty"`
	Pool                 string  `yaml:"pool,omitempty" json:"pool,omitempty"`
	Harness              string  `yaml:"harness,omitempty" json:"harness,omitempty"`
	Overlay              string  `yaml:"overlay,omitempty" json:"overlay,omitempty"`
	Priority             int     `yaml:"priority,omitempty" json:"priority,omitempty"`
	OverridePiPreference bool    `yaml:"override_pi_preference,omitempty" json:"override_pi_preference,omitempty"`
	InputCostPer1M       float64 `yaml:"input_cost_per_1m,omitempty" json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M      float64 `yaml:"output_cost_per_1m,omitempty" json:"output_cost_per_1m,omitempty"`
}

// BuildCatalog serializes the current routing pools, 3-tier models, and excluded models into CatalogYAML.
func (e *Engine) BuildCatalog(now time.Time) (*CatalogYAML, error) {
	if e == nil || e.store == nil {
		return nil, fmt.Errorf("routing engine not initialized")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	pools, err := e.store.ListPools()
	if err != nil {
		return nil, err
	}
	models, err := e.store.ListModels()
	if err != nil {
		return nil, err
	}
	excluded, err := e.store.ListExcludedModels()
	if err != nil {
		return nil, err
	}
	if excluded == nil {
		excluded = []string{}
	}

	catPools := make([]CatalogPoolYAML, 0, len(pools))
	for _, p := range pools {
		cp := CatalogPoolYAML{
			ID:          p.ID,
			DisplayName: p.DisplayName,
			Category:    p.Category,
			Enabled:     p.Enabled,
			CapUSD:      p.CapUSD,
			ReserveUSD:  p.ReserveUSD,
			ResetDay:    p.ResetDay,
			WindowHours: p.WindowHours,
			PaceSlack:   p.PaceSlack,
			Weight:      p.Weight,
			ProbeSource: p.ProbeSource,
			Notes:       p.Notes,
		}
		if p.ExpiresAt != nil {
			cp.ExpiresAt = p.ExpiresAt.UTC().Format(time.RFC3339)
		}
		catPools = append(catPools, cp)
	}

	tiers := map[Tier][]CatalogModelYAML{
		TierLightweight: {},
		TierWorkhorse:   {},
		TierFrontier:    {},
	}
	for _, m := range models {
		tiers[m.Tier] = append(tiers[m.Tier], CatalogModelYAML{
			ID:                   m.ModelID,
			Provider:             m.Provider,
			Billing:              m.Billing,
			Enabled:              m.Enabled,
			Notes:                m.Description,
			Pool:                 m.PoolID,
			Harness:              m.Harness,
			Overlay:              m.Overlay,
			Priority:             m.Priority,
			OverridePiPreference: m.OverridePiPreference,
			InputCostPer1M:       m.InputCostPer1M,
			OutputCostPer1M:      m.OutputCostPer1M,
		})
	}

	return &CatalogYAML{
		SchemaVersion:  1,
		Source:         "onwatch",
		GeneratedAt:    now.Format(time.RFC3339),
		Pools:          catPools,
		Tiers:          tiers,
		ExcludedModels: excluded,
	}, nil
}

// ExportCatalogYAML marshals the current catalog as YAML bytes.
func (e *Engine) ExportCatalogYAML(now time.Time) ([]byte, error) {
	cat, err := e.BuildCatalog(now)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(cat)
}

// ImportCatalog parses and validates a CatalogYAML and atomically updates the routing catalog.
func (e *Engine) ImportCatalog(cat *CatalogYAML) error {
	if e == nil || e.store == nil {
		return fmt.Errorf("routing engine not initialized")
	}
	if cat == nil {
		return fmt.Errorf("catalog is nil")
	}
	if cat.SchemaVersion != 0 && cat.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d (expected 1)", cat.SchemaVersion)
	}

	existingPools, err := e.store.ListPools()
	if err != nil {
		return err
	}
	poolByID := make(map[string]Pool, len(existingPools))
	for _, p := range existingPools {
		poolByID[p.ID] = p
	}

	now := time.Now().UTC()
	var poolsToUpsert []Pool
	for _, cp := range cat.Pools {
		id := strings.TrimSpace(cp.ID)
		if id == "" {
			return fmt.Errorf("pool id cannot be empty")
		}
		if !ValidCategory(cp.Category) {
			return fmt.Errorf("invalid category %q for pool %q", cp.Category, id)
		}
		p := poolByID[id]
		p.ID = id
		p.DisplayName = cp.DisplayName
		if p.DisplayName == "" {
			p.DisplayName = id
		}
		p.Category = cp.Category
		p.Enabled = cp.Enabled
		p.CapUSD = cp.CapUSD
		p.ReserveUSD = cp.ReserveUSD
		p.ResetDay = cp.ResetDay
		p.WindowHours = cp.WindowHours
		p.PaceSlack = cp.PaceSlack
		if cp.Weight > 0 {
			p.Weight = cp.Weight
		} else if p.Weight == 0 {
			p.Weight = 100
		}
		if cp.ProbeSource != "" {
			p.ProbeSource = cp.ProbeSource
		}
		p.Notes = cp.Notes
		if cp.ExpiresAt != "" {
			t := parseTimeFlexible(cp.ExpiresAt)
			if t == nil {
				return fmt.Errorf("invalid expires_at %q on pool %q", cp.ExpiresAt, id)
			}
			p.ExpiresAt = t
		} else {
			p.ExpiresAt = nil
		}
		p.UpdatedAt = now
		poolByID[id] = p
		poolsToUpsert = append(poolsToUpsert, p)
	}

	var models []RouteModel
	for tier, entries := range cat.Tiers {
		if !ValidTier(tier) {
			return fmt.Errorf("invalid tier %q", tier)
		}
		for _, entry := range entries {
			modelID := strings.TrimSpace(entry.ID)
			if modelID == "" {
				return fmt.Errorf("model id cannot be empty in tier %q", tier)
			}
			poolID := strings.TrimSpace(entry.Pool)
			if poolID == "" {
				poolID = inferPoolFromCatalogEntry(entry)
			}
			if _, ok := poolByID[poolID]; !ok {
				return fmt.Errorf("model %q references unknown pool %q", modelID, poolID)
			}
			harness := strings.TrimSpace(entry.Harness)
			if harness == "" {
				harness = inferHarnessFromEntry(entry)
			}
			provider := strings.TrimSpace(entry.Provider)
			if provider == "" {
				provider = harness
			}
			billing := strings.TrimSpace(entry.Billing)
			if billing == "" {
				billing = "byok"
			}
			prio := entry.Priority
			if prio == 0 {
				prio = 100
			}
			models = append(models, RouteModel{
				Tier:                 tier,
				PoolID:               poolID,
				Harness:              harness,
				Provider:             provider,
				Overlay:              strings.TrimSpace(entry.Overlay),
				ModelID:              modelID,
				Billing:              billing,
				Description:          strings.TrimSpace(entry.Notes),
				Priority:             prio,
				OverridePiPreference: entry.OverridePiPreference,
				InputCostPer1M:       entry.InputCostPer1M,
				OutputCostPer1M:      entry.OutputCostPer1M,
				Enabled:              entry.Enabled,
			})
		}
	}

	return e.store.ReplaceCatalog(poolsToUpsert, models, cat.ExcludedModels)
}

// ImportCatalogYAML unmarshals YAML bytes and imports the catalog.
func (e *Engine) ImportCatalogYAML(data []byte) error {
	var cat CatalogYAML
	if err := yaml.UnmarshalStrict(data, &cat); err != nil {
		// Fallback to non-strict in case extra caller metadata fields exist.
		if err2 := yaml.Unmarshal(data, &cat); err2 != nil {
			return fmt.Errorf("parse catalog yaml: %w", err2)
		}
	}
	return e.ImportCatalog(&cat)
}

func inferPoolFromCatalogEntry(entry CatalogModelYAML) string {
	switch strings.ToLower(strings.TrimSpace(entry.Billing)) {
	case "free":
		return "nvidia-free"
	case "payg":
		return "openrouter-payg"
	}
	switch strings.ToLower(strings.TrimSpace(entry.Provider)) {
	case "nvidia":
		return "nvidia-free"
	case "chutes":
		return "chutes"
	case "google-ai-studio":
		return "google-ai-studio"
	case "xai", "grok":
		return "xai"
	case "cursor":
		return "cursor"
	case "anthropic", "claudeagent":
		return "claude"
	case "openai", "codex":
		return "codex"
	case "antigravity":
		if strings.Contains(strings.ToLower(entry.ID), "flash") {
			return "antigravity-flash"
		}
		if strings.Contains(strings.ToLower(entry.ID), "gemini") {
			return "antigravity-gemini"
		}
		return "antigravity-3p"
	default:
		return "openrouter-payg"
	}
}

func inferHarnessFromEntry(entry CatalogModelYAML) string {
	switch strings.ToLower(strings.TrimSpace(entry.Provider)) {
	case "cursor", "grok", "claudeagent", "codex", "antigravity":
		return entry.Provider
	default:
		return "pi"
	}
}
