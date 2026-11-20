// mcp_schema_heal.go vá khe hở của cơ chế lazy-load schema MCP: khi MCP server trả lỗi
// tham số (validation), tool_result gửi lại model được bổ sung schema đầy đủ đọc từ file
// definition trên đĩa, biến vòng retry "đoán mò" thành vòng retry có căn cứ.
package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

// mcpHealSchemaBudgetBytes giới hạn dung lượng schema tiêm vào tool_result mỗi lần heal.
const mcpHealSchemaBudgetBytes = 4096

// mcpHealErrorMarkers là các mẫu thông báo lỗi tham số phổ biến của MCP server/client.
var mcpHealErrorMarkers = []string{
	"invalid parameters",
	"invalid parameter",
	"is required",
	"missing required",
	"required parameter",
	"unknown parameter",
	"unknown argument",
	"unexpected parameter",
	"unexpected argument",
	"validation error",
	"failed to parse",
}

func looksLikeMCPParameterError(text string) bool {
	lowered := strings.ToLower(strings.TrimSpace(text))
	if lowered == "" {
		return false
	}
	for _, marker := range mcpHealErrorMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// enrichMCPParameterError bổ sung input schema đầy đủ của tool vào payload lỗi tham số.
// Giữ nguyên payload khi không đủ căn cứ (không phải lỗi validation, không tìm được definition path).
func (service *Service) enrichMCPParameterError(stream *ActiveStream, pending runtimecore.PendingExec, payload string) string {
	if !looksLikeMCPParameterError(payload) {
		return payload
	}
	toolName, definitionPath := resolveMCPToolDefinitionPath(stream, pending)
	if definitionPath == "" {
		return payload
	}
	if schema := readMCPToolFullSchema(definitionPath); schema != "" {
		return payload + "\nInput schema for tool \"" + toolName + "\":\n" + schema + "\nCorrect the arguments against this schema and retry."
	}
	return payload + "\nFull parameter schema: Read " + definitionPath + " before retrying."
}

func resolveMCPToolDefinitionPath(stream *ActiveStream, pending runtimecore.PendingExec) (string, string) {
	decoded, err := runtimecore.DecodeMCPToolPayload(pending.ArgsJSON)
	if err != nil {
		return "", ""
	}
	serverIdentifier := firstNonEmpty(decoded.Server, decoded.ProviderIdentifier)
	toolName := strings.TrimSpace(decoded.ToolName)
	name := strings.TrimSpace(decoded.Name)
	if toolName == "" {
		toolName = runtimecore.InferMCPToolName(serverIdentifier, name)
	}

	candidates := make([]string, 0, 4)
	if serverIdentifier != "" && toolName != "" {
		candidates = append(candidates, forwarderCanonicalMCPToolLookupName(serverIdentifier, toolName))
	}
	candidates = append(candidates, toolName)
	if serverIdentifier != "" && name != "" {
		candidates = append(candidates, forwarderCanonicalMCPToolLookupName(serverIdentifier, name))
	}
	if name != "" {
		candidates = append(candidates, name)
	}

	if stream == nil {
		return toolName, ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for _, key := range candidates {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if path := strings.TrimSpace(stream.MCPToolDefinitionPaths[key]); path != "" {
			return toolName, path
		}
	}
	return toolName, ""
}

type mcpHealSchemaCacheEntry struct {
	modTime time.Time
	schema  string
}

var (
	mcpHealSchemaCacheMu sync.RWMutex
	mcpHealSchemaCache   = make(map[string]mcpHealSchemaCacheEntry)
)

// readMCPToolFullSchema trích field "arguments" (kèm mô tả từng tham số) từ file definition,
// nén còn một dòng JSON và cắt theo ngân sách byte. Kết quả cache theo modTime của file.
func readMCPToolFullSchema(defPath string) string {
	cleanPath := filepath.Clean(defPath)
	info, err := os.Stat(cleanPath)
	if err != nil || info.IsDir() {
		return ""
	}

	mcpHealSchemaCacheMu.RLock()
	cached, ok := mcpHealSchemaCache[cleanPath]
	mcpHealSchemaCacheMu.RUnlock()
	if ok && cached.modTime.Equal(info.ModTime()) {
		return cached.schema
	}

	schema := ""
	if data, err := os.ReadFile(cleanPath); err == nil {
		var doc struct {
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(data, &doc); err == nil && len(doc.Arguments) > 0 {
			schema = compactJSONPayload(doc.Arguments, mcpHealSchemaBudgetBytes)
		}
	}

	mcpHealSchemaCacheMu.Lock()
	mcpHealSchemaCache[cleanPath] = mcpHealSchemaCacheEntry{modTime: info.ModTime(), schema: schema}
	mcpHealSchemaCacheMu.Unlock()
	return schema
}

func compactJSONPayload(raw json.RawMessage, budget int) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		value = strings.TrimSpace(string(raw))
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	if budget > 0 && len(encoded) > budget {
		cut := budget
		for cut > 0 && !isUTF8Boundary(encoded, cut) {
			cut--
		}
		encoded = append(encoded[:cut], []byte("...(truncated)")...)
	}
	return string(encoded)
}

func isUTF8Boundary(data []byte, index int) bool {
	if index <= 0 {
		return true
	}
	if index >= len(data) {
		return false
	}
	return data[index]&0xC0 != 0x80
}
