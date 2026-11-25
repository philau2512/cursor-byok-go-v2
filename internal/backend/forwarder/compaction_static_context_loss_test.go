package forwarder

// Hồi quy cho bug "model ngáo tool sau auto-compact":
// static context (skill descriptors, custom subagents, repository_info, MCP descriptors) chỉ
// client gửi ở turn đầu. Trước đây entry realtime của turn >= 2 bị strip các field đó, mà
// auto-compaction lại chỉ preserve entry của turn hiện tại, nên <agent_skills>/<subagents> biến
// mất và <user_info> bị méo sau khi compact; xấu hơn, nếu turn bị compact không có entry
// request_context nào khớp TurnSeq+RequestID (prewarm/supersede chiếm turnSeq, hoặc intent đến mà
// client không gửi request context) thì model mất luôn cả <mcp_file_system> lẫn <rules>.
//
// Hợp đồng sau khi sửa:
//   - normalizeRealtimeRequestContextForStorage giữ lại skill descriptors / custom subagents /
//     repository_info, với công thức render trùng khít bản static để projector dedup về MỘT block
//     static duy nhất (giữ prefix cache).
//   - carryForwardStaticContext bù mọi field static còn trống từ history.
//   - autoCompactionPreservedEntryIndexes fallback về entry request_context gần nhất.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	promptengine "cursor/internal/backend/agent/prompt"
)

const (
	reproSkillMarker    = "REPRO-CLIENT-SKILL"
	reproSubagentMarker = "REPRO-CLIENT-SUBAGENT"
	reproRuleMarker     = "REPRO-CLIENT-RULE"
	reproMCPServerID    = "user-gridex"
	// reproMCPSectionSignature là dòng heading chỉ có trong <mcp_file_system> do engine render,
	// mỗi block đúng một lần (server id thì lặp lại trong embedded descriptor).
	reproMCPSectionSignature = "## MCP Tool Access (Deferred Schemas)"
	reproGitRepoYes          = "Is directory a git repo: Yes"
	reproGitRepoUnknown      = "Is directory a git repo: Unknown"
)

// reproStaticRequestContext mô phỏng payload turn 1: client gửi đầy đủ static context
// (skills, subagents, rules, repository_info, MCP descriptors).
func reproStaticRequestContext(root string) *agentv1.RequestContext {
	rc := mcpCarryLiveRequestContext(root, true)
	rc.AgentSkills = []*agentv1.AgentSkill{{
		FullPath:    filepath.ToSlash(filepath.Join(root, ".cursor", "skills", "repro-client-skill", "SKILL.md")),
		Description: reproSkillMarker + ": skill chi client gui len o turn dau",
	}}
	rc.CustomSubagents = []*agentv1.CustomSubagent{{
		Name:        "repro-client-subagent",
		Description: reproSubagentMarker + ": subagent chi client gui len o turn dau",
		Prompt:      strings.Repeat("REPRO-SUBAGENT-PROMPT-SYSTEM-", 40),
		Tools:       []string{"Read", "Grep", "Shell"},
	}}
	rc.Rules = reproClientRules(root)
	rc.RepositoryInfo = []*agentv1.RepositoryIndexingInfo{{IsLocal: true}}
	return rc
}

// reproRealtimeRequestContext mô phỏng payload turn >= 2: client chỉ gửi env + rules,
// không gửi lại skills/subagents/repository_info/MCP options.
func reproRealtimeRequestContext(root string) *agentv1.RequestContext {
	rc := mcpCarryLiveRequestContext(root, false)
	rc.Rules = reproClientRules(root)
	return rc
}

func reproClientRules(root string) []*agentv1.CursorRule {
	return []*agentv1.CursorRule{{
		FullPath: filepath.ToSlash(filepath.Join(root, ".cursor", "rules", "repro.mdc")),
		Content:  reproRuleMarker + ": quy tac client gui lai moi turn",
	}}
}

// reproConversation dựng hội thoại nhiều turn: turn 1 mang static payload,
// turn 2..turns mang realtime payload (client không gửi lại static context).
func reproConversation(t *testing.T, root string, turns int) *ConversationFile {
	t.Helper()
	conversation := &ConversationFile{
		ConversationID:     "conv-repro-static-loss",
		RootConversationID: "conv-repro-static-loss",
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		mcpCarryRequestContextEntry(t, 1, "request-1", reproStaticRequestContext(root), conversation.Entries),
		compactionTestUserEntry(t, 1, "request-1", "cau hoi dau tien", "message-1"),
		newAssistantTextEntry(1, "request-1", "tra loi dau tien", "", ""),
	})
	for turn := int64(2); turn <= int64(turns); turn++ {
		requestID := fmt.Sprintf("request-%d", turn)
		historyEntries := append([]HistoryEntry(nil), conversation.Entries...)
		appendEntriesInPlace(conversation, []HistoryEntry{
			mcpCarryRequestContextEntry(t, turn, requestID, reproRealtimeRequestContext(root), historyEntries),
			compactionTestUserEntry(t, turn, requestID, fmt.Sprintf("cau hoi %d", turn), fmt.Sprintf("message-%d", turn)),
			newAssistantTextEntry(turn, requestID, fmt.Sprintf("tra loi %d", turn), "", ""),
		})
	}
	return conversation
}

// reproCompileMessages trả về từng message model-visible theo thứ tự gửi lên provider.
func reproCompileMessages(t *testing.T, conversation *ConversationFile) []reproMessage {
	t.Helper()
	projector := NewHistoryProjector()
	compiler := NewPromptCompiler(projector, NewToolCatalog(), nil, nil)
	compiled, err := compiler.Compile(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, "dung gridex mcp chay dry run", "test-model")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	messages := make([]reproMessage, 0, len(compiled.Messages))
	for _, message := range compiled.Messages {
		messages = append(messages, reproMessage{Role: message.Role, Content: message.Content})
	}
	return messages
}

type reproMessage struct {
	Role    string
	Content string
}

// reproCompileText trả về toàn bộ text model-visible mà forwarder gửi lên provider.
func reproCompileText(t *testing.T, conversation *ConversationFile) string {
	t.Helper()
	var builder strings.Builder
	for _, message := range reproCompileMessages(t, conversation) {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

// reproPresence là bảng section static có/không xuất hiện trong payload.
type reproPresence map[string]bool

func reproMeasure(text string) reproPresence {
	return reproPresence{
		"agent_skills": strings.Contains(text, reproSkillMarker),
		"subagents":    strings.Contains(text, reproSubagentMarker),
		"rules":        strings.Contains(text, reproRuleMarker),
		"mcp":          strings.Contains(text, reproMCPServerID),
		"git_repo_yes": strings.Contains(text, reproGitRepoYes),
		"git_repo_unk": strings.Contains(text, reproGitRepoUnknown),
	}
}

// reproStaticBlockCounts đo số lần mỗi marker xuất hiện; phải bằng 1 để projector dedup
// mọi block static về một bản, giữ nguyên prefix đã cache.
// Marker chọn là chuỗi chỉ xuất hiện đúng một lần trong một block render.
func reproStaticBlockCounts(text string) map[string]int {
	return map[string]int{
		"agent_skills": strings.Count(text, reproSkillMarker),
		"subagents":    strings.Count(text, reproSubagentMarker),
		"rules":        strings.Count(text, reproRuleMarker),
		"mcp":          strings.Count(text, reproMCPSectionSignature),
	}
}

func reproPresenceString(presence reproPresence) string {
	keys := make([]string, 0, len(presence))
	for key := range presence {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%t", key, presence[key]))
	}
	return strings.Join(parts, " ")
}

func reproCountsString(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, " ")
}

// reproAssertFull yêu cầu mọi section static đều còn trong payload.
func reproAssertFull(t *testing.T, label string, presence reproPresence) {
	t.Helper()
	missing := make([]string, 0, 5)
	for _, key := range []string{"agent_skills", "subagents", "rules", "mcp", "git_repo_yes"} {
		if !presence[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%s: thieu section static %s\nmatrix: %s", label, strings.Join(missing, ", "), reproPresenceString(presence))
	}
	if presence["git_repo_unk"] {
		t.Fatalf("%s: <user_info> bi méo thanh 'Is directory a git repo: Unknown'\nmatrix: %s", label, reproPresenceString(presence))
	}
}

func reproCompact(t *testing.T, conversation *ConversationFile, turnSeq int64, requestID string) {
	t.Helper()
	if err := applyCompactionToConversation(conversation, &PendingCompaction{
		Trigger:                   "auto",
		CurrentTurnSeq:            turnSeq,
		CurrentRequestID:          requestID,
		PreserveCurrentTurnInputs: true,
	}, "tom tat nguyen van cu"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}
}

// mcpCarryDecode unmarshal payload của một request_context entry phục vụ assertion.
func mcpCarryDecode(t *testing.T, entry HistoryEntry) *agentv1.RequestContext {
	t.Helper()
	decoded := &agentv1.RequestContext{}
	if err := protojson.Unmarshal(entry.Payload, decoded); err != nil {
		t.Fatalf("unmarshal request_context entry: %v", err)
	}
	return decoded
}

// Entry realtime của turn >= 2 phải giữ lại skill descriptors / custom subagents /
// repository_info (qua carry-forward) để compaction còn nguồn render trong turn hiện tại.
func TestRealtimeRequestContextKeepsStaticPayload(t *testing.T) {
	root := mcpCarryWriteRoot(t)

	if !shouldPersistStaticRequestContext(nil) {
		t.Fatal("history rong phai cho phep ghi static payload")
	}
	turn1Entry := mcpCarryRequestContextEntry(t, 1, "request-1", reproStaticRequestContext(root), nil)
	turn1RC := mcpCarryDecode(t, turn1Entry)
	if !requestContextCarriesStaticPayload(turn1RC) {
		t.Fatal("turn-1 entry phai mang static payload")
	}
	if len(turn1RC.GetSkillOptions().GetSkillDescriptors()) == 0 || len(turn1RC.GetCustomSubagents()) == 0 || len(turn1RC.GetRepositoryInfo()) == 0 {
		t.Fatalf("turn-1 entry thieu static section: %#v", turn1RC)
	}
	if shouldPersistStaticRequestContext([]HistoryEntry{turn1Entry}) {
		t.Fatal("turn 2 van duoc phep ghi full static payload (phai chan lai de tranh trung block)")
	}

	historyEntries := []HistoryEntry{turn1Entry}
	for turn := int64(2); turn <= 4; turn++ {
		requestID := fmt.Sprintf("request-%d", turn)
		entry := mcpCarryRequestContextEntry(t, turn, requestID, reproRealtimeRequestContext(root), historyEntries)
		decoded := mcpCarryDecode(t, entry)
		if len(decoded.GetSkillOptions().GetSkillDescriptors()) == 0 {
			t.Fatalf("turn %d entry mat skill descriptors: %#v", turn, decoded)
		}
		if len(decoded.GetCustomSubagents()) == 0 {
			t.Fatalf("turn %d entry mat custom subagents: %#v", turn, decoded)
		}
		if len(decoded.GetRepositoryInfo()) == 0 {
			t.Fatalf("turn %d entry mat repository_info: %#v", turn, decoded)
		}
		if decoded.GetMcpFileSystemOptions() == nil {
			t.Fatalf("turn %d entry mat MCP options: %#v", turn, decoded)
		}
		// Entry realtime khong duoc nhan ban system prompt cua subagent vao history moi turn.
		for _, agent := range decoded.GetCustomSubagents() {
			if agent.GetPrompt() != "" || len(agent.GetTools()) != 0 {
				t.Fatalf("turn %d entry van luu subagent prompt/tools => phinh history: name=%q promptLen=%d tools=%d",
					turn, agent.GetName(), len(agent.GetPrompt()), len(agent.GetTools()))
			}
		}
		// Bản realtime phải render y hệt bản static để projector dedup gộp về một block.
		if got, want := reproMeasure(reproRenderStaticSections(t, decoded)), reproMeasure(reproRenderStaticSections(t, turn1RC)); got["agent_skills"] != want["agent_skills"] || got["subagents"] != want["subagents"] || got["rules"] != want["rules"] || got["mcp"] != want["mcp"] {
			t.Fatalf("turn %d render khong trung turn 1 => dedup that bai, block static bi nhan doi: %s", turn, reproPresenceString(got))
		}
		historyEntries = append(historyEntries, entry)
	}
}

// reproRenderStaticSections dựng text static section của một request context qua prompt engine.
func reproRenderStaticSections(t *testing.T, requestContext *agentv1.RequestContext) string {
	t.Helper()
	messages := promptengine.BuildRequestContextReplayMessages(requestContext)
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

// Carry-forward phải bù đủ mọi field static, không chỉ MCP.
func TestCarryForwardStaticContextRestoresAllStaticFields(t *testing.T) {
	root := mcpCarryWriteRoot(t)
	turn1Entry := mcpCarryRequestContextEntry(t, 1, "request-1", reproStaticRequestContext(root), nil)

	liveRC := reproRealtimeRequestContext(root)
	carried := carryForwardStaticContext(liveRC, []HistoryEntry{turn1Entry})

	if carried.GetMcpFileSystemOptions() == nil {
		t.Fatal("MCP options phai duoc bu")
	}
	if len(carried.GetSkillOptions().GetSkillDescriptors()) == 0 {
		t.Fatal("skill descriptors phai duoc bu")
	}
	if len(carried.GetCustomSubagents()) == 0 {
		t.Fatal("custom subagents phai duoc bu")
	}
	if len(carried.GetRepositoryInfo()) == 0 {
		t.Fatal("repository_info phai duoc bu")
	}
	if liveRC.GetMcpFileSystemOptions() != nil || len(liveRC.GetSkillOptions().GetSkillDescriptors()) != 0 {
		t.Fatal("carry-forward da mutate request context song")
	}
}

// Client báo MCP options rỗng một cách tường minh (đã tắt hết server) thì carry-forward
// không được "hồi sinh" server đã tắt.
func TestCarryForwardStaticContextRespectsExplicitEmptyMCPClear(t *testing.T) {
	root := mcpCarryWriteRoot(t)
	turn1Entry := mcpCarryRequestContextEntry(t, 1, "request-1", reproStaticRequestContext(root), nil)

	clearedRC := reproRealtimeRequestContext(root)
	clearedRC.McpFileSystemOptions = &agentv1.McpFileSystemOptions{}
	carried := carryForwardStaticContext(clearedRC, []HistoryEntry{turn1Entry})

	if len(carried.GetMcpFileSystemOptions().GetMcpDescriptors()) > 0 {
		t.Fatalf("snapshot MCP cu da ghi de len tin hieu clear: %#v", carried.GetMcpFileSystemOptions())
	}
	if len(carried.GetSkillOptions().GetSkillDescriptors()) == 0 {
		t.Fatal("clear MCP khong duoc lam mat skills")
	}
}

// Mở turn mới không được làm đổi phần history đã gửi trước đó (bảo vệ prefix cache):
// static context phải chỉ xuất hiện một lần ở vị trí cũ, phần thay đổi duy nhất là suffix.
func TestStaticContextStaysInPrefixAcrossNewTurns(t *testing.T) {
	root := mcpCarryWriteRoot(t)
	conversation := reproConversation(t, root, 3)
	before := reproCompileMessages(t, conversation)

	appendEntriesInPlace(conversation, []HistoryEntry{
		mcpCarryRequestContextEntry(t, 4, "request-4", reproRealtimeRequestContext(root), append([]HistoryEntry(nil), conversation.Entries...)),
		compactionTestUserEntry(t, 4, "request-4", "cau hoi turn 4", "message-4"),
	})
	after := reproCompileMessages(t, conversation)

	common := 0
	for common < len(before) && common < len(after) && before[common] == after[common] {
		common++
	}
	// Cho phép vài message cuối khác nhau (suffix động của turn hiện tại).
	if diff := len(before) - common; diff > 3 {
		t.Fatalf("turn mới làm đổi %d message lịch sử ở đầu request => prefix cache bi pha (common=%d, before=%d, after=%d)",
			diff, common, len(before), len(after))
	}
	if staticIndex := reproIndexOfMarker(before, reproSkillMarker); staticIndex >= 0 && staticIndex >= common {
		t.Fatalf("block static nam trong phan bi turn moi viet lai (staticIndex=%d, common=%d)", staticIndex, common)
	}
}

func reproIndexOfMarker(messages []reproMessage, marker string) int {
	for index, message := range messages {
		if strings.Contains(message.Content, marker) {
			return index
		}
	}
	return -1
}

// Kiểm tra ngược cho chính bộ đếm: nó phải phát hiện được block static bị nhân đôi, nếu không
// mọi assertion count==1 ở trên đều vô nghĩa. Dựng history theo hình dạng "trước khi sửa"
// (entry realtime không mang skills/subagents) rồi đo: <rules> sẽ xuất hiện 2 lần vì block
// turn 1 và block turn 2 có nội dung khác nhau nên dedup không gộp được.
func TestStaticBlockCountsDetectDuplicateBlocks(t *testing.T) {
	root := mcpCarryWriteRoot(t)
	conversation := &ConversationFile{
		ConversationID:     "conv-repro-dup-detect",
		RootConversationID: "conv-repro-dup-detect",
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
	}
	legacyRC := normalizeRealtimeRequestContextForStorage(reproRealtimeRequestContext(root))
	if legacyRC == nil {
		t.Fatal("normalizeRealtimeRequestContextForStorage tra ve nil")
	}
	if len(legacyRC.GetSkillOptions().GetSkillDescriptors()) != 0 {
		t.Fatal("mau 'truoc khi sua' khong duoc mang skills")
	}
	legacyPayload, err := protojson.Marshal(legacyRC)
	if err != nil {
		t.Fatalf("marshal legacy entry: %v", err)
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		mcpCarryRequestContextEntry(t, 1, "request-1", reproStaticRequestContext(root), conversation.Entries),
		compactionTestUserEntry(t, 1, "request-1", "cau hoi dau tien", "message-1"),
		newAssistantTextEntry(1, "request-1", "tra loi dau tien", "", ""),
		{TurnSeq: 2, RequestID: "request-2", Role: "user", Kind: "request_context", Payload: legacyPayload},
		compactionTestUserEntry(t, 2, "request-2", "cau hoi turn 2", "message-2"),
	})

	counts := reproStaticBlockCounts(reproCompileText(t, conversation))
	t.Logf("de dang block static duoc bo dem phat hien: %s", reproCountsString(counts))
	if counts["rules"] < 2 {
		t.Fatalf("bo dem khong phat hien block <rules> bi nhan doi: %s", reproCountsString(counts))
	}
	if counts["agent_skills"] != 1 {
		t.Fatalf("ky vong skills chi co trong block turn 1: %s", reproCountsString(counts))
	}
}

// Kịch bản gốc: auto-compact trong turn có entry request_context.
// Trước sửa: skills + subagents + repository_info mất, <user_info> méo.
func TestAutoCompactionKeepsClientPushedStaticContext(t *testing.T) {
	root := mcpCarryWriteRoot(t)
	conversation := reproConversation(t, root, 4)

	beforeText := reproCompileText(t, conversation)
	before := reproMeasure(beforeText)
	reproAssertFull(t, "truoc compact", before)

	reproCompact(t, conversation, 4, "request-4")

	afterText := reproCompileText(t, conversation)
	after := reproMeasure(afterText)
	t.Logf("matrix sau auto-compact (turn hien tai co request_context): %s", reproPresenceString(after))
	reproAssertFull(t, "sau compact", after)

	// Dedup: mọi block static phải gộp về đúng một bản, cả trước lẫn sau compact,
	// để phần đầu của history (prefix đã cache) không đổi.
	for label, counts := range map[string]map[string]int{
		"truoc compact": reproStaticBlockCounts(beforeText),
		"sau compact":   reproStaticBlockCounts(afterText),
	} {
		for key, count := range counts {
			if count != 1 {
				t.Fatalf("%s: section %s xuat hien %d lan, muon 1 (dedup that bai, prefix cache bi pha): %s",
					label, key, count, reproCountsString(counts))
			}
		}
	}
}

// Kịch bản xấu hơn: turn bị compact KHÔNG có entry request_context nào khớp TurnSeq+RequestID
// (prewarm/supersede chiếm turnSeq, hoặc intent đến mà client không gửi request context).
// Trước sửa: model mất sạch <mcp_file_system> + <rules> + static context.
func TestAutoCompactionWithoutCurrentTurnRequestContextKeepsMCPAndRules(t *testing.T) {
	root := mcpCarryWriteRoot(t)
	conversation := &ConversationFile{
		ConversationID:     "conv-repro-no-current-rc",
		RootConversationID: "conv-repro-no-current-rc",
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		mcpCarryRequestContextEntry(t, 1, "request-1", reproStaticRequestContext(root), conversation.Entries),
		compactionTestUserEntry(t, 1, "request-1", "cau hoi dau tien", "message-1"),
		newAssistantTextEntry(1, "request-1", "tra loi dau tien", "", ""),
	})
	// Turn 2 đến từ client không kèm request_context (intent.RequestContext == nil nên
	// buildRunEntries không ghi entry nào), nhưng vẫn được đếm turnSeq + requestID mới.
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 2, "request-2", "cau hoi turn 2 khong co request_context", "message-2"),
		newAssistantTextEntry(2, "request-2", "tra loi turn 2", "", ""),
	})

	reproAssertFull(t, "truoc compact", reproMeasure(reproCompileText(t, conversation)))

	reproCompact(t, conversation, 2, "request-2")

	presence := reproMeasure(reproCompileText(t, conversation))
	t.Logf("matrix sau compact khi turn hien tai khong co request_context: %s", reproPresenceString(presence))
	if !presence["mcp"] {
		t.Fatalf("turn bi compact khong co request_context khop => mat <mcp_file_system>: %s", reproPresenceString(presence))
	}
	if !presence["rules"] {
		t.Fatalf("mat <rules> khi turn hien tai khong co request_context: %s", reproPresenceString(presence))
	}
	if !presence["agent_skills"] || !presence["subagents"] {
		t.Fatalf("mat skills/subagents khi turn hien tai khong co request_context: %s", reproPresenceString(presence))
	}
}

// Sau compact, mở turn mới (hành vi user stop rồi gửi tin) vẫn đủ static context và
// không nhân đôi block static.
func TestNewTurnAfterCompactionKeepsStaticContextWithoutDuplicates(t *testing.T) {
	root := mcpCarryWriteRoot(t)
	conversation := reproConversation(t, root, 4)
	reproCompact(t, conversation, 4, "request-4")

	appendEntriesInPlace(conversation, []HistoryEntry{
		mcpCarryRequestContextEntry(t, 5, "request-5", reproRealtimeRequestContext(root), append([]HistoryEntry(nil), conversation.Entries...)),
		compactionTestUserEntry(t, 5, "request-5", "cau hoi sau compact", "message-5"),
	})

	text := reproCompileText(t, conversation)
	presence := reproMeasure(text)
	t.Logf("matrix sau khi mo turn moi: %s", reproPresenceString(presence))
	reproAssertFull(t, "turn moi sau compact", presence)

	counts := reproStaticBlockCounts(text)
	for key, count := range counts {
		if count != 1 {
			t.Fatalf("turn moi sau compact: section %v xuat hien %d lan, muon 1: %s", key, count, reproCountsString(counts))
		}
	}
}
