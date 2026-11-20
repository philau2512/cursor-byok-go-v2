package forwarder

import (
	"encoding/json"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	"cursor/internal/memory"
)

func newMemoryTestCompiler(t *testing.T) (*DefaultPromptCompiler, *memory.Store) {
	t.Helper()
	store := memory.NewStore(t.TempDir())
	compiler := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
	compiler.SetMemoryDependencies(store, &memoryConfigStub{enabled: true, autoCapture: true, overviewMax: 4096})
	return compiler, store
}

func mustCompilerSave(t *testing.T, store *memory.Store, project string, entry memory.Entry) {
	t.Helper()
	if _, err := store.Save(project, entry); err != nil {
		t.Fatal(err)
	}
}

func TestBuildMemoryOverviewPromptContextHappy(t *testing.T) {
	compiler, store := newMemoryTestCompiler(t)
	mustCompilerSave(t, store, "d-proj", memory.Entry{
		Title: "Gradle compile command", Category: memory.CategoryBuildConfig,
		Keywords: []string{"gradle"}, Content: "use gradlew", Origin: memory.OriginModel,
	})
	conversation := &ConversationFile{WorkspacePaths: []string{`d:\proj`}}
	context, ok := compiler.buildMemoryOverviewPromptContext(conversation)
	if !ok {
		t.Fatal("expected memory context")
	}
	if context.Source != promptContextSourceProjectMemories {
		t.Fatalf("source = %q", context.Source)
	}
	content := context.Message.Content
	if !strings.HasPrefix(content, "<project_memories scope=\"d-proj\">") {
		t.Fatalf("bad block head:\n%s", content)
	}
	if !strings.Contains(content, "Gradle compile command [gradle]") || !strings.Contains(content, projectMemoriesGuide) {
		t.Fatalf("block missing overview/guide:\n%s", content)
	}
	if context.ContentHash != promptContextContentHash(context.Message) {
		t.Fatal("content hash must match message")
	}
}

func TestBuildMemoryOverviewPromptContextGates(t *testing.T) {
	compiler, store := newMemoryTestCompiler(t)
	mustCompilerSave(t, store, "d-proj", memory.Entry{
		Title: "Some lesson", Category: memory.CategoryPitfall, Content: "body", Origin: memory.OriginModel,
	})
	// Store trống cho project khác -> không emit.
	if _, ok := compiler.buildMemoryOverviewPromptContext(&ConversationFile{WorkspacePaths: []string{`x:\other`}}); ok {
		t.Fatal("empty store must not emit")
	}
	// Không có workspace path.
	if _, ok := compiler.buildMemoryOverviewPromptContext(&ConversationFile{}); ok {
		t.Fatal("missing workspace must not emit")
	}
	// Config tắt.
	disabled := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
	disabled.SetMemoryDependencies(store, &memoryConfigStub{enabled: false})
	if _, ok := disabled.buildMemoryOverviewPromptContext(&ConversationFile{WorkspacePaths: []string{`d:\proj`}}); ok {
		t.Fatal("disabled must not emit")
	}
}

func TestBuildMemoryOverviewSkipsWhenUnchangedSinceLastPersist(t *testing.T) {
	compiler, store := newMemoryTestCompiler(t)
	mustCompilerSave(t, store, "d-proj", memory.Entry{
		Title: "Stable lesson", Category: memory.CategoryDecision, Content: "body", Origin: memory.OriginModel,
	})
	conversation := &ConversationFile{WorkspacePaths: []string{`d:\proj`}}
	context, ok := compiler.buildMemoryOverviewPromptContext(conversation)
	if !ok {
		t.Fatal("first emit expected")
	}
	// Giả lập persist xong (entry prompt_context mang cùng source+hash).
	payload, _ := json.Marshal(promptContextEntryPayload{
		Source:      context.Source,
		Role:        context.Message.Role,
		Content:     context.Message.Content,
		ContentHash: context.ContentHash,
	})
	conversation.Entries = append(conversation.Entries, HistoryEntry{Kind: "prompt_context", Payload: payload})
	if _, ok := compiler.buildMemoryOverviewPromptContext(conversation); ok {
		t.Fatal("unchanged overview must not re-emit")
	}
	// Store đổi -> hash khác -> emit lại (append-only, history cũ giữ nguyên).
	mustCompilerSave(t, store, "d-proj", memory.Entry{
		Title: "New lesson added", Category: memory.CategoryTaskExperience, Content: "more", Origin: memory.OriginModel,
	})
	if _, ok := compiler.buildMemoryOverviewPromptContext(conversation); !ok {
		t.Fatal("changed overview must re-emit")
	}
}

func TestDerivePromptContextsIncludesMemoryCandidate(t *testing.T) {
	compiler, store := newMemoryTestCompiler(t)
	mustCompilerSave(t, store, "d-proj", memory.Entry{
		Title: "Derived lesson", Category: memory.CategoryEnvConfig, Content: "env", Origin: memory.OriginUser,
	})
	conversation := &ConversationFile{
		ConversationID: "conv-memory-1",
		Mode:           "agent",
		NextTurnSeq:    1,
		WorkspacePaths: []string{`d:\proj`},
	}
	contexts, err := compiler.DerivePromptContexts(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, "hello")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, context := range contexts {
		if context.Source == promptContextSourceProjectMemories {
			found = true
			if !context.Persist {
				t.Fatal("memory context must be persisted via standard channel")
			}
			if !strings.Contains(context.Message.Content, "Derived lesson") {
				t.Fatalf("content mismatch: %q", context.Message.Content)
			}
		}
	}
	if !found {
		t.Fatalf("project_memories context missing in %d contexts", len(contexts))
	}
}

func TestMemoryNudgeMarkerDetection(t *testing.T) {
	matching := []string{
		"Nhớ là build backend dùng gradlew.bat nhé",
		"Từ giờ đừng dùng Ls nữa",
		"Sai rồi, phải sửa lại thành TanStack Router",
		"tôi đã bảo bạn rồi mà, sao lặp lại vậy",
		"Remember we never edit generated bindings",
		"use PatchEdit instead of Write for existing files",
		"this should be a staged submission flow",
	}
	nonMatching := []string{
		"refactor login handler cho gọn hơn",
		"why is the test failing with timeout?",
		"",
		"   ",
	}
	for _, text := range matching {
		if !userTextSuggestsDurableLesson(text) {
			t.Errorf("expected nudge marker in %q", text)
		}
	}
	for _, text := range nonMatching {
		if userTextSuggestsDurableLesson(text) {
			t.Errorf("unexpected nudge marker in %q", text)
		}
	}
}

func TestMemoryNudgeEmissionAndGate(t *testing.T) {
	compiler, store := newMemoryTestCompiler(t)
	mustCompilerSave(t, store, "d-proj", memory.Entry{
		Title: "Stable item", Category: memory.CategoryPitfall, Content: "body", Origin: memory.OriginModel,
	})
	conversation := &ConversationFile{WorkspacePaths: []string{`d:\proj`}}
	nudge, ok := compiler.buildMemoryNudgePromptContext(conversation, "từ giờ đừng gọi Ls nữa")
	if !ok {
		t.Fatal("correction signal must emit nudge")
	}
	if nudge.Source != promptContextSourceMemoryNudge {
		t.Fatalf("source = %q", nudge.Source)
	}
	if !strings.Contains(nudge.Message.Content, "SaveMemory before finishing") {
		t.Fatalf("nudge text wrong:\n%s", nudge.Message.Content)
	}
	// Không có tín hiệu -> không nudge.
	if _, ok := compiler.buildMemoryNudgePromptContext(conversation, "list các file trong src"); ok {
		t.Fatal("neutral message must not nudge")
	}
	// Kill-switch -> không nudge.
	off := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), nil)
	off.SetMemoryDependencies(store, &memoryConfigStub{enabled: false})
	if _, ok := off.buildMemoryNudgePromptContext(conversation, "nhớ là phải dùng pnpm"); ok {
		t.Fatal("disabled memory must not nudge")
	}
	// Derive end-to-end: nudge có mặt khi khớp.
	contexts, err := compiler.DerivePromptContexts(conversation, agentv1.AgentMode_AGENT_MODE_AGENT, "nhớ là phải dùng pnpm")
	if err != nil {
		t.Fatal(err)
	}
	var nudgeFound bool
	for _, context := range contexts {
		if context.Source == promptContextSourceMemoryNudge {
			nudgeFound = true
		}
	}
	if !nudgeFound {
		t.Fatal("DerivePromptContexts must include memory nudge for directive text")
	}
}
