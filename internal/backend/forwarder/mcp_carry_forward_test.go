package forwarder

// Hồi quy cho bug: MCP descriptors chỉ được client gửi ở turn 1, bị
// normalizeRequestContextForStorageMode strip khỏi entry turn >= 2, và vì compaction
// chỉ preserve entry của turn hiện tại nên <mcp_file_system>/<mcp_meta_tools> biến mất
// vĩnh viễn khỏi model-visible history sau auto-compaction.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	promptengine "cursor/internal/backend/agent/prompt"
)

func mcpCarryDescriptor(dir string) []*agentv1.McpDescriptor {
	return mcpCarryDescriptorNamed(dir, "user-gridex", "gridex", "query")
}

func mcpCarryDescriptorNamed(dir, serverID, serverName, toolName string) []*agentv1.McpDescriptor {
	return []*agentv1.McpDescriptor{{
		ServerIdentifier: serverID,
		ServerName:       serverName,
		FolderPath:       stringPtr(filepath.ToSlash(filepath.Join(dir, "mcps", serverID))),
		Tools: []*agentv1.McpToolDescriptor{
			{ToolName: toolName, DefinitionPath: stringPtr(filepath.ToSlash(filepath.Join(dir, "mcps", serverID, "tools", toolName+".json")))},
		},
	}}
}

// mcpCarryWriteRoot dựng cây descriptor MCP trên đĩa để hydrateLocalMCPDescriptors đọc được
// description/schema; dùng chung một root cho mọi turn để static content ổn định (dedup).
func mcpCarryWriteRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, serverID := range []string{"user-gridex", "user-traceui"} {
		defDir := filepath.Join(root, "mcps", serverID, "tools")
		if err := os.MkdirAll(defDir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	gridexJSON := `{"name":"query","description":"Run a read-only SQL query","arguments":[{"name":"sql","type":"string","required":true,"description":"SQL text"}]}`
	if err := os.WriteFile(filepath.Join(root, "mcps", "user-gridex", "tools", "query.json"), []byte(gridexJSON), 0644); err != nil {
		t.Fatal(err)
	}
	traceuiJSON := `{"name":"trace","description":"Analyze a trace file","arguments":[{"name":"path","type":"string","required":true,"description":"Trace path"}]}`
	if err := os.WriteFile(filepath.Join(root, "mcps", "user-traceui", "tools", "trace.json"), []byte(traceuiJSON), 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

func mcpCarryLiveRequestContext(root string, withMCP bool) *agentv1.RequestContext {
	rc := &agentv1.RequestContext{
		Env: &agentv1.RequestContextEnv{
			OsVersion:      "win32 10.0.22621",
			WorkspacePaths: []string{filepath.ToSlash(root)},
		},
	}
	if withMCP {
		rc.McpFileSystemOptions = &agentv1.McpFileSystemOptions{
			Enabled:             true,
			WorkspaceProjectDir: filepath.ToSlash(root),
			McpDescriptors:      mcpCarryDescriptor(root),
		}
	}
	return rc
}

func mcpCarryRequestContextEntry(t *testing.T, turnSeq int64, requestID string, rc *agentv1.RequestContext, historyEntries []HistoryEntry) HistoryEntry {
	t.Helper()
	entries, err := buildRunEntries(InboundIntent{RequestID: requestID, RequestContext: rc}, agentv1.AgentMode_AGENT_MODE_AGENT, turnSeq, historyEntries)
	if err != nil {
		t.Fatalf("buildRunEntries: %v", err)
	}
	for _, entry := range entries {
		if entry.Kind == "request_context" {
			return entry
		}
	}
	t.Fatal("buildRunEntries produced no request_context entry")
	return HistoryEntry{}
}

func mcpCarryCompiledSections(t *testing.T, conversation *ConversationFile) (int, int) {
	t.Helper()
	projector := NewHistoryProjector()
	compiler := NewPromptCompiler(projector, NewToolCatalog(), nil, nil)
	compiled, err := compiler.Compile(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, "chay dry run voi gridex mcp", "test-model")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	sections := 0
	for _, message := range compiled.Messages {
		if strings.Contains(message.Content, "<mcp_file_system>") || strings.Contains(message.Content, "<mcp_meta_tools>") {
			sections++
		}
	}
	return sections, len(compiled.Messages)
}

// Entry realtime (turn >= 2) phải giữ nguyên MCP options qua round-trip normalize + protojson.
func TestNormalizeRealtimeRequestContextKeepsMCPOptions(t *testing.T) {
	rc := mcpCarryLiveRequestContext(mcpCarryWriteRoot(t), true)
	rc.Rules = nil

	normalized := normalizeRealtimeRequestContextForStorage(rc)
	if normalized == nil || normalized.GetMcpFileSystemOptions() == nil {
		t.Fatalf("realtime normalization dropped MCP options: %#v", normalized)
	}
	descriptors := normalized.GetMcpFileSystemOptions().GetMcpDescriptors()
	if len(descriptors) != 1 || descriptors[0].GetServerIdentifier() != "user-gridex" {
		t.Fatalf("unexpected descriptors after realtime normalization: %#v", descriptors)
	}

	payload, err := protojson.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded := &agentv1.RequestContext{}
	if err := protojson.Unmarshal(payload, decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.GetMcpFileSystemOptions() == nil {
		t.Fatal("MCP options lost through protojson round-trip")
	}
}

// Turn sau không nhận MCP từ client vẫn phải persist snapshot MCP vào entry.
func TestBuildRunEntriesCarriesForwardMCPOptions(t *testing.T) {
	root := mcpCarryWriteRoot(t)

	turn1RC := mcpCarryLiveRequestContext(root, true)
	turn1Entry := mcpCarryRequestContextEntry(t, 1, "request-1", turn1RC, nil)

	turn2RC := mcpCarryLiveRequestContext(root, false)
	turn2Entry := mcpCarryRequestContextEntry(t, 2, "request-2", turn2RC, []HistoryEntry{turn1Entry})

	decoded := &agentv1.RequestContext{}
	if err := protojson.Unmarshal(turn2Entry.Payload, decoded); err != nil {
		t.Fatalf("unmarshal turn-2 entry: %v", err)
	}
	if decoded.GetMcpFileSystemOptions() == nil {
		t.Fatal("turn-2 entry lost MCP options despite carry-forward")
	}
	msgs := promptengine.BuildRequestContextReplayMessages(decoded)
	if len(msgs) == 0 || !strings.Contains(msgs[0].Content, "<mcp_file_system>") {
		t.Fatalf("carried turn-2 entry does not render MCP section: %#v", msgs)
	}
}

// Client báo MCP options rỗng một cách tường minh (đã tắt hết server) thì không được bù snapshot cũ.
func TestBuildRunEntriesRespectsExplicitEmptyMCPClear(t *testing.T) {
	root := mcpCarryWriteRoot(t)

	turn1RC := mcpCarryLiveRequestContext(root, true)
	turn1Entry := mcpCarryRequestContextEntry(t, 1, "request-1", turn1RC, nil)

	clearedRC := mcpCarryLiveRequestContext(root, false)
	clearedRC.McpFileSystemOptions = &agentv1.McpFileSystemOptions{}
	clearedEntry := mcpCarryRequestContextEntry(t, 2, "request-2", clearedRC, []HistoryEntry{turn1Entry})

	decoded := &agentv1.RequestContext{}
	if err := protojson.Unmarshal(clearedEntry.Payload, decoded); err != nil {
		t.Fatalf("unmarshal cleared turn-2 entry: %v", err)
	}
	if decoded.GetMcpFileSystemOptions() != nil && len(decoded.GetMcpFileSystemOptions().GetMcpDescriptors()) > 0 {
		t.Fatal("explicit empty MCP options were overwritten by carried snapshot")
	}
}

// Kịch bản gốc: multi-turn thường dedup đúng 1 section; sau compaction section vẫn còn (1 lần).
func TestMCPSectionSurvivesCompactionViaCarryForward(t *testing.T) {
	root := mcpCarryWriteRoot(t)
	conversation := &ConversationFile{
		ConversationID:     "conv-mcp-carry",
		RootConversationID: "conv-mcp-carry",
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
	}

	turn1RC := mcpCarryLiveRequestContext(root, true)
	appendEntriesInPlace(conversation, []HistoryEntry{
		mcpCarryRequestContextEntry(t, 1, "request-1", turn1RC, conversation.Entries),
		compactionTestUserEntry(t, 1, "request-1", "first question", "message-1"),
		newAssistantTextEntry(1, "request-1", "first answer", "", ""),
	})

	historyEntries := append([]HistoryEntry(nil), conversation.Entries...)
	for turn := int64(2); turn <= 4; turn++ {
		requestID := "request-" + string(rune('0'+turn))
		liveRC := mcpCarryLiveRequestContext(root, false)
		appendEntriesInPlace(conversation, []HistoryEntry{
			mcpCarryRequestContextEntry(t, turn, requestID, liveRC, historyEntries),
			compactionTestUserEntry(t, turn, requestID, "question", "message-"+string(rune('0'+turn))),
			newAssistantTextEntry(turn, requestID, "answer", "", ""),
		})
		historyEntries = append([]HistoryEntry(nil), conversation.Entries...)
	}

	before, beforeMessages := mcpCarryCompiledSections(t, conversation)
	if before != 1 {
		t.Fatalf("expected exactly 1 MCP section before compaction, got %d (messages=%d)", before, beforeMessages)
	}

	if err := applyCompactionToConversation(conversation, &PendingCompaction{
		Trigger:                   "auto",
		CurrentTurnSeq:            4,
		CurrentRequestID:          "request-4",
		PreserveCurrentTurnInputs: true,
	}, "earlier context summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}

	after, afterMessages := mcpCarryCompiledSections(t, conversation)
	if after != 1 {
		t.Fatalf("MCP section lost after compaction: sections=%d messages=%d", after, afterMessages)
	}

	// Turn mới sau compaction tiếp tục carry-forward và không nhân đôi section.
	liveRC := mcpCarryLiveRequestContext(root, false)
	appendEntriesInPlace(conversation, []HistoryEntry{
		mcpCarryRequestContextEntry(t, 5, "request-5", liveRC, conversation.Entries),
		compactionTestUserEntry(t, 5, "request-5", "next question", "message-5"),
	})
	later, laterMessages := mcpCarryCompiledSections(t, conversation)
	if later != 1 {
		t.Fatalf("MCP section count after turn 5 = %d, want 1 (messages=%d)", later, laterMessages)
	}
}

// mcpCarryCompiledMCPText nối toàn nội dung message có chứa section MCP.
func mcpCarryCompiledMCPText(t *testing.T, conversation *ConversationFile) string {
	t.Helper()
	projector := NewHistoryProjector()
	compiler := NewPromptCompiler(projector, NewToolCatalog(), nil, nil)
	compiled, err := compiler.Compile(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, "khi bat tat mcp thi no co nap dung ko", "test-model")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	var builder strings.Builder
	for _, message := range compiled.Messages {
		if strings.Contains(message.Content, "<mcp_file_system>") || strings.Contains(message.Content, "<mcp_meta_tools>") {
			builder.WriteString(message.Content)
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func mcpCarryOptionsRequestContext(root string, descriptors []*agentv1.McpDescriptor) *agentv1.RequestContext {
	rc := mcpCarryLiveRequestContext(root, false)
	rc.McpFileSystemOptions = &agentv1.McpFileSystemOptions{
		Enabled:             true,
		WorkspaceProjectDir: filepath.ToSlash(root),
		McpDescriptors:      descriptors,
	}
	return rc
}

// Tắt server giữa phiên: options sống từ client luôn thắng, snapshot cũ không được "hồi sinh" server đã tắt.
// Sau compaction, section còn lại phải là danh sách MỚI (không còn server đã tắt).
func TestMCPOptionsFromClientWinAfterServerToggledOff(t *testing.T) {
	root := mcpCarryWriteRoot(t)

	turn1RC := mcpCarryOptionsRequestContext(root, mcpCarryDescriptor(root))
	turn1Entry := mcpCarryRequestContextEntry(t, 1, "request-1", turn1RC, nil)

	// Client đã tắt gridex, chỉ còn traceui.
	toggledRC := mcpCarryOptionsRequestContext(root, mcpCarryDescriptorNamed(root, "user-traceui", "traceui", "trace"))
	toggledEntry := mcpCarryRequestContextEntry(t, 2, "request-2", toggledRC, []HistoryEntry{turn1Entry})

	decoded := &agentv1.RequestContext{}
	if err := protojson.Unmarshal(toggledEntry.Payload, decoded); err != nil {
		t.Fatalf("unmarshal toggled entry: %v", err)
	}
	descriptors := decoded.GetMcpFileSystemOptions().GetMcpDescriptors()
	if len(descriptors) != 1 || descriptors[0].GetServerIdentifier() != "user-traceui" {
		t.Fatalf("client-sent options were overwritten by carried snapshot: %#v", descriptors)
	}
	if servers := collectMCPToolServers(decoded); servers["query"] != "" {
		t.Fatalf("disabled gridex tool still routed: %q", servers["query"])
	}

	conversation := &ConversationFile{
		ConversationID:     "conv-mcp-toggle",
		RootConversationID: "conv-mcp-toggle",
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		turn1Entry,
		compactionTestUserEntry(t, 1, "request-1", "first question", "message-1"),
		newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		toggledEntry,
		compactionTestUserEntry(t, 2, "request-2", "second question", "message-2"),
	})

	// Append-only: section cũ vẫn còn trong history cho tới khi bị compaction cắt đi.
	if sections, _ := mcpCarryCompiledSections(t, conversation); sections != 2 {
		t.Fatalf("expected 2 MCP sections before compaction (old + refreshed), got %d", sections)
	}
	if err := applyCompactionToConversation(conversation, &PendingCompaction{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
	}, "earlier context summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}
	text := mcpCarryCompiledMCPText(t, conversation)
	if !strings.Contains(text, "user-traceui") {
		t.Fatal("newly enabled server missing from MCP section after compaction")
	}
	if strings.Contains(text, "user-gridex") {
		t.Fatal("disabled server resurrected in MCP section after compaction")
	}
}

// Carry-forward chỉ bù khi client không gửi options; nó không được mutate request context sống
// vì routing registry (MCPToolServers) phải phản ánh đúng những gì client báo turn này.
func TestCarryForwardDoesNotMutateLiveRequestContext(t *testing.T) {
	root := mcpCarryWriteRoot(t)

	turn1Entry := mcpCarryRequestContextEntry(t, 1, "request-1", mcpCarryOptionsRequestContext(root, mcpCarryDescriptor(root)), nil)

	liveRC := mcpCarryLiveRequestContext(root, false)
	carried := carryForwardMCPOptions(liveRC, []HistoryEntry{turn1Entry})
	if carried.GetMcpFileSystemOptions() == nil {
		t.Fatal("carry-forward did not fill MCP options")
	}
	if liveRC.GetMcpFileSystemOptions() != nil {
		t.Fatal("carry-forward mutated the live request context")
	}
}

// Prewarm cũng chiếm một turnSeq nhưng không gửi request_context, nên user message đầu tiên
// rơi vào turn 2. Trước đây điều kiện "turnSeq == 1" khiến static context (kèm MCP descriptors)
// bị strip ngay từ turn thật đầu tiên và conversation không bao giờ có MCP section.
func TestStaticRequestContextPersistedWhenPrewarmConsumedFirstTurn(t *testing.T) {
	root := mcpCarryWriteRoot(t)
	conversation := &ConversationFile{
		ConversationID:     "conv-prewarm-first",
		RootConversationID: "conv-prewarm-first",
		Mode:               "agent",
		NextTurnSeq:        2,
		NextEntrySeq:       2,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		newMetadataEntry(1, "prewarm-1", "run_request", map[string]any{"prewarm": true}),
	})

	liveRC := mcpCarryLiveRequestContext(root, true)
	entry := mcpCarryRequestContextEntry(t, 2, "request-2", liveRC, conversation.Entries)

	decoded := &agentv1.RequestContext{}
	if err := protojson.Unmarshal(entry.Payload, decoded); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	if decoded.GetMcpFileSystemOptions() == nil {
		t.Fatal("static context stripped because prewarm already consumed turn 1")
	}
	msgs := promptengine.BuildRequestContextReplayMessages(decoded)
	if len(msgs) == 0 || !strings.Contains(msgs[0].Content, "<mcp_file_system>") {
		t.Fatalf("MCP section missing after prewarm-stolen turn 1: %#v", msgs)
	}
}
