package forwarder

// Hồi quy cho lỗ hổng đầu vào: client hiện đại tách rules/skills/subagents/MCP descriptors
// sang pre_fetched_blobs và chỉ gửi blob id trong request_context_parts. Trước khi có
// resolveRequestContextPartReferences, forwarder chỉ đọc dynamic_context nên toàn bộ MCP
// descriptors bị bỏ ngay từ lúc nhận message — model không thấy tool MCP nào.

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

func partsTestBlob(part proto.Message) *agentv1.PreFetchedBlob {
	value, err := proto.Marshal(part)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(value)
	return &agentv1.PreFetchedBlob{Id: digest[:], Value: value}
}

func partsTestMCPDescriptors() *agentv1.McpFileSystemOptions {
	return &agentv1.McpFileSystemOptions{
		Enabled:             true,
		WorkspaceProjectDir: "C:/proj",
		McpDescriptors: []*agentv1.McpDescriptor{{
			ServerIdentifier: "user-gridex",
			ServerName:       "gridex",
			FolderPath:       stringPtr("C:/proj/mcps/user-gridex"),
			Tools:            []*agentv1.McpToolDescriptor{{ToolName: "query"}},
		}},
	}
}

func partsTestRunRequest(parts *agentv1.RequestContextPartReferences, blobs []*agentv1.PreFetchedBlob, topLevelMCP *agentv1.McpFileSystemOptions) *agentv1.AgentClientMessage {
	return &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				Action: &agentv1.ConversationAction{
					Action: &agentv1.ConversationAction_UserMessageAction{
						UserMessageAction: &agentv1.UserMessageAction{
							UserMessage: &agentv1.UserMessage{MessageId: "m-1", Text: "chay dry run voi gridex mcp"},
						},
					},
					RequestContextParts: parts,
				},
				PreFetchedBlobs:      blobs,
				McpFileSystemOptions: topLevelMCP,
			},
		},
	}
}

func TestExtractRequestContextRestoresMCPFromPrefetchedBlob(t *testing.T) {
	mcpsBlob := partsTestBlob(&agentv1.RequestContextMcpsPart{McpFileSystemOptions: partsTestMCPDescriptors()})
	rulesBlob := partsTestBlob(&agentv1.RequestContextRulesPart{
		Rules: []*agentv1.CursorRule{{FullPath: "rule.mdc", Content: "Always be concise."}},
	})
	message := partsTestRunRequest(&agentv1.RequestContextPartReferences{
		DynamicContext: &agentv1.RequestContext{
			Env: &agentv1.RequestContextEnv{ProjectFolder: "C:/proj"},
		},
		McpsBlobId:  mcpsBlob.GetId(),
		RulesBlobId: rulesBlob.GetId(),
	}, []*agentv1.PreFetchedBlob{mcpsBlob, rulesBlob}, nil)

	cache := &prefetchedBlobCache{}
	cache.remember(message.GetRunRequest().GetPreFetchedBlobs())
	extracted := extractRequestContext(message, cache)

	if extracted == nil {
		t.Fatal("expected request context")
	}
	descriptors := extracted.GetMcpFileSystemOptions().GetMcpDescriptors()
	if len(descriptors) != 1 || descriptors[0].GetServerIdentifier() != "user-gridex" {
		t.Fatalf("MCP descriptors not restored from blob: %#v", descriptors)
	}
	if len(extracted.GetRules()) != 1 || extracted.GetRules()[0].GetFullPath() != "rule.mdc" {
		t.Fatalf("rules not restored from blob: %#v", extracted.GetRules())
	}
	if extracted.GetEnv().GetProjectFolder() != "C:/proj" {
		t.Fatalf("dynamic context lost during blob merge: %#v", extracted.GetEnv())
	}
}

// Payload động client gửi inline phải thắng blob tham chiếu (không ghi đè dữ liệu mới nhất).
func TestExtractRequestContextInlineMCPWinsOverBlob(t *testing.T) {
	mcpsBlob := partsTestBlob(&agentv1.RequestContextMcpsPart{McpFileSystemOptions: partsTestMCPDescriptors()})
	inline := &agentv1.McpFileSystemOptions{
		Enabled:             true,
		WorkspaceProjectDir: "C:/proj",
		McpDescriptors: []*agentv1.McpDescriptor{{
			ServerIdentifier: "user-traceui",
			ServerName:       "traceui",
			Tools:            []*agentv1.McpToolDescriptor{{ToolName: "trace"}},
		}},
	}
	message := partsTestRunRequest(&agentv1.RequestContextPartReferences{
		DynamicContext: &agentv1.RequestContext{McpFileSystemOptions: inline},
		McpsBlobId:     mcpsBlob.GetId(),
	}, []*agentv1.PreFetchedBlob{mcpsBlob}, nil)

	cache := &prefetchedBlobCache{}
	cache.remember(message.GetRunRequest().GetPreFetchedBlobs())
	extracted := extractRequestContext(message, cache)

	descriptors := extracted.GetMcpFileSystemOptions().GetMcpDescriptors()
	if len(descriptors) != 1 || descriptors[0].GetServerIdentifier() != "user-traceui" {
		t.Fatalf("blob overwrote inline MCP options: %#v", descriptors)
	}
}

// conversation_action không mang pre_fetched_blobs nhưng vẫn tham chiếu blob id đã gửi
// ở run_request trước đó, nên cache phải sống lintas message.
func TestConversationActionResolvesMCPFromCachedBlob(t *testing.T) {
	mcpsBlob := partsTestBlob(&agentv1.RequestContextMcpsPart{McpFileSystemOptions: partsTestMCPDescriptors()})
	cache := &prefetchedBlobCache{}
	cache.remember([]*agentv1.PreFetchedBlob{mcpsBlob})

	action := &agentv1.ConversationAction{
		Action: &agentv1.ConversationAction_UserMessageAction{
			UserMessageAction: &agentv1.UserMessageAction{
				UserMessage: &agentv1.UserMessage{MessageId: "m-2", Text: "follow up"},
			},
		},
		RequestContextParts: &agentv1.RequestContextPartReferences{
			DynamicContext: &agentv1.RequestContext{
				Env: &agentv1.RequestContextEnv{ProjectFolder: "C:/proj"},
			},
			McpsBlobId: mcpsBlob.GetId(),
		},
	}

	extracted := extractConversationActionRequestContext(action, cache)
	if len(extracted.GetMcpFileSystemOptions().GetMcpDescriptors()) != 1 {
		t.Fatalf("MCP descriptors not restored in conversation_action path: %#v", extracted)
	}
}

// AgentRunRequest còn mang mcp_file_system_options ở gốc, ngoài action — nguồn thứ ba từng bị bỏ.
func TestExtractRequestContextFillsTopLevelRunRequestMCP(t *testing.T) {
	message := partsTestRunRequest(nil, nil, partsTestMCPDescriptors())

	extracted := extractRequestContext(message, &prefetchedBlobCache{})
	if extracted == nil {
		t.Fatal("expected request context from top-level run request options")
	}
	if len(extracted.GetMcpFileSystemOptions().GetMcpDescriptors()) != 1 {
		t.Fatalf("top-level MCP options not applied: %#v", extracted)
	}
}

// Merge phải clone, không mutate AgentClientMessage gốc, để debug record giữ đúng payload wire.
func TestResolveRequestContextPartsDoesNotMutateMessage(t *testing.T) {
	mcpsBlob := partsTestBlob(&agentv1.RequestContextMcpsPart{McpFileSystemOptions: partsTestMCPDescriptors()})
	dynamic := &agentv1.RequestContext{Env: &agentv1.RequestContextEnv{ProjectFolder: "C:/proj"}}
	message := partsTestRunRequest(&agentv1.RequestContextPartReferences{
		DynamicContext: dynamic,
		McpsBlobId:     mcpsBlob.GetId(),
	}, []*agentv1.PreFetchedBlob{mcpsBlob}, nil)

	cache := &prefetchedBlobCache{}
	cache.remember(message.GetRunRequest().GetPreFetchedBlobs())
	extractRequestContext(message, cache)

	if dynamic.GetMcpFileSystemOptions() != nil {
		t.Fatal("blob merge mutated the original dynamic context inside the client message")
	}
}

func TestPrefetchedBlobCacheEvictsOldestBlobs(t *testing.T) {
	cache := &prefetchedBlobCache{}
	for index := 0; index < prefetchedBlobCacheMaxItems+5; index++ {
		cache.remember([]*agentv1.PreFetchedBlob{partsTestBlob(&agentv1.RequestContextSubagentsPart{
			CustomSubagents: []*agentv1.CustomSubagent{{Name: fmt.Sprintf("sub-%d", index)}},
		})})
	}
	cache.mu.Lock()
	itemCount := len(cache.byID)
	orderLength := len(cache.order)
	cache.mu.Unlock()
	if itemCount != prefetchedBlobCacheMaxItems || orderLength != prefetchedBlobCacheMaxItems {
		t.Fatalf("cache not bounded: items=%d order=%d cap=%d", itemCount, orderLength, prefetchedBlobCacheMaxItems)
	}
	// Blob cũ nhất đã bị đẩy ra, blob mới nhất vẫn resolve được.
	oldest := partsTestBlob(&agentv1.RequestContextSubagentsPart{
		CustomSubagents: []*agentv1.CustomSubagent{{Name: "sub-0"}},
	})
	if _, ok := cache.resolve(oldest.GetId()); ok {
		t.Fatal("oldest blob still cached after eviction")
	}
	newest := partsTestBlob(&agentv1.RequestContextSubagentsPart{
		CustomSubagents: []*agentv1.CustomSubagent{{Name: fmt.Sprintf("sub-%d", prefetchedBlobCacheMaxItems+4)}},
	})
	if _, ok := cache.resolve(newest.GetId()); !ok {
		t.Fatal("newest blob missing from cache")
	}
}
