// local_subscription_tools.go exposes BYOK-owned tools through the newer
// client channel ListLocalSubscriptionTools / CallLocalSubscriptionTool.
//
// Lý do có kênh này: SaveMemory/SearchMemory là tool của riêng BYOK nên client
// không có typed ToolCall variant cho chúng; hiện chúng đang mượn danh dạng
// MCP tool (memoryToolServerIdentifier) chỉ để UI render được card. Kênh
// subscription tool tách bạch đúng bản chất "tool chạy local, server tự thực
// thi", và description + input_schema được lấy thẳng từ prompt/tools.json nên
// không tồn tại hai bản định nghĩa tool lệch nhau.
//
// Thủ tục đăng ký vẫn là path thật của client (/agent.v1.AgentService/...), và
// message wire lấy thẳng từ bản sync agent_v1.proto: từ khi sync client 3.32,
// năm message LocalSubscriptionTool* đã nằm trong snapshot chính thức với field
// number giống hệt bản chép tay trước đây, nên bản hand-port
// proto/byok_local_v1.proto đã được xóa để không còn hai định nghĩa song song.
package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"cursor/gen/agentv1"
	"cursor/gen/agentv1/agentv1connect"
	promptassets "cursor/prompt"
)

// Procedure path mà client bản mới gọi. Dùng constant do connect sinh ra từ
// agent_v1.proto để path không bao giờ lệch khỏi bản sync.
const (
	ListLocalSubscriptionToolsProcedure = agentv1connect.AgentServiceListLocalSubscriptionToolsProcedure
	CallLocalSubscriptionToolProcedure  = agentv1connect.AgentServiceCallLocalSubscriptionToolProcedure
)

// localSubscriptionTool là một tool BYOK sở hữu, thực thi server-side.
type localSubscriptionTool struct {
	// name khớp "function.name" trong prompt/tools.json.
	name string
	// assetMode quyết định đọc catalog nào khi lấy description/schema.
	assetMode promptassets.Mode
	// enabled báo tool có được expose lúc này hay không.
	enabled func(service *Service) bool
	// run thực thi tool với args JSON gốc từ client.
	run func(service *Service, stream *ActiveStream, argsJSON []byte) string
}

// localSubscriptionToolSpecs là registry duy nhất của kênh subscription tool.
// Thêm tool BYOK mới thì khai báo ở đây, không tạo route riêng lẻ.
func localSubscriptionToolSpecs() []localSubscriptionTool {
	return []localSubscriptionTool{
		{
			name:      "SaveMemory",
			assetMode: promptassets.ModeAgent,
			enabled:   func(service *Service) bool { return service.memoryEnabledFor() },
			run: func(service *Service, stream *ActiveStream, argsJSON []byte) string {
				return service.saveMemoryResult(stream, argsJSON)
			},
		},
		{
			name:      "SearchMemory",
			assetMode: promptassets.ModeAgent,
			enabled:   func(service *Service) bool { return service.memoryEnabledFor() },
			run: func(service *Service, stream *ActiveStream, argsJSON []byte) string {
				return service.searchMemoryResult(stream, argsJSON)
			},
		},
	}
}

// subscriptionToolCatalogCache nhớ kết quả parse tools.json theo mode.
var subscriptionToolCatalogCache sync.Map // promptassets.Mode -> map[string]*agentv1.LocalSubscriptionToolDefinition

// subscriptionToolCatalog trả về definition theo tên tool từ tools.json.
func subscriptionToolCatalog(mode promptassets.Mode) map[string]*agentv1.LocalSubscriptionToolDefinition {
	if cached, ok := subscriptionToolCatalogCache.Load(mode); ok {
		definitions, typed := cached.(map[string]*agentv1.LocalSubscriptionToolDefinition)
		if typed {
			return definitions
		}
	}
	definitions := make(map[string]*agentv1.LocalSubscriptionToolDefinition)
	rawTools, err := promptassets.ReadTools(mode)
	if err == nil {
		var items []json.RawMessage
		if err := json.Unmarshal(rawTools, &items); err == nil {
			for _, item := range items {
				definition, ok := parseSubscriptionToolDefinition(item)
				if !ok {
					continue
				}
				definitions[definition.GetToolName()] = definition
			}
		}
	}
	subscriptionToolCatalogCache.Store(mode, definitions)
	return definitions
}

// parseSubscriptionToolDefinition đọc {function:{name,description,parameters}}.
func parseSubscriptionToolDefinition(raw json.RawMessage) (*agentv1.LocalSubscriptionToolDefinition, bool) {
	var envelope struct {
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, false
	}
	toolName := strings.TrimSpace(envelope.Function.Name)
	if toolName == "" {
		return nil, false
	}
	schema := strings.TrimSpace(string(envelope.Function.Parameters))
	if schema == "" {
		schema = `{"type":"object","properties":{}}`
	}
	return &agentv1.LocalSubscriptionToolDefinition{
		ToolName:        toolName,
		Description:     strings.TrimSpace(envelope.Function.Description),
		InputSchemaJson: schema,
	}, true
}

// findLocalSubscriptionTool tra tool theo tên trong registry.
func findLocalSubscriptionTool(name string) (localSubscriptionTool, bool) {
	trimmed := strings.TrimSpace(name)
	for _, candidate := range localSubscriptionToolSpecs() {
		if candidate.name == trimmed {
			return candidate, true
		}
	}
	return localSubscriptionTool{}, false
}

// enabledLocalSubscriptionTools liệt kê tool đang bật, theo thứ tự khai báo.
func (service *Service) enabledLocalSubscriptionTools() []*agentv1.LocalSubscriptionToolDefinition {
	definitions := make([]*agentv1.LocalSubscriptionToolDefinition, 0, len(localSubscriptionToolSpecs()))
	if service == nil {
		return definitions
	}
	for _, tool := range localSubscriptionToolSpecs() {
		if tool.enabled != nil && !tool.enabled(service) {
			continue
		}
		catalog := subscriptionToolCatalog(tool.assetMode)
		cached, ok := catalog[tool.name]
		if !ok {
			// Không có trong catalog thì vẫn expose nhưng không có schema,
			// để lộ ra lỗi ở tools.json chứ không âm thầm mất tool.
			definitions = append(definitions, &agentv1.LocalSubscriptionToolDefinition{
				ToolName: tool.name,
			})
			continue
		}
		// Tạo message mới cho mỗi response: cache chỉ đọc, không chia sẻ
		// instance protobuf giữa các request đang chạy song song.
		definitions = append(definitions, &agentv1.LocalSubscriptionToolDefinition{
			ToolName:        cached.GetToolName(),
			Description:     cached.GetDescription(),
			InputSchemaJson: cached.GetInputSchemaJson(),
		})
	}
	return definitions
}

// ListLocalSubscriptionTools trả về tool BYOK publish cho client bản mới.
func (service *Service) ListLocalSubscriptionTools(
	ctx context.Context,
	req *connect.Request[agentv1.ListLocalSubscriptionToolsRequest],
) (*connect.Response[agentv1.ListLocalSubscriptionToolsResponse], error) {
	if service == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	tools := service.enabledLocalSubscriptionTools()
	service.debug.LogRuntime(ctx, "", "", "local_subscription_tools_listed", map[string]any{
		"channel":    "local_subscription_tool",
		"tool_count": len(tools),
	})
	return connect.NewResponse(&agentv1.ListLocalSubscriptionToolsResponse{Tools: tools}), nil
}

// CallLocalSubscriptionTool thực thi tool server-side theo conversation_id.
func (service *Service) CallLocalSubscriptionTool(
	ctx context.Context,
	req *connect.Request[agentv1.CallLocalSubscriptionToolRequest],
) (*connect.Response[agentv1.CallLocalSubscriptionToolResponse], error) {
	if service == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	conversationID := strings.TrimSpace(req.Msg.GetConversationId())
	toolName := strings.TrimSpace(req.Msg.GetToolName())
	argsJSON := []byte(req.Msg.GetToolArgsJson())

	tool, found := findLocalSubscriptionTool(toolName)
	if !found {
		// Tool lạ là lỗi giao thức, không phải lỗi nghiệp vụ của model.
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown local subscription tool: %s", toolName))
	}
	if tool.enabled != nil && !tool.enabled(service) {
		// Điều kiện nghiệp vụ: trả kết quả lỗi để model đọc được lý do.
		return connect.NewResponse(&agentv1.CallLocalSubscriptionToolResponse{
			ResultJson: mustLocalSubscriptionResultJSON("Project memory is disabled."),
			IsError:    true,
		}), nil
	}

	var stream *ActiveStream
	if service.broker != nil && conversationID != "" {
		stream, _ = service.broker.ActiveStreamForConversation(conversationID)
	}
	resultText := tool.run(service, stream, argsJSON)
	isError := isMemoryToolFailureResult(resultText)

	service.debug.LogRuntime(ctx, "", conversationID, "local_subscription_tool_called", map[string]any{
		"channel":     "local_subscription_tool",
		"tool_name":   toolName,
		"resolved":    stream != nil,
		"result_len":  len(resultText),
		"is_error":    isError,
		"args_bytes":  len(argsJSON),
		"result_head": headForLocalSubscriptionLog(resultText),
	})
	return connect.NewResponse(&agentv1.CallLocalSubscriptionToolResponse{
		ResultJson: mustLocalSubscriptionResultJSON(resultText),
		IsError:    isError,
	}), nil
}

// mustLocalSubscriptionResultJSON bọc text thành JSON object theo đúng tên field
// result_json; text của memory tool vốn là plain text nên không thể trả thô.
// ASSUMPTION (cần kiểm chứng khi sync proto (a)): client render result_json như
// JSON, nên ta luôn trả object {"text": ...} thay vì chuỗi không hợp lệ.
func mustLocalSubscriptionResultJSON(text string) string {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return `{"text":""}`
	}
	return string(payload)
}

// headForLocalSubscriptionLog cắt bớt kết quả khi ghi debug log.
func headForLocalSubscriptionLog(text string) string {
	trimmed := strings.TrimSpace(text)
	const limit = 160
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "..."
}
