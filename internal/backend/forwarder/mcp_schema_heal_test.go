package forwarder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimecore "cursor/internal/backend/agent/core"
)

func TestLooksLikeMCPParameterError(t *testing.T) {
	positives := []string{
		"Invalid parameters: connection_id is required",
		"missing required field 'sql'",
		"unknown parameter: reason",
		"unexpected argument \"foo\"",
		"request validation error: schema mismatch",
	}
	for _, text := range positives {
		if !looksLikeMCPParameterError(text) {
			t.Fatalf("expected %q to be detected as parameter validation error", text)
		}
	}
	negatives := []string{
		"",
		"Query failed: You have an error in your SQL syntax",
		"Query failed: Unknown column 'phone_number' in 'where clause'",
		"connection refused",
	}
	for _, text := range negatives {
		if looksLikeMCPParameterError(text) {
			t.Fatalf("expected %q NOT to be detected as parameter validation error", text)
		}
	}
}

func TestEnrichMCPParameterErrorInjectsSchema(t *testing.T) {
	tempDir := t.TempDir()
	defPath := filepath.Join(tempDir, "query.json")
	toolJSON := `{
		"name": "query",
		"description": "Execute a read SQL query",
		"arguments": {
			"type": "object",
			"properties": {
				"connection_id": {"type": "string", "description": "Connection identifier (required)"},
				"sql": {"type": "string", "description": "SQL query to execute"}
			},
			"required": ["connection_id"]
		}
	}`
	if err := os.WriteFile(defPath, []byte(toolJSON), 0644); err != nil {
		t.Fatalf("write tool json: %v", err)
	}

	stream := &ActiveStream{}
	stream.MCPToolDefinitionPaths = map[string]string{
		"query": defPath,
		forwarderCanonicalMCPToolLookupName("user-gridex", "query"): defPath,
	}
	pending := runtimecore.PendingExec{ArgsJSON: []byte(`{"server":"user-gridex","toolName":"query","arguments":{"reason":"check"}}`)}

	service := &Service{}
	got := service.enrichMCPParameterError(stream, pending, "Invalid parameters: connection_id is required")
	if !strings.Contains(got, "Invalid parameters: connection_id is required") {
		t.Fatal("original error text must be preserved")
	}
	if !strings.Contains(got, `"required":["connection_id"]`) || !strings.Contains(got, "Connection identifier (required)") {
		t.Fatalf("expected full schema with per-parameter descriptions to be injected, got %q", got)
	}
	if !strings.Contains(got, "Correct the arguments against this schema and retry.") {
		t.Fatalf("expected retry guidance, got %q", got)
	}
}

func TestEnrichMCPParameterErrorKeepsNonValidationError(t *testing.T) {
	stream := &ActiveStream{}
	stream.MCPToolDefinitionPaths = map[string]string{"query": "unused.json"}
	pending := runtimecore.PendingExec{ArgsJSON: []byte(`{"server":"user-gridex","toolName":"query","arguments":{"sql":"SELECT 1"}}`)}

	service := &Service{}
	payload := "Query failed: You have an error in your SQL syntax"
	if got := service.enrichMCPParameterError(stream, pending, payload); got != payload {
		t.Fatalf("non-validation errors must pass through unchanged, got %q", got)
	}
}

func TestEnrichMCPParameterErrorFallsBackToReadHint(t *testing.T) {
	tempDir := t.TempDir()
	defPath := filepath.Join(tempDir, "missing_schema.json")
	if err := os.WriteFile(defPath, []byte(`{"name":"x","description":"no arguments field"}`), 0644); err != nil {
		t.Fatalf("write tool json: %v", err)
	}
	stream := &ActiveStream{}
	stream.MCPToolDefinitionPaths = map[string]string{"x": defPath}
	pending := runtimecore.PendingExec{ArgsJSON: []byte(`{"server":"srv","toolName":"x"}`)}

	service := &Service{}
	got := service.enrichMCPParameterError(stream, pending, "Invalid parameters: value is required")
	if !strings.Contains(got, "Read "+defPath) {
		t.Fatalf("expected read-hint fallback, got %q", got)
	}
}
