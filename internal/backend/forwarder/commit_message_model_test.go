package forwarder

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
	legacyruntime "cursor/internal/runtime"
)

type fakeCommitModelMemory struct {
	agentHash     string
	explorerHash  string
	commitModel   string
	savedExplorer []string
}

func (f *fakeCommitModelMemory) LastAgentModelHash() string { return f.agentHash }

func (f *fakeCommitModelMemory) SaveLastAgentModelHash(context.Context, string) error { return nil }

func (f *fakeCommitModelMemory) LastExplorerModelHash() string { return f.explorerHash }

func (f *fakeCommitModelMemory) SaveLastExplorerModelHash(_ context.Context, value string) error {
	f.savedExplorer = append(f.savedExplorer, value)
	return nil
}

func (f *fakeCommitModelMemory) CommitMessageModel() string { return f.commitModel }

type fakeCommitResolver struct {
	channels map[string]*legacyruntime.ResolvedChannel
}

func (r *fakeCommitResolver) SelectChannelForModel(_ context.Context, modelID string) (*legacyruntime.ResolvedChannel, error) {
	channel, ok := r.channels[modelID]
	if !ok {
		return nil, errors.New("channel not available")
	}
	return channel, nil
}

func (r *fakeCommitResolver) ProviderStreamIdleTimeout(context.Context) time.Duration {
	return time.Minute
}

func newCommitResolver(entries map[string]string) *fakeCommitResolver {
	channels := make(map[string]*legacyruntime.ResolvedChannel, len(entries))
	for requested, channelID := range entries {
		channels[requested] = &legacyruntime.ResolvedChannel{ID: channelID}
	}
	return &fakeCommitResolver{channels: channels}
}

func TestResolveCommitMessageModelIDChainPriority(t *testing.T) {
	resolver := newCommitResolver(map[string]string{
		"configured-flash": "chan-configured",
		"chan-explorer":    "chan-explorer",
		"chan-agent":       "chan-agent",
		"stale-hash":       "other-channel",
	})

	service := &Service{
		resolver:    resolver,
		modelMemory: &fakeCommitModelMemory{commitModel: "configured-flash", explorerHash: "chan-explorer", agentHash: "chan-agent"},
	}
	if modelID, source, raw := service.resolveCommitMessageModelID(context.Background()); modelID != "chan-configured" || source != "commit_message_config" || raw != "configured-flash" {
		t.Fatalf("config must win: got %q/%q/%q", modelID, source, raw)
	}

	service.modelMemory = &fakeCommitModelMemory{explorerHash: "chan-explorer", agentHash: "chan-agent"}
	if modelID, source, _ := service.resolveCommitMessageModelID(context.Background()); modelID != "chan-explorer" || source != "explorer_override" {
		t.Fatalf("explorer must win over agent: got %q/%q", modelID, source)
	}

	service.modelMemory = &fakeCommitModelMemory{agentHash: "chan-agent"}
	if modelID, source, _ := service.resolveCommitMessageModelID(context.Background()); modelID != "chan-agent" || source != "last_agent_model_hash" {
		t.Fatalf("agent hash must be used when no explorer: got %q/%q", modelID, source)
	}

	service.modelMemory = &fakeCommitModelMemory{}
	if modelID, source, _ := service.resolveCommitMessageModelID(context.Background()); modelID != "" || source != "default_fallback" {
		t.Fatalf("empty memory must fall back to default: got %q/%q", modelID, source)
	}
}

func TestResolveCommitMessageModelIDSkipsInvalidCandidates(t *testing.T) {
	resolver := newCommitResolver(map[string]string{
		"chan-explorer": "chan-explorer",
		"chan-agent":    "chan-agent",
		"stale-hash":    "other-channel",
	})
	service := &Service{
		resolver: resolver,
		// config trỏ model không tồn tại, explorer là hash stale -> phải rơi xuống agent.
		modelMemory: &fakeCommitModelMemory{commitModel: "missing-model", explorerHash: "stale-hash", agentHash: "chan-agent"},
	}
	if modelID, source, _ := service.resolveCommitMessageModelID(context.Background()); modelID != "chan-agent" || source != "last_agent_model_hash" {
		t.Fatalf("invalid candidates must be skipped: got %q/%q", modelID, source)
	}
}

func TestMaybeCaptureExplorerModelHashFromSubagentRun(t *testing.T) {
	resolver := newCommitResolver(map[string]string{"flash-model": "chan-flash"})
	memory := &fakeCommitModelMemory{}
	service := &Service{resolver: resolver, modelMemory: memory}

	service.maybeCaptureExplorerModelHash(context.Background(), "Explorer", "flash-model", nil)
	if len(memory.savedExplorer) != 1 || memory.savedExplorer[0] != "chan-flash" {
		t.Fatalf("expected channel id captured from explorer run, got %v", memory.savedExplorer)
	}
}

func TestMaybeCaptureExplorerModelHashFromOverrides(t *testing.T) {
	resolver := newCommitResolver(map[string]string{"flash-model": "chan-flash"})
	memory := &fakeCommitModelMemory{}
	service := &Service{resolver: resolver, modelMemory: memory}

	overrides := map[string]runtimecore.SubagentModelOverrideSelection{
		"generalPurpose": {SubagentType: "generalPurpose", Selection: "model", ModelID: "other-model"},
		"explore":        {SubagentType: "Explorer", Selection: "model", ModelID: "flash-model"},
	}
	service.maybeCaptureExplorerModelHash(context.Background(), "", "main-model", overrides)
	if len(memory.savedExplorer) != 1 || memory.savedExplorer[0] != "chan-flash" {
		t.Fatalf("expected explore override captured (preferred over generalPurpose), got %v", memory.savedExplorer)
	}
}

func TestMaybeCaptureExplorerModelHashFallsBackToGeneralPurpose(t *testing.T) {
	// Cùng semantics với Task subagent routing: thiếu "explore" thì fallback "generalPurpose".
	resolver := newCommitResolver(map[string]string{"gp-model": "chan-gp"})
	memory := &fakeCommitModelMemory{}
	service := &Service{resolver: resolver, modelMemory: memory}

	overrides := map[string]runtimecore.SubagentModelOverrideSelection{
		"generalPurpose": {SubagentType: "generalPurpose", Selection: "model", ModelID: "gp-model"},
	}
	service.maybeCaptureExplorerModelHash(context.Background(), "", "main-model", overrides)
	if len(memory.savedExplorer) != 1 || memory.savedExplorer[0] != "chan-gp" {
		t.Fatalf("expected generalPurpose fallback captured, got %v", memory.savedExplorer)
	}
}

func TestResolveCommitMessageModelIDPrefersLiveOverrides(t *testing.T) {
	resolver := newCommitResolver(map[string]string{
		"live-flash": "chan-live",
		"chan-stale": "chan-stale",
		"chan-agent": "chan-agent",
	})
	memory := &fakeCommitModelMemory{explorerHash: "chan-stale", agentHash: "chan-agent"}
	service := &Service{resolver: resolver, modelMemory: memory}

	service.rememberSubagentModelOverrides(map[string]runtimecore.SubagentModelOverrideSelection{
		"explore": {SubagentType: "Explorer", Selection: "model", ModelID: "live-flash"},
	})
	if modelID, source, raw := service.resolveCommitMessageModelID(context.Background()); modelID != "chan-live" || source != "explorer_override_live" || raw != "live-flash" {
		t.Fatalf("live overrides must win over persisted hash: got %q/%q/%q", modelID, source, raw)
	}

	// Empty snapshot must not wipe the previous one, and non-explorer keys must not match.
	service.rememberSubagentModelOverrides(nil)
	if modelID, source, _ := service.resolveCommitMessageModelID(context.Background()); modelID != "chan-live" || source != "explorer_override_live" {
		t.Fatalf("empty snapshot must not clear live overrides: got %q/%q", modelID, source)
	}
}

func TestMaybeCaptureExplorerModelHashIgnoresNonExplorerRuns(t *testing.T) {
	resolver := newCommitResolver(map[string]string{"main-model": "chan-main"})
	memory := &fakeCommitModelMemory{}
	service := &Service{resolver: resolver, modelMemory: memory}

	service.maybeCaptureExplorerModelHash(context.Background(), "", "main-model", nil)
	service.maybeCaptureExplorerModelHash(context.Background(), "browser-use", "main-model", map[string]runtimecore.SubagentModelOverrideSelection{
		"browser-use": {SubagentType: "browser-use", Selection: "model", ModelID: "main-model"},
	})
	if len(memory.savedExplorer) != 0 {
		t.Fatalf("non-explorer sources must not be captured, got %v", memory.savedExplorer)
	}
}

func TestMaybeCaptureExplorerModelHashSkipsUnresolvableModel(t *testing.T) {
	resolver := newCommitResolver(map[string]string{})
	memory := &fakeCommitModelMemory{}
	service := &Service{resolver: resolver, modelMemory: memory}

	service.maybeCaptureExplorerModelHash(context.Background(), "explore", "missing-model", nil)
	if len(memory.savedExplorer) != 0 {
		t.Fatalf("unresolvable model must be skipped, got %v", memory.savedExplorer)
	}
}
