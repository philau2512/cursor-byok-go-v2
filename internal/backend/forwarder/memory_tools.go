// memory_tools.go xử lý SaveMemory/SearchMemory server-side cho project memory,
// không round-trip exec sang client (theo precedent local-state TodoWrite).
package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	"cursor/internal/memory"
	"google.golang.org/protobuf/types/known/structpb"
)

type saveMemoryArgs struct {
	Title         string   `json:"title"`
	Category      string   `json:"category"`
	Content       string   `json:"content"`
	Keywords      []string `json:"keywords"`
	UsageScenario []string `json:"usage_scenario"`
	Origin        string   `json:"origin"`
}

type searchMemoryArgs struct {
	Query    string `json:"query"`
	Category string `json:"category"`
	Limit    int    `json:"limit"`
}

// memorySecretPattern phát hiện dạng credential phổ biến; bảo thủ để không
// chặn nhầm lệnh build chứa từ khóa (yêu cầu dấu :/= nối tiếp chuỗi dài).
var memorySecretPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|passw(?:or)?d|token)\s*[:=]\s*\S{8,}`)

var memoryPrivateKeysPattern = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)

func containsMemorySecret(content string) bool {
	return memorySecretPattern.MatchString(content) || memoryPrivateKeysPattern.MatchString(content)
}

// projectMemorySlug lấy workspace đầu tiên của stream; "" khi không có path.
func projectMemorySlug(stream *ActiveStream) string {
	if stream == nil {
		return ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if len(stream.WorkspacePaths) == 0 {
		return ""
	}
	return memory.ProjectSlug(stream.WorkspacePaths[0])
}

// memoryEnabledFor báo whether store + config cho phép dùng memory.
func (service *Service) memoryEnabledFor() bool {
	if service == nil || service.memoryStore == nil {
		return false
	}
	if service.memoryConfig == nil {
		return true
	}
	return service.memoryConfig.MemoryEnabled()
}

func (service *Service) handleSaveMemoryToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	resultText := service.saveMemoryResult(stream, invocation.ArgsJSON)
	return service.completeImmediateToolResult(stream, invocation, resultText, buildMemoryToolCall(invocation, resultText))
}

func (service *Service) handleSearchMemoryToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	resultText := service.searchMemoryResult(stream, invocation.ArgsJSON)
	return service.completeImmediateToolResult(stream, invocation, resultText, buildMemoryToolCall(invocation, resultText))
}

// --- UI card (mượn format MCP, không sửa proto) ---------------------------
//
// Cursor client hiển thị tool theo typed ToolCall oneof; SaveMemory/SearchMemory
// không có variant riêng nên ta dựng chúng dưới dạng McpToolCall với
// serverIdentifier "byok-memory" — kết quả là card MCP generic (tên tool,
// args JSON, text result) đúng như cách call MCP hiển thị.

const memoryToolServerIdentifier = "byok-memory"

// buildMemoryToolCall tạo ToolCall hiển thị cho started (resultText == "")
// và completed (resultText != "").
func buildMemoryToolCall(invocation runtimecore.ToolInvocation, resultText string) *agentv1.ToolCall {
	trimmedResult := strings.TrimSpace(resultText)
	var result *agentv1.McpToolResult
	if trimmedResult != "" {
		result = &agentv1.McpToolResult{
			Result: &agentv1.McpToolResult_Success{
				Success: &agentv1.McpSuccess{
					Content: []*agentv1.McpToolResultContentItem{{
						Content: &agentv1.McpToolResultContentItem_Text{
							Text: &agentv1.McpTextContent{Text: trimmedResult},
						},
					}},
					IsError: isMemoryToolFailureResult(trimmedResult),
				},
			},
		}
	}
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_McpToolCall{
			McpToolCall: &agentv1.McpToolCall{
				Args: &agentv1.McpArgs{
					Name:               memoryToolServerIdentifier + ":" + strings.TrimSpace(invocation.ToolName),
					Args:               decodeMemoryArgsForDisplay(invocation.ArgsJSON),
					ToolCallId:         strings.TrimSpace(invocation.CallID),
					ProviderIdentifier: memoryToolServerIdentifier,
					ToolName:           strings.TrimSpace(invocation.ToolName),
				},
				Result: result,
			},
		},
	}
}

// decodeMemoryArgsForDisplay chuyển args JSON sang map structpb cho card;
// JSON hỏng/thiếu trả về map rỗng (card vẫn hiển thị tên tool).
func decodeMemoryArgsForDisplay(argsJSON []byte) map[string]*structpb.Value {
	if len(argsJSON) == 0 {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(argsJSON, &raw); err != nil {
		return nil
	}
	normalized := make(map[string]any, len(raw))
	for key, value := range raw {
		if strings.TrimSpace(key) == "" {
			continue
		}
		normalized[key] = value
	}
	message, err := structpb.NewStruct(normalized)
	if err != nil {
		return nil
	}
	return message.GetFields()
}

// isMemoryToolFailureResult nhận diện result lỗi/recoverable của 2 tool để
// đánh dấu IsError trên card.
func isMemoryToolFailureResult(resultText string) bool {
	for _, prefix := range []string{"SaveMemory failed:", "SearchMemory failed:", "Project memory is disabled.", "SaveMemory rate limited"} {
		if strings.HasPrefix(resultText, prefix) {
			return true
		}
	}
	return false
}

// memorySaveRateLimitPerStream chặn model spam SaveMemory trong một run.
const memorySaveRateLimitPerStream = 30

// saveMemoryResult chứa toàn bộ logic SaveMemory, tách khỏi completion để test
// không phải dựng cả pipeline appendToolResult/broker.
func (service *Service) saveMemoryResult(stream *ActiveStream, argsJSON []byte) string {
	if !service.memoryEnabledFor() {
		return "Project memory is disabled."
	}
	if stream != nil {
		stream.mu.Lock()
		stream.MemorySaveCount++
		attempt := stream.MemorySaveCount
		stream.mu.Unlock()
		if attempt > memorySaveRateLimitPerStream {
			return "SaveMemory rate limited for this run; consolidate memories."
		}
	}
	var args saveMemoryArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "SaveMemory failed: invalid arguments JSON: " + err.Error() + " | Send a valid JSON object with title, category, content, origin."
	}
	origin := strings.TrimSpace(strings.ToLower(args.Origin))
	switch origin {
	case "":
		origin = string(memory.OriginModel)
	case string(memory.OriginUser), string(memory.OriginModel):
	default:
		return fmt.Sprintf("SaveMemory failed: origin must be \"user\" or \"model\" (got %q; \"auto\" is reserved for system capture)", args.Origin)
	}
	if containsMemorySecret(args.Content) {
		return "SaveMemory failed: memory content must not contain credentials; store a pointer to where the secret lives instead."
	}
	project := projectMemorySlug(stream)
	if project == "" {
		return "SaveMemory failed: no workspace path is available on this run, so there is no project scope to store into."
	}
	entry := memory.Entry{
		Title:         args.Title,
		Category:      memory.Category(strings.TrimSpace(args.Category)),
		Keywords:      args.Keywords,
		UsageScenario: args.UsageScenario,
		Origin:        memory.Origin(origin),
		Content:       args.Content,
	}
	saved, err := service.memoryStore.Save(project, entry)
	if err != nil {
		return "SaveMemory failed: " + err.Error() +
			" | Retry with ALL required args: title (3-100 chars, 3-10 words, e.g. \"Order Delivery Status Conventions\"), category (enum value), content, origin (\"user\" or \"model\")."
	}
	service.logMemoryEvent(stream, "memory_save", map[string]any{
		"project": project, "rel_path": saved.RelPath, "origin": saved.Origin, "category": saved.Category,
	})
	return fmt.Sprintf("Saved memory: %s (%s) at %s", saved.Title, saved.Category, service.memoryStore.AbsPath(saved.RelPath))
}

// searchMemoryResult là phần logic thuần của SearchMemory (test seam như trên).
func (service *Service) searchMemoryResult(stream *ActiveStream, argsJSON []byte) string {
	if !service.memoryEnabledFor() {
		return "Project memory is disabled."
	}
	var args searchMemoryArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "SearchMemory failed: invalid arguments: " + err.Error()
	}
	project := projectMemorySlug(stream)
	if project == "" {
		return "SearchMemory failed: no workspace path is available on this run."
	}
	maxBytes := 0
	if service.memoryConfig != nil {
		maxBytes = service.memoryConfig.MemoryOverviewMaxBytes()
	}
	if strings.TrimSpace(args.Query) == "" {
		overview, err := service.memoryStore.Overview(project, maxBytes)
		if err != nil {
			return "SearchMemory failed: " + err.Error()
		}
		if strings.TrimSpace(overview) == "" {
			return "No project memories recorded yet for this workspace."
		}
		service.logMemoryEvent(stream, "memory_search_overview", map[string]any{"project": project, "bytes": len(overview)})
		return overview
	}
	results, err := service.memoryStore.Search(project, args.Query, strings.TrimSpace(args.Category), args.Limit)
	if err != nil {
		return "SearchMemory failed: " + err.Error()
	}
	service.logMemoryEvent(stream, "memory_search", map[string]any{"project": project, "hits": len(results)})
	if len(results) == 0 {
		return fmt.Sprintf("No memories matched %q in this project.", strings.TrimSpace(args.Query))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Project memories matching %q (%d):\n", strings.TrimSpace(args.Query), len(results))
	for index, hit := range results {
		fmt.Fprintf(&b, "%d. [%s] %s", index+1, hit.Entry.Category, hit.Entry.Title)
		if hit.Score > 0 {
			fmt.Fprintf(&b, " (score %.0f)", hit.Score)
		}
		b.WriteString("\n")
		if len(hit.Entry.UsageScenario) > 0 {
			fmt.Fprintf(&b, "   when: %s\n", strings.Join(hit.Entry.UsageScenario, " | "))
		}
		fmt.Fprintf(&b, "   %s\n", strings.ReplaceAll(strings.TrimSpace(hit.Entry.Content), "\n", "\n   "))
		fmt.Fprintf(&b, "   file: %s (origin: %s)\n", service.memoryStore.AbsPath(hit.Entry.RelPath), hit.Entry.Origin)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (service *Service) logMemoryEvent(stream *ActiveStream, event string, fields map[string]any) {
	if service == nil || service.debug == nil || stream == nil {
		return
	}
	stream.mu.Lock()
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	stream.mu.Unlock()
	service.debug.LogRuntime(context.Background(), requestID, conversationID, event, fields)
}
