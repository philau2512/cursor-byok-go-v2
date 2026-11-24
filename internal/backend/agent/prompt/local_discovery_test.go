package promptengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cursor/gen/agentv1"
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

func TestLocalDiscoveryCachesSkillsSubagentsAndMCPReadableUntilInvalidated(t *testing.T) {
	InvalidateAgentRulesCache()
	t.Cleanup(InvalidateAgentRulesCache)

	root := t.TempDir()
	skillDir := filepath.Join(root, ".cursor", "skills", "demo-skill")
	agentDir := filepath.Join(root, ".cursor", "agents")
	mcpDir := filepath.Join(root, "mcps", "user-demo", "tools")
	for _, dir := range []string{skillDir, agentDir, mcpDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: demo-skill\ndescription: Initial skill\n---\nBody"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	agentPath := filepath.Join(agentDir, "demo-agent.md")
	if err := os.WriteFile(agentPath, []byte("---\nname: demo-agent\ndescription: Initial agent\n---\nPrompt"), 0o644); err != nil {
		t.Fatalf("write agent: %v", err)
	}
	mcpPath := filepath.Join(mcpDir, "ping.json")
	if err := os.WriteFile(mcpPath, []byte(`{"description":"Ping tool","arguments":{"type":"object"}}`), 0o644); err != nil {
		t.Fatalf("write mcp tool: %v", err)
	}

	rc1 := &agentv1.RequestContext{
		Env: &agentv1.RequestContextEnv{WorkspacePaths: []string{root}},
	}
	mergeLocalSkills(rc1)
	mergeLocalSubagents(rc1)
	if !hasReadableMCPDefinitionFile(mcpPath) {
		t.Fatal("expected mcp definition file to be readable")
	}
	if len(rc1.GetAgentSkills()) == 0 || len(rc1.GetCustomSubagents()) == 0 {
		t.Fatalf("expected discovered skill and subagent, got skills=%d subagents=%d", len(rc1.GetAgentSkills()), len(rc1.GetCustomSubagents()))
	}

	// Remove files on disk without invalidating cache: cached entries should still serve within the turn.
	_ = os.Remove(skillPath)
	_ = os.Remove(agentPath)
	_ = os.Remove(mcpPath)

	rc2 := &agentv1.RequestContext{
		Env: &agentv1.RequestContextEnv{WorkspacePaths: []string{root}},
	}
	mergeLocalSkills(rc2)
	mergeLocalSubagents(rc2)
	if !hasReadableMCPDefinitionFile(mcpPath) {
		t.Fatal("expected cached mcp readability before invalidation")
	}
	foundSkill := false
	for _, s := range rc2.GetAgentSkills() {
		if s.GetFullPath() == skillPath {
			foundSkill = true
			break
		}
	}
	if !foundSkill {
		t.Fatal("expected cached skill before invalidation")
	}

	// After InvalidateAgentRulesCache (new user turn), removed files must no longer appear.
	InvalidateAgentRulesCache()
	if hasReadableMCPDefinitionFile(mcpPath) {
		t.Fatal("expected mcp readability cache to clear after InvalidateAgentRulesCache")
	}
	rc3 := &agentv1.RequestContext{
		Env: &agentv1.RequestContextEnv{WorkspacePaths: []string{root}},
	}
	mergeLocalSkills(rc3)
	for _, s := range rc3.GetAgentSkills() {
		if s.GetFullPath() == skillPath {
			t.Fatal("removed skill should not appear after InvalidateAgentRulesCache")
		}
	}
	for _, a := range rc3.GetCustomSubagents() {
		if a.GetFullPath() == agentPath {
			t.Fatal("removed subagent should not appear after InvalidateAgentRulesCache")
		}
	}
}
