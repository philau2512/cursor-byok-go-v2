package forwarder

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"cursor/gen/agentv1"
	"cursor/gen/byoklocalv1"
	"cursor/internal/memory"
	promptassets "cursor/prompt"
)

// memoryToggleConfig giả lập config bật/tắt project memory.
type memoryToggleConfig struct {
	enabled bool
}

func (config memoryToggleConfig) MemoryEnabled() bool { return config.enabled }
func (config memoryToggleConfig) MemoryAutoCapture() bool {
	return false
}
func (config memoryToggleConfig) MemoryOverviewMaxBytes() int { return 4096 }

// newLocalSubscriptionTestService dựng service trỏ store/memory về temp dir
// để test không ghi vào memory store thật của máy dev.
func newLocalSubscriptionTestService(t *testing.T, memoryEnabled bool) *Service {
	t.Helper()
	historyRoot, err := os.MkdirTemp("", "forwarder-local-subscription-")
	if err != nil {
		t.Fatalf("create history root failed: %v", err)
	}
	service := NewService(historyRoot, failingChannelResolver{})
	service.memoryStore = memory.NewStore(t.TempDir())
	service.memoryConfig = memoryToggleConfig{enabled: memoryEnabled}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = service.Close(ctx)
		_ = os.RemoveAll(historyRoot)
	})
	return service
}

func TestSubscriptionToolCatalogComesFromToolsJSON(t *testing.T) {
	catalog := subscriptionToolCatalog(promptassets.ModeAgent)
	for _, toolName := range []string{"SaveMemory", "SearchMemory"} {
		definition, ok := catalog[toolName]
		if !ok {
			t.Fatalf("tool %s missing from tools.json catalog", toolName)
		}
		if definition.GetToolName() != toolName {
			t.Fatalf("expected tool name %s, got %q", toolName, definition.GetToolName())
		}
		if len(definition.GetDescription()) < 40 {
			t.Fatalf("tool %s has suspiciously short description: %q", toolName, definition.GetDescription())
		}
		var schema map[string]any
		if err := json.Unmarshal([]byte(definition.GetInputSchemaJson()), &schema); err != nil {
			t.Fatalf("tool %s input_schema_json is not valid JSON: %v", toolName, err)
		}
		if _, ok := schema["properties"]; !ok {
			t.Fatalf("tool %s schema has no properties: %#v", toolName, schema)
		}
	}
}

func TestListLocalSubscriptionToolsHonoursMemoryFlag(t *testing.T) {
	t.Run("enabled exposes memory tools", func(t *testing.T) {
		service := newLocalSubscriptionTestService(t, true)
		response, err := service.ListLocalSubscriptionTools(context.Background(),
			connect.NewRequest(&byoklocalv1.ListLocalSubscriptionToolsRequest{}))
		if err != nil {
			t.Fatalf("ListLocalSubscriptionTools failed: %v", err)
		}
		names := make(map[string]bool)
		for _, tool := range response.Msg.GetTools() {
			names[tool.GetToolName()] = true
		}
		if !names["SaveMemory"] || !names["SearchMemory"] {
			t.Fatalf("expected both memory tools, got %v", names)
		}
	})

	t.Run("disabled publishes nothing", func(t *testing.T) {
		service := newLocalSubscriptionTestService(t, false)
		response, err := service.ListLocalSubscriptionTools(context.Background(),
			connect.NewRequest(&byoklocalv1.ListLocalSubscriptionToolsRequest{}))
		if err != nil {
			t.Fatalf("ListLocalSubscriptionTools failed: %v", err)
		}
		if len(response.Msg.GetTools()) != 0 {
			t.Fatalf("expected empty tool list when memory disabled, got %d", len(response.Msg.GetTools()))
		}
	})
}

func TestCallLocalSubscriptionToolRejectsUnknownTool(t *testing.T) {
	service := newLocalSubscriptionTestService(t, true)
	_, err := service.CallLocalSubscriptionTool(context.Background(), connect.NewRequest(
		&byoklocalv1.CallLocalSubscriptionToolRequest{
			ConversationId: "conv-unknown",
			ToolName:       "NotATool",
			ToolArgsJson:   "{}",
		}))
	if err == nil {
		t.Fatal("expected unknown tool to fail")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

func TestCallLocalSubscriptionToolDisabledIsBusinessError(t *testing.T) {
	service := newLocalSubscriptionTestService(t, false)
	response, err := service.CallLocalSubscriptionTool(context.Background(), connect.NewRequest(
		&byoklocalv1.CallLocalSubscriptionToolRequest{
			ConversationId: "conv-disabled",
			ToolName:       "SaveMemory",
			ToolArgsJson:   `{"title":"x","category":"project_introduction","content":"y","origin":"model"}`,
		}))
	if err != nil {
		t.Fatalf("disabled tool must answer as business error, got protocol error: %v", err)
	}
	if !response.Msg.GetIsError() {
		t.Fatal("expected is_error=true for disabled memory")
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(response.Msg.GetResultJson()), &payload); err != nil {
		t.Fatalf("result_json must be a JSON object: %v (%s)", err, response.Msg.GetResultJson())
	}
	if payload["text"] != "Project memory is disabled." {
		t.Fatalf("unexpected disabled message: %q", payload["text"])
	}
}

// TestCallLocalSubscriptionToolSaveThenSearch chạy trọn vòng save→search qua
// kênh subscription tool, chứng minh registry dùng chung logic với đường
// tool-call nội bộ (không nhân bản implementation).
func TestCallLocalSubscriptionToolSaveThenSearch(t *testing.T) {
	service := newLocalSubscriptionTestService(t, true)

	stream, err := service.broker.OpenStream("req-local-subscription", "conv-local-subscription", 0,
		"default", "Test Model", agentv1.AgentMode_AGENT_MODE_AGENT, "seed")
	if err != nil || stream == nil {
		t.Fatalf("open stream failed: %v", err)
	}
	workspace := t.TempDir()
	stream.mu.Lock()
	stream.WorkspacePaths = []string{workspace}
	stream.mu.Unlock()

	ctx := context.Background()
	args, err := json.Marshal(map[string]any{
		"title":          "Local subscription tool spike note",
		"category":       "important_decision_experience",
		"content":        "Result contract: {\"text\": ...} plus is_error flag.",
		"keywords":       []string{"subscription", "tools"},
		"usage_scenario": []string{"When extending the local subscription tool registry, keep one source of truth"},
		"origin":         "model",
	})
	if err != nil {
		t.Fatalf("marshal save args failed: %v", err)
	}

	saved, err := service.CallLocalSubscriptionTool(ctx, connect.NewRequest(
		&byoklocalv1.CallLocalSubscriptionToolRequest{
			ConversationId: "conv-local-subscription",
			ToolName:       "SaveMemory",
			ToolArgsJson:   string(args),
		}))
	if err != nil {
		t.Fatalf("SaveMemory via subscription channel failed: %v", err)
	}
	if saved.Msg.GetIsError() {
		t.Fatalf("expected successful save, got error result: %s", saved.Msg.GetResultJson())
	}
	var savedPayload map[string]string
	if err := json.Unmarshal([]byte(saved.Msg.GetResultJson()), &savedPayload); err != nil {
		t.Fatalf("save result_json invalid: %v", err)
	}
	if savedPayload["text"] == "" {
		t.Fatal("expected non-empty save result text")
	}

	found, err := service.CallLocalSubscriptionTool(ctx, connect.NewRequest(
		&byoklocalv1.CallLocalSubscriptionToolRequest{
			ConversationId: "conv-local-subscription",
			ToolName:       "SearchMemory",
			ToolArgsJson:   `{"query":"subscription tool registry"}`,
		}))
	if err != nil {
		t.Fatalf("SearchMemory via subscription channel failed: %v", err)
	}
	if found.Msg.GetIsError() {
		t.Fatalf("expected successful search, got error result: %s", found.Msg.GetResultJson())
	}
	var foundPayload map[string]string
	if err := json.Unmarshal([]byte(found.Msg.GetResultJson()), &foundPayload); err != nil {
		t.Fatalf("search result_json invalid: %v", err)
	}
	if !strings.Contains(foundPayload["text"], "Local subscription tool spike note") {
		t.Fatalf("expected saved memory in search result, got: %q", foundPayload["text"])
	}
}
