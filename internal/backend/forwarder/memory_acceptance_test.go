// memory_acceptance_test.go chứng minh 4 acceptance criteria của
// plans/20261120-byok-project-memory + kill-switch + rate-limit.
package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	"cursor/internal/memory"
	"google.golang.org/protobuf/encoding/protojson"
)

func newMemoryAcceptanceFixture(t *testing.T) (*DefaultPromptCompiler, *memory.Store, *Service, *ConversationFile) {
	t.Helper()
	store := memory.NewStore(t.TempDir())
	cfg := &memoryConfigStub{enabled: true, autoCapture: true, overviewMax: 4096}
	compiler := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
	compiler.SetMemoryDependencies(store, cfg)
	service := &Service{memoryStore: store, memoryConfig: cfg}
	conversation := &ConversationFile{
		ConversationID: "conv-accept",
		Mode:           "agent",
		NextTurnSeq:    2,
		WorkspacePaths: []string{`d:\proj`},
	}
	return compiler, store, service, conversation
}

// AC1: convention sống qua conversation — session A lưu qua tool path,
// conversation B (mới, cùng workspace) thấy overview + search ra nội dung.
func TestAcceptance1_ConventionSurvivesNewConversation(t *testing.T) {
	compiler, _, service, _ := newMemoryAcceptanceFixture(t)
	streamA := &ActiveStream{WorkspacePaths: []string{`d:\proj`}}
	out := service.saveMemoryResult(streamA, mustArgsJSON(t, saveMemoryArgs{
		Title: "Gradle compile command", Category: string(memory.CategoryBuildConfig),
		Keywords: []string{"gradle", "compileJava"},
		Content:  "Verify backend compiles with ./gradlew.bat compileJava -q.",
		Origin:   "user",
	}))
	if !strings.HasPrefix(out, "Saved memory") {
		t.Fatalf("save failed: %q", out)
	}
	conversationB := &ConversationFile{ConversationID: "conv-b", Mode: "agent", NextTurnSeq: 1, WorkspacePaths: []string{`d:\proj`}}
	context, ok := compiler.buildMemoryOverviewPromptContext(conversationB)
	if !ok || !strings.Contains(context.Message.Content, "Gradle compile command") {
		t.Fatalf("new conversation must see overview: ok=%t", ok)
	}
	searchOut := service.searchMemoryResult(streamA, mustArgsJSON(t, searchMemoryArgs{Query: "how to compile backend gradle"}))
	if !strings.Contains(searchOut, "gradlew.bat compileJava") {
		t.Fatalf("search must return durable content: %q", searchOut)
	}
}

// AC2: audit trail — sửa file memory từ ngoài được tool layer phản ánh ngay.
func TestAcceptance2_UserFileEditVisible(t *testing.T) {
	_, store, service, conversation := newMemoryAcceptanceFixture(t)
	stream := &ActiveStream{WorkspacePaths: []string{`d:\proj`}}
	saved, err := store.Save("d-proj", memory.Entry{
		Title: "Deploy target", Category: memory.CategoryEnvConfig,
		Keywords: []string{"deploy"}, Content: "Deploy to staging via docker compose.", Origin: memory.OriginModel,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out := service.searchMemoryResult(stream, mustArgsJSON(t, searchMemoryArgs{Query: "deploy"})); !strings.Contains(out, "docker compose") {
		t.Fatalf("baseline search: %q", out)
	}
	// User mở file sửa bằng editor: thêm keyword "k8s".
	realPath := filepath.Join(store.Root(), "projects", filepath.FromSlash(saved.RelPath))
	raw, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), "keywords:\n    - deploy", "keywords:\n    - deploy\n    - k8s", 1)
	if edited == string(raw) {
		// fallback: format yaml list có thể là inline — vá đơn giản hơn.
		edited = strings.Replace(string(raw), "- \"deploy\"", "- \"deploy\"\n    - \"k8s\"", 1)
	}
	if err := os.WriteFile(realPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	future := saved.UpdatedAt.AddDate(0, 0, 1)
	if err := os.Chtimes(realPath, future, future); err != nil {
		t.Fatal(err)
	}
	entries, _, err := store.List("d-proj")
	if err != nil || len(entries) != 1 {
		t.Fatalf("list after edit: %v %d", err, len(entries))
	}
	if !strings.Contains(strings.Join(entries[0].Keywords, ","), "k8s") {
		t.Fatalf("external edit invisible: %+v", entries[0])
	}
	_ = conversation
}

// AC3: prefix-cache safety — overview chỉ được persist MỘT lần khi nội dung
// đổi; history prefix giữa 2 compile không đổi byte nào khi store ổn định.
func TestAcceptance3_PrefixCacheStability(t *testing.T) {
	compiler, store, _, conversation := newMemoryAcceptanceFixture(t)
	if _, err := store.Save("d-proj", memory.Entry{
		Title: "Pinned convention", Category: memory.CategoryPracticeSpec,
		Keywords: []string{"convention"}, Content: "Use conventional commits in this repo.", Origin: memory.OriginUser,
	}); err != nil {
		t.Fatal(err)
	}
	// Turn 1: derive ra context, giả lập persist entry như service loop.
	contexts, err := compiler.DerivePromptContexts(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, "first question")
	if err != nil {
		t.Fatal(err)
	}
	var memoryContext *PromptContextMessage
	for index := range contexts {
		if contexts[index].Source == promptContextSourceProjectMemories {
			memoryContext = &contexts[index]
		}
	}
	if memoryContext == nil {
		t.Fatal("turn 1 must emit memory context")
	}
	payload, _ := json.Marshal(promptContextEntryPayload{
		Source: memoryContext.Source, Role: memoryContext.Message.Role,
		Content: memoryContext.Message.Content, ContentHash: memoryContext.ContentHash,
	})
	userPayload, _ := protojson.Marshal(&agentv1.UserMessage{Text: "first question"})
	conversation.Entries = append(conversation.Entries,
		HistoryEntry{TurnSeq: 1, Kind: "user_message", Role: "user", Payload: userPayload},
		HistoryEntry{TurnSeq: 1, Kind: "prompt_context", Role: "system", Payload: payload},
	)

	compileA, err := compiler.Compile(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, "second question", "Test Model")
	if err != nil {
		t.Fatal(err)
	}
	// Turn 2 với store KHÔNG đổi: không emit context mới (không nhân bản block).
	contexts2, err := compiler.DerivePromptContexts(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, "second question")
	if err != nil {
		t.Fatal(err)
	}
	for _, context := range contexts2 {
		if context.Source == promptContextSourceProjectMemories {
			t.Fatal("unchanged store must not re-emit overview")
		}
	}
	// Compile 2 lần liên tiếp stable byte-for-byte.
	compileB, err := compiler.Compile(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, "second question", "Test Model")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(compileA.Messages, compileB.Messages) {
		t.Fatal("identical inputs must compile to identical messages")
	}
	// Overview đúng MỘT lần trong messages của compile (đếm theo block emit
	// thật sự, không đếm literal trong system prompt rules).
	count := 0
	for _, message := range compileA.Messages {
		if message.Role == "system" {
			continue
		}
		if strings.Contains(message.Content, "<project_memories scope=\"d-proj\">") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("overview must appear exactly once in replay, got %d", count)
	}
}

// AC4: compaction handoff -> entries auto -> nổi lên trong overview.
func TestAcceptance4_CompactionToOverview(t *testing.T) {
	compiler, store, service, conversation := newMemoryAcceptanceFixture(t)
	stream := &ActiveStream{WorkspacePaths: []string{`d:\proj`}}
	captured, _ := service.captureMemoriesFromCompaction(stream, captureFixtureHandoff)
	if captured == 0 {
		t.Fatal("capture expected")
	}
	entries, _, err := store.List("d-proj")
	if err != nil || len(entries) != captured {
		t.Fatalf("entries=%d captured=%d err=%v", len(entries), captured, err)
	}
	context, ok := compiler.buildMemoryOverviewPromptContext(conversation)
	if !ok {
		t.Fatal("overview expected after capture")
	}
	if !strings.Contains(context.Message.Content, "Dead end:") && !strings.Contains(context.Message.Content, "Constraint:") {
		t.Fatalf("captured titles missing from overview:\n%s", context.Message.Content)
	}
}

// Kill-switch: memoryEnabled=false vô hiệu cả 3 đường (overview, tools, capture).
func TestAcceptance5_KillSwitch(t *testing.T) {
	compiler, store, _, conversation := newMemoryAcceptanceFixture(t)
	if _, err := store.Save("d-proj", memory.Entry{Title: "Kill switch probe", Category: memory.CategoryPitfall, Content: "body text here", Origin: memory.OriginModel}); err != nil {
		t.Fatal(err)
	}
	off := &memoryConfigStub{enabled: false, autoCapture: true, overviewMax: 4096}
	compiler.SetMemoryDependencies(store, off)
	if _, ok := compiler.buildMemoryOverviewPromptContext(conversation); ok {
		t.Fatal("disabled compiler must not emit")
	}
	service := &Service{memoryStore: store, memoryConfig: off}
	stream := &ActiveStream{WorkspacePaths: []string{`d:\proj`}}
	if out := service.saveMemoryResult(stream, mustArgsJSON(t, saveMemoryArgs{Title: "Nope entry", Category: string(memory.CategoryPitfall), Content: "no", Origin: "model"})); out != "Project memory is disabled." {
		t.Fatalf("disabled save: %q", out)
	}
	if out := service.searchMemoryResult(stream, mustArgsJSON(t, searchMemoryArgs{Query: "x"})); out != "Project memory is disabled." {
		t.Fatalf("disabled search: %q", out)
	}
	if got, _ := service.captureMemoriesFromCompaction(stream, captureFixtureHandoff); got != 0 {
		t.Fatal("disabled capture must be inert")
	}
}

// Rate-limit: attempt thứ 31 trong cùng run bị chặn, không ghi thêm file.
func TestAcceptance6_SaveMemoryRateLimit(t *testing.T) {
	_, store, service, _ := newMemoryAcceptanceFixture(t)
	stream := &ActiveStream{WorkspacePaths: []string{`d:\proj`}}
	for i := 1; i <= memorySaveRateLimitPerStream; i++ {
		out := service.saveMemoryResult(stream, mustArgsJSON(t, saveMemoryArgs{
			Title: "Rate limit probe entry " + itoa(i), Category: string(memory.CategoryTaskExperience),
			Content: "durable content for probe number " + itoa(i), Origin: "model",
		}))
		if !strings.HasPrefix(out, "Saved memory") {
			t.Fatalf("attempt %d unexpectedly blocked: %q", i, out)
		}
	}
	blocked := service.saveMemoryResult(stream, mustArgsJSON(t, saveMemoryArgs{
		Title: "Rate limit overflow entry", Category: string(memory.CategoryTaskExperience),
		Content: "this attempt must hit the limit", Origin: "model",
	}))
	if !strings.Contains(blocked, "rate limited") {
		t.Fatalf("overflow not blocked: %q", blocked)
	}
	entries, _, err := store.List("d-proj")
	if err != nil || len(entries) != memorySaveRateLimitPerStream {
		t.Fatalf("overflow must not write: %d entries err=%v", len(entries), err)
	}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
