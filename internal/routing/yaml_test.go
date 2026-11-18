package routing

import (
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v2"
)

func TestCatalogYAMLRoundTripAndOrIacCompatibility(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	eng := NewEngine(rs, nil)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	data, err := eng.ExportCatalogYAML(now)
	if err != nil {
		t.Fatalf("ExportCatalogYAML: %v", err)
	}

	// Verify or-iac top-level contract keys
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["schema_version"] != 1 || raw["source"] != "onwatch" {
		t.Fatalf("unexpected header in exported YAML: %+v", raw)
	}
	text := string(data)
	for _, want := range []string{"lightweight:", "workhorse:", "frontier:", "excluded_models:", "x-ai/grok-4.7"} {
		if !strings.Contains(text, want) {
			t.Fatalf("exported YAML missing %q", want)
		}
	}

	// Modify and re-import round trip
	var cat CatalogYAML
	if err := yaml.Unmarshal(data, &cat); err != nil {
		t.Fatal(err)
	}
	cat.ExcludedModels = append(cat.ExcludedModels, "example/bad-model")
	cat.Tiers[TierWorkhorse] = append(cat.Tiers[TierWorkhorse], CatalogModelYAML{
		ID:       "nvidia/test-added:free",
		Provider: "nvidia",
		Billing:  "free",
		Enabled:  true,
		Notes:    "added via yaml import",
		Pool:     "nvidia-free",
		Harness:  "pi",
		Priority: 150,
	})
	updatedYAML, err := yaml.Marshal(&cat)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ImportCatalogYAML(updatedYAML); err != nil {
		t.Fatalf("ImportCatalogYAML: %v", err)
	}

	excl, _ := rs.ListExcludedModels()
	foundExcl := false
	for _, e := range excl {
		if e == "example/bad-model" {
			foundExcl = true
		}
	}
	if !foundExcl {
		t.Fatalf("expected example/bad-model in excluded list, got %v", excl)
	}

	models, _ := rs.ListModels()
	foundModel := false
	for _, m := range models {
		if m.ModelID == "nvidia/test-added:free" && m.Priority == 150 {
			foundModel = true
		}
	}
	if !foundModel {
		t.Fatalf("expected imported model nvidia/test-added:free in DB")
	}
}

func TestImportBareOrIacCatalogWithoutPools(t *testing.T) {
	_, rs := openTestRoutingDB(t)
	eng := NewEngine(rs, nil)

	bareYAML := `
schema_version: 1
source: or-iac
tiers:
  lightweight:
    - id: nvidia/nemotron-3.5-lightning:free
      provider: nvidia
      billing: free
      enabled: true
      notes: fast free
  workhorse:
    - id: moonshotai/kimi-k2.6
      provider: chutes
      billing: byok
      enabled: true
      notes: chutes kimi
  frontier:
    - id: openai/gpt-6.1-terra
      provider: openrouter
      billing: payg
      enabled: true
      notes: payg frontier
excluded_models:
  - anthropic/claude-opus-5.5
`
	if err := eng.ImportCatalogYAML([]byte(bareYAML)); err != nil {
		t.Fatalf("ImportCatalogYAML bare: %v", err)
	}
	models, _ := rs.ListModels()
	if len(models) != 3 {
		t.Fatalf("expected 3 models after bare import, got %d", len(models))
	}
}
