// compiler_memory.go phát hành project memory overview vào request động hiện tại
// theo kênh prompt_context (persist khi nội dung đổi, append-only, không rewrite
// history — giữ prefix cache).
package forwarder

import (
	"encoding/json"
	"strings"

	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/memory"
)

const promptContextSourceProjectMemories = "project_memories"

// promptContextSourceMemoryNudge: reminder 1 dòng ở request động khi user message
// cho thấy tín hiệu chỉnh hướng / directive dài hạn (Option A của plan memory).
const promptContextSourceMemoryNudge = "memory_nudge"

const projectMemoriesGuide = "Guide: recall with SearchMemory before asking the user; SaveMemory for durable lessons only; never store credentials."

const memoryNudgeText = "<memory_nudge>\nThe latest user message contains a correction, preference, or lasting directive signal. " +
	"If this exchange produced a durable lesson, user rule, or verified pitfall, call SaveMemory before finishing this turn. " +
	"SearchMemory by title first and re-save with the same title to update an existing memory. Ignore if nothing durable changed.\n</memory_nudge>"

// memoryNudgeMarkers là danh sách substring (đã lowercase) báo hiệu directive
// hoặc correction trong user message. Conservatively biased: nudge 1 dòng rẻ,
// bỏ sót đắt hơn.
var memoryNudgeMarkers = []string{
	// Tiếng Việt
	"nhớ là", "nhớ giúp", "nhớ quy ước", "ghi nhớ", "từ giờ", "từ nay", "quy ước",
	"về sau", "mặc định từ", "đừng dùng", "đừng gọi", "đừng làm", "không phải",
	"sai rồi", "thay vì", "sửa lại thành", "làm lại thành", "nhắc lại", "bảo bạn",
	"nhiều lần rồi", "luôn dùng", "luôn sử dụng", "lần sau",
	// English
	"remember", "note that", "from now on", "instead of", "don't use", "do not use",
	"never use", "never call", "always use", "stop using", "i told you", "again?",
	"should be", "is wrong",
}

func userTextSuggestsDurableLesson(latestUserText string) bool {
	lowered := strings.ToLower(strings.TrimSpace(latestUserText))
	if lowered == "" {
		return false
	}
	for _, marker := range memoryNudgeMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// buildMemoryNudgePromptContext phát hành nudge khi memory bật VÀ user message
// gần nhất mang tín hiệu. Không dùng last-hash suppression như overview:
// nudge có chủ đích lặp theo turn (mỗi turn khớp một bản, append-only),
// dedup trong-turn đã có filterCurrentTurnPromptContexts lo (source+hash).
func (compiler *DefaultPromptCompiler) buildMemoryNudgePromptContext(conversation *ConversationFile, latestUserText string) (PromptContextMessage, bool) {
	if compiler == nil || compiler.memoryStore == nil || conversation == nil {
		return PromptContextMessage{}, false
	}
	if compiler.memoryConfig != nil && !compiler.memoryConfig.MemoryEnabled() {
		return PromptContextMessage{}, false
	}
	if !userTextSuggestsDurableLesson(latestUserText) {
		return PromptContextMessage{}, false
	}
	return newPromptContextMessage(promptContextSourceMemoryNudge, modeladapter.Message{Role: "user", Content: memoryNudgeText}, false), true
}

// SetMemoryDependencies nối store + config memory vào compiler (test seam).
func (compiler *DefaultPromptCompiler) SetMemoryDependencies(store *memory.Store, cfg projectMemoryConfig) {
	if compiler == nil {
		return
	}
	compiler.memoryStore = store
	compiler.memoryConfig = cfg
}

// buildMemoryOverviewPromptContext trả context overview khi và chỉ khi:
// memory bật, conversation có workspace, store có entry, và nội dung overview
// KHÁC bản cuối đã persist trong history (chống lặp token qua các turn).
func (compiler *DefaultPromptCompiler) buildMemoryOverviewPromptContext(conversation *ConversationFile) (PromptContextMessage, bool) {
	if compiler == nil || compiler.memoryStore == nil || conversation == nil {
		return PromptContextMessage{}, false
	}
	if compiler.memoryConfig != nil && !compiler.memoryConfig.MemoryEnabled() {
		return PromptContextMessage{}, false
	}
	if len(conversation.WorkspacePaths) == 0 {
		return PromptContextMessage{}, false
	}
	project := memory.ProjectSlug(conversation.WorkspacePaths[0])
	if project == "" {
		return PromptContextMessage{}, false
	}
	maxBytes := 0
	if compiler.memoryConfig != nil {
		maxBytes = compiler.memoryConfig.MemoryOverviewMaxBytes()
	}
	overview, err := compiler.memoryStore.Overview(project, maxBytes)
	if err != nil || strings.TrimSpace(overview) == "" {
		return PromptContextMessage{}, false
	}
	block := "<project_memories scope=\"" + project + "\">\n" +
		strings.TrimSpace(overview) + "\n" +
		projectMemoriesGuide + "\n</project_memories>"
	message := modeladapter.Message{Role: "user", Content: block}
	if lastPromptContextContentHash(conversation, promptContextSourceProjectMemories) == promptContextContentHash(message) {
		return PromptContextMessage{}, false
	}
	return newPromptContextMessage(promptContextSourceProjectMemories, message, false), true
}

// lastPromptContextContentHash quét ngược history tìm bản prompt_context gần
// nhất của source cho trước; "" khi chưa từng persist (hoặc compaction đã
// cắt sạch các bản cũ — khi đó phát lại là đúng ý nghĩa).
func lastPromptContextContentHash(conversation *ConversationFile, source string) string {
	if conversation == nil {
		return ""
	}
	for index := len(conversation.Entries) - 1; index >= 0; index-- {
		entry := conversation.Entries[index]
		if strings.TrimSpace(entry.Kind) != "prompt_context" {
			continue
		}
		var payload promptContextEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Source) != strings.TrimSpace(source) {
			continue
		}
		return strings.TrimSpace(payload.ContentHash)
	}
	return ""
}
