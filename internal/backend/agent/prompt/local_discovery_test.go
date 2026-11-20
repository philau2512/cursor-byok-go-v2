package promptengine

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestCompactMCPToolSchemaKeepsRequiredAndTypes(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {
			"connection_id": {"type": "string", "description": "Connection identifier (required)"},
			"format": {"type": "string", "enum": ["standard", "compact"], "description": "Output format"},
			"row_limit": {"type": "integer", "default": 1000, "maximum": 10000, "description": "Maximum rows"}
		},
		"required": ["connection_id"]
	}`)
	compacted := compactMCPToolSchema(raw)
	if compacted == "" {
		t.Fatal("compactMCPToolSchema returned empty for a small schema")
	}
	for _, want := range []string{`"required":["connection_id"]`, `"connection_id"`, `"type":"string"`, `"enum":["standard","compact"]`} {
		if !strings.Contains(compacted, want) {
			t.Fatalf("compact schema %q missing %q", compacted, want)
		}
	}
	for _, forbidden := range []string{"Connection identifier", "Output format", "Maximum rows", "default", "maximum"} {
		if strings.Contains(compacted, forbidden) {
			t.Fatalf("compact schema %q should not contain %q", compacted, forbidden)
		}
	}
}

func TestCompactMCPToolSchemaBudgetFallbackToLazy(t *testing.T) {
	properties := make(map[string]any)
	for i := 0; i < 60; i++ {
		properties[strings.Repeat("long_property_name", 3)+string(rune('a'+i%26))+strconv.Itoa(i)] = map[string]any{
			"type": "string",
			"enum": []string{strings.Repeat("huge_enum_value_", 10) + strconv.Itoa(i), strings.Repeat("second_huge_enum_", 10)},
		}
	}
	doc := map[string]any{"type": "object", "properties": properties, "required": []string{"x"}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := compactMCPToolSchema(json.RawMessage(raw)); got != "" {
		t.Fatalf("oversized schema should fall back to lazy load, got %d bytes", len(got))
	}
}

func TestCompactMCPToolSchemaSlimVariantDropsEnums(t *testing.T) {
	padding := strings.Repeat("p", 700)
	doc := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{"type": "string", "enum": []string{padding, padding}},
		},
		"required": []string{"mode"},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := compactMCPToolSchema(json.RawMessage(raw))
	if got == "" {
		t.Fatal("expected slim variant within budget, got empty")
	}
	if strings.Contains(got, padding) {
		t.Fatal("slim variant should drop enum values")
	}
	if !strings.Contains(got, `"required":["mode"]`) {
		t.Fatalf("slim variant should keep required, got %q", got)
	}
}
