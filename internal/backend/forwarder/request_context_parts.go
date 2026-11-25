package forwarder

// Client Cursor hiện đại tách các phần lớn của request context (rules, skills, subagents,
// MCP descriptors) ra ngoài payload động: action chỉ mang `request_context_parts` gồm các
// blob id, nội dung gửi một lần trong `pre_fetched_blobs`. Nếu chỉ đọc dynamic_context thì
// toàn bộ MCP descriptors bị rơi ngay từ đầu vào — model không thấy tool MCP nào.
// File này resolve các blob id đó và hợp nhất vào RequestContext trước khi lưu/dispatch.

import (
	"sync"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

const (
	prefetchedBlobCacheMaxItems = 256
	prefetchedBlobCacheMaxBytes = 8 << 20
)

// prefetchedBlobCache giữ blob mà client gửi lên ở run_request/prewarm_request, vì
// conversation_action không mang pre_fetched_blobs nhưng vẫn tham chiếu cùng blob id.
// Zero value dùng được ngay (lazy init) nên các test khởi tạo Service{} không cần chỉnh.
type prefetchedBlobCache struct {
	mu    sync.Mutex
	order []string
	byID  map[string][]byte
	total int
}

func (cache *prefetchedBlobCache) remember(items []*agentv1.PreFetchedBlob) {
	if cache == nil || len(items) == 0 {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.byID == nil {
		cache.byID = make(map[string][]byte, len(items))
	}
	for _, item := range items {
		id := item.GetId()
		value := item.GetValue()
		if len(id) == 0 || len(value) == 0 {
			continue
		}
		key := string(id)
		if _, exists := cache.byID[key]; exists {
			continue
		}
		cache.byID[key] = append([]byte(nil), value...)
		cache.order = append(cache.order, key)
		cache.total += len(value)
	}
	cache.evictLocked()
}

func (cache *prefetchedBlobCache) evictLocked() {
	for len(cache.order) > 0 && (len(cache.order) > prefetchedBlobCacheMaxItems || cache.total > prefetchedBlobCacheMaxBytes) {
		key := cache.order[0]
		cache.order = cache.order[1:]
		cache.total -= len(cache.byID[key])
		delete(cache.byID, key)
	}
}

func (cache *prefetchedBlobCache) resolve(id []byte) ([]byte, bool) {
	if cache == nil || len(id) == 0 {
		return nil, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.byID[string(id)]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), value...), true
}

// resolveRequestContextPartReferences hợp nhất các phần request context nằm trong blob vào
// context sống. Chỉ bù vào field còn trống để payload động client gửi trực tiếp luôn thắng.
func resolveRequestContextPartReferences(parts *agentv1.RequestContextPartReferences, cache *prefetchedBlobCache, base *agentv1.RequestContext) *agentv1.RequestContext {
	if parts == nil || cache == nil || !partsReferenceBlobs(parts) {
		return base
	}
	merged := cloneRequestContext(base)

	if rules := decodeRequestContextRulesPart(cache, parts.GetRulesBlobId()); rules != nil {
		if len(merged.GetRules()) == 0 && len(rules.GetRules()) > 0 {
			merged.Rules = rules.GetRules()
		}
		if len(merged.GetNonFileRules()) == 0 && len(rules.GetNonFileRules()) > 0 {
			merged.NonFileRules = rules.GetNonFileRules()
		}
		if merged.GetCloudRule() == "" && rules.GetCloudRule() != "" {
			merged.CloudRule = stringPtr(rules.GetCloudRule())
		}
	}

	if skills := decodeRequestContextSkillsPart(cache, parts.GetSkillsBlobId()); skills != nil {
		if merged.GetSkillOptions() == nil && skills.GetSkillOptions() != nil {
			merged.SkillOptions = skills.GetSkillOptions()
		}
		if len(merged.GetAgentSkills()) == 0 && len(skills.GetAgentSkills()) > 0 {
			merged.AgentSkills = skills.GetAgentSkills()
		}
	}

	if subagents := decodeRequestContextSubagentsPart(cache, parts.GetSubagentsBlobId()); subagents != nil {
		if len(merged.GetCustomSubagents()) == 0 && len(subagents.GetCustomSubagents()) > 0 {
			merged.CustomSubagents = subagents.GetCustomSubagents()
		}
	}

	if mcps := decodeRequestContextMcpsPart(cache, parts.GetMcpsBlobId()); mcps != nil {
		if merged.GetMcpFileSystemOptions() == nil && mcps.GetMcpFileSystemOptions() != nil {
			merged.McpFileSystemOptions = mcps.GetMcpFileSystemOptions()
		}
		if merged.GetMcpMetaToolOptions() == nil && mcps.GetMcpMetaToolOptions() != nil {
			merged.McpMetaToolOptions = mcps.GetMcpMetaToolOptions()
		}
		if len(merged.GetTools()) == 0 && len(mcps.GetTools()) > 0 {
			merged.Tools = mcps.GetTools()
		}
		if len(merged.GetMcpInstructions()) == 0 && len(mcps.GetMcpInstructions()) > 0 {
			merged.McpInstructions = mcps.GetMcpInstructions()
		}
	}

	return merged
}

func decodeRequestContextRulesPart(cache *prefetchedBlobCache, id []byte) *agentv1.RequestContextRulesPart {
	part := &agentv1.RequestContextRulesPart{}
	if !resolvePartBlob(cache, id, part) {
		return nil
	}
	return part
}

func decodeRequestContextSkillsPart(cache *prefetchedBlobCache, id []byte) *agentv1.RequestContextSkillsPart {
	part := &agentv1.RequestContextSkillsPart{}
	if !resolvePartBlob(cache, id, part) {
		return nil
	}
	return part
}

func decodeRequestContextSubagentsPart(cache *prefetchedBlobCache, id []byte) *agentv1.RequestContextSubagentsPart {
	part := &agentv1.RequestContextSubagentsPart{}
	if !resolvePartBlob(cache, id, part) {
		return nil
	}
	return part
}

func decodeRequestContextMcpsPart(cache *prefetchedBlobCache, id []byte) *agentv1.RequestContextMcpsPart {
	part := &agentv1.RequestContextMcpsPart{}
	if !resolvePartBlob(cache, id, part) {
		return nil
	}
	return part
}

// resolvePartBlob đọc blob và giải mã vào message đích. Blob id là content-addressed, nên
// id lạ/không còn trong cache hoặc payload hỏng đều im lặng bỏ qua (giữ nguyên context sống).
func resolvePartBlob(cache *prefetchedBlobCache, id []byte, message proto.Message) bool {
	if cache == nil || len(id) == 0 || message == nil {
		return false
	}
	raw, ok := cache.resolve(id)
	if !ok {
		return false
	}
	return proto.Unmarshal(raw, message) == nil
}

// partsReferenceBlobs báo hiệu action thật sự tách nội dung sang blob; khi client gửi toàn
// bộ inline thì đường này không clone/giải mã gì cả.
func partsReferenceBlobs(parts *agentv1.RequestContextPartReferences) bool {
	return len(parts.GetRulesBlobId()) > 0 ||
		len(parts.GetSkillsBlobId()) > 0 ||
		len(parts.GetSubagentsBlobId()) > 0 ||
		len(parts.GetMcpsBlobId()) > 0
}

// cloneRequestContext trả về bản sao để không mutate AgentClientMessage gốc — debug recorder
// cần ghi lại đúng những gì client gửi lên, không phải context sau khi forwarder tự bù.
func cloneRequestContext(requestContext *agentv1.RequestContext) *agentv1.RequestContext {
	if requestContext == nil {
		return &agentv1.RequestContext{}
	}
	cloned, ok := proto.Clone(requestContext).(*agentv1.RequestContext)
	if !ok || cloned == nil {
		return &agentv1.RequestContext{}
	}
	return cloned
}

// applyRunRequestContextDefaults bù MCP/skill options mà client đặt ở gốc AgentRunRequest
// (ngoài action.request_context) — nguồn thứ ba mà forwarder từng bỏ qua.
func applyRunRequestContextDefaults(rc *agentv1.RequestContext, runRequest *agentv1.AgentRunRequest) *agentv1.RequestContext {
	if runRequest == nil {
		return rc
	}
	if runRequest.GetMcpFileSystemOptions() == nil && runRequest.GetSkillOptions() == nil {
		return rc
	}
	if rc == nil {
		rc = &agentv1.RequestContext{}
	} else {
		rc = cloneRequestContext(rc)
	}
	if rc.GetMcpFileSystemOptions() == nil {
		rc.McpFileSystemOptions = runRequest.GetMcpFileSystemOptions()
	}
	if rc.GetSkillOptions() == nil {
		rc.SkillOptions = runRequest.GetSkillOptions()
	}
	return rc
}
