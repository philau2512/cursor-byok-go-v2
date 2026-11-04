package forwarder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"cursor/gen/agentv1"
)

func TestUpdateConversationTitleAndFiles(t *testing.T) {
	tempDir := t.TempDir()
	store := NewConversationFileStore(tempDir)

	conversationID := "test-convo-title-123"
	_, err := store.CreateConversation(conversationID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", "")
	if err != nil {
		t.Fatalf("CreateConversation failed: %v", err)
	}

	testTitle := "Đổi tên cuộc hội thoại mới"
	if err := store.UpdateConversationTitle(conversationID, testTitle, false); err != nil {
		t.Fatalf("UpdateConversationTitle failed: %v", err)
	}

	// 1. Verify state.json
	convo, err := store.LoadConversation(conversationID)
	if err != nil {
		t.Fatalf("LoadConversation failed: %v", err)
	}
	if convo.Title != testTitle {
		t.Errorf("expected Title %q, got %q", testTitle, convo.Title)
	}
	if convo.Name != testTitle {
		t.Errorf("expected Name %q, got %q", testTitle, convo.Name)
	}

	// 2. Verify custom-title.json
	hyphenPath := filepath.Join(tempDir, conversationID, "custom-title.json")
	hyphenBytes, err := os.ReadFile(hyphenPath)
	if err != nil {
		t.Fatalf("read custom-title.json failed: %v", err)
	}
	var hyphenPayload customTitleFile
	if err := json.Unmarshal(hyphenBytes, &hyphenPayload); err != nil {
		t.Fatalf("unmarshal custom-title.json failed: %v", err)
	}
	if hyphenPayload.CustomTitle != testTitle {
		t.Errorf("expected CustomTitle %q, got %q", testTitle, hyphenPayload.CustomTitle)
	}

	// 3. Verify custom_title.json
	underscorePath := filepath.Join(tempDir, conversationID, "custom_title.json")
	underscoreBytes, err := os.ReadFile(underscorePath)
	if err != nil {
		t.Fatalf("read custom_title.json failed: %v", err)
	}
	var underscorePayload customTitleFile
	if err := json.Unmarshal(underscoreBytes, &underscorePayload); err != nil {
		t.Fatalf("unmarshal custom_title.json failed: %v", err)
	}
	if underscorePayload.CustomTitle != testTitle {
		t.Errorf("expected CustomTitle %q, got %q", testTitle, underscorePayload.CustomTitle)
	}

	// 4. Test onlyIfEmpty = true (should not overwrite existing title)
	if err := store.UpdateConversationTitle(conversationID, "New Ignored Title", true); err != nil {
		t.Fatalf("UpdateConversationTitle with onlyIfEmpty failed: %v", err)
	}
	convoAfter, _ := store.LoadConversation(conversationID)
	if convoAfter.Title != testTitle {
		t.Errorf("expected Title to stay %q, got %q", testTitle, convoAfter.Title)
	}

	// 5. Test DeleteConversation
	if err := store.DeleteConversation(conversationID); err != nil {
		t.Fatalf("DeleteConversation failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, conversationID)); !os.IsNotExist(err) {
		t.Errorf("expected conversation directory to be deleted, err=%v", err)
	}
}

func TestServiceUpdateConversationMetadataAndNameAgent(t *testing.T) {
	tempDir := t.TempDir()
	store := NewConversationFileStore(tempDir)
	service := &Service{
		store: store,
	}

	conversationID := "test-rpc-convo-456"
	_, err := store.CreateConversation(conversationID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", "")
	if err != nil {
		t.Fatalf("CreateConversation failed: %v", err)
	}

	// RPC UpdateConversationMetadata
	req := connect.NewRequest(&agentv1.UpdateConversationMetadataRequest{
		ConversationId: conversationID,
		Name:           stringPtr("RPC Renamed Title"),
	})
	resp, err := service.UpdateConversationMetadata(context.Background(), req)
	if err != nil {
		t.Fatalf("UpdateConversationMetadata RPC failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Check files created
	hyphenPath := filepath.Join(tempDir, conversationID, "custom-title.json")
	if _, err := os.Stat(hyphenPath); err != nil {
		t.Errorf("custom-title.json was not created: %v", err)
	}

	// RPC NameAgent
	nameReq := connect.NewRequest(&agentv1.NameAgentRequest{
		UserMessage: "viết pr des cho 2 pr github",
	})
	nameResp, err := service.NameAgent(context.Background(), nameReq)
	if err != nil {
		t.Fatalf("NameAgent RPC failed: %v", err)
	}
	if nameResp.Msg.GetName() != "viết pr des cho 2 pr github" {
		t.Errorf("unexpected name from NameAgent: %q", nameResp.Msg.GetName())
	}
}
