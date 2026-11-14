package forwarder

import (
	"context"
	"testing"

	"cursor/internal/backend/server/config"
)

func TestAgyRoutingPrefixAndResolver(t *testing.T) {
	service := &Service{}

	// 1. Kiểm tra prefix agy/ và antigravity/
	if !service.isAgyRequest("agy/gemini-2.5-pro") {
		t.Errorf("kỳ vọng isAgyRequest('agy/gemini-2.5-pro') == true")
	}
	if !service.isAgyRequest("antigravity/claude-3-7-sonnet") {
		t.Errorf("kỳ vọng isAgyRequest('antigravity/claude-3-7-sonnet') == true")
	}
	if service.isAgyRequest("claude-3-7-sonnet") {
		t.Errorf("kỳ vọng isAgyRequest('claude-3-7-sonnet') == false khi chưa có resolver")
	}

	// 2. Kiểm tra hàm tiện ích config.IsAgyModelOrProvider
	if !config.IsAgyModelOrProvider("agy", "anything") {
		t.Errorf("kỳ vọng IsAgyModelOrProvider('agy', 'anything') == true")
	}
	if !config.IsAgyModelOrProvider("openai", "agy/claude-3-7") {
		t.Errorf("kỳ vọng IsAgyModelOrProvider('openai', 'agy/claude-3-7') == true")
	}
	if config.IsAgyModelOrProvider("openai", "gpt-4o") {
		t.Errorf("kỳ vọng IsAgyModelOrProvider('openai', 'gpt-4o') == false")
	}

	// 3. Kiểm tra SelectChannelForModel với prefix
	manager := &config.Manager{}
	channel, err := manager.SelectChannelForModel(context.Background(), "agy/gemini-2.5-pro")
	if err != nil {
		t.Fatalf("SelectChannelForModel failed: %v", err)
	}
	if channel.Provider != config.ProviderTypeAgy {
		t.Errorf("Kỳ vọng provider = agy, thực tế = %s", channel.Provider)
	}
	if channel.Model != "gemini-2.5-pro" {
		t.Errorf("Kỳ vọng model = gemini-2.5-pro, thực tế = %s", channel.Model)
	}
}