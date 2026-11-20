package forwarder

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	"cursor/internal/memory"
)

type memoryConfigStub struct {
	enabled     bool
	autoCapture bool
	overviewMax int
}

func (stub *memoryConfigStub) MemoryEnabled() bool         { return stub.enabled }
func (stub *memoryConfigStub) MemoryAutoCapture() bool     { return stub.autoCapture }
func (stub *memoryConfigStub) MemoryOverviewMaxBytes() int { return stub.overviewMax }

func newMemoryTestSetup(t *testing.T) (*Service, *ActiveStream, *memory.Store) {
	t.Helper()
	store := memory.NewStore(t.TempDir())
	service := &Service{
		memoryStore:  store,
		memoryConfig: &memoryConfigStub{enabled: true, autoCapture: true, overviewMax: 4096},
	}
	stream := &ActiveStream{WorkspacePaths: []string{`d:\proj`}}
	return service, stream, store
}

func mustArgsJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSaveMemorySecretGuard(t *testing.T) {
	if !containsMemorySecret("api_key: sk-abcdef123456") {
		t.Fatal("must detect api key form")
	}
	if !containsMemorySecret("password = Sup3rS3cretValue") {
		t.Fatal("must detect password form")
	}
	if !containsMemorySecret("-----BEGIN RSA PRIVATE KEY-----\nMIIEow") {
		t.Fatal("must detect pem")
	}
	if containsMemorySecret("The build uses gradlew.bat compileJava -q") {
		t.Fatal("false positive on build command")
	}
	if containsMemorySecret("token bucket rate limiting is configured in config.yaml") {
		t.Fatal("false positive: word token without assignment must pass")
	}
}

func TestSaveMemoryThenSearchRoundTrip(t *testing.T) {
	service, stream, store := newMemoryTestSetup(t)
	args := saveMemoryArgs{
		Title:         "Gradle compile command",
		Category:      string(memory.CategoryBuildConfig),
		Content:       "From repo root verify backend compiles with ./gradlew.bat compileJava -q.",
		Keywords:      []string{"gradle", "compileJava"},
		UsageScenario: []string{"When verifying a Java backend change."},
		Origin:        "user",
	}
	result := service.saveMemoryResult(stream, mustArgsJSON(t, args))
	if !strings.HasPrefix(result, "Saved memory: Gradle compile command") {
		t.Fatalf("unexpected result: %q", result)
	}
	expectedAbs := filepath.Join(store.Root(), "projects", "d-proj", string(memory.CategoryBuildConfig), "gradle-compile-command.md")
	if !strings.HasSuffix(result, expectedAbs) {
		t.Fatalf("save result must end with absolute path %q, got %q", expectedAbs, result)
	}
	entries, _, err := store.List("d-proj")
	if err != nil || len(entries) != 1 || entries[0].Origin != memory.OriginUser {
		t.Fatalf("store state wrong: %v %+v", err, entries)
	}

	searchArgs := mustArgsJSON(t, searchMemoryArgs{Query: "gradle compile"})
	out := service.searchMemoryResult(stream, searchArgs)
	if !strings.Contains(out, "Gradle compile command") || !strings.Contains(out, "gradlew.bat") {
		t.Fatalf("search output missing content:\n%s", out)
	}
	if !strings.Contains(out, "file: "+filepath.Join(store.Root(), "projects")+"/") &&
		!strings.Contains(out, "file: "+filepath.Join(store.Root(), "projects")+"\\") {
		t.Fatalf("search output must show absolute file path:\n%s", out)
	}
	if !strings.Contains(out, "origin: user") {
		t.Fatalf("search output missing audit info:\n%s", out)
	}
}

func TestSaveMemoryRejectsAutoOrigin(t *testing.T) {
	service, stream, _ := newMemoryTestSetup(t)
	args := saveMemoryArgs{Title: "Sneaky auto entry", Category: string(memory.CategoryPitfall),
		Content: "model should not forge auto", Origin: "auto"}
	result := service.saveMemoryResult(stream, mustArgsJSON(t, args))
	if !strings.Contains(result, "origin must be \"user\" or \"model\"") {
		t.Fatalf("auto origin must be rejected, got %q", result)
	}
}

func TestSaveMemoryValidatesCategoryAndReports(t *testing.T) {
	service, stream, _ := newMemoryTestSetup(t)
	args := saveMemoryArgs{Title: "Category mistake", Category: "bogus_category", Content: "x", Origin: "model"}
	result := service.saveMemoryResult(stream, mustArgsJSON(t, args))
	if !strings.HasPrefix(result, "SaveMemory failed:") || !strings.Contains(result, "invalid category") {
		t.Fatalf("bad category must surface, got %q", result)
	}
}

func TestMemoryToolsNoWorkspaceAndDisabled(t *testing.T) {
	service, _, _ := newMemoryTestSetup(t)
	noPath := &ActiveStream{}
	if out := service.saveMemoryResult(noPath, mustArgsJSON(t, saveMemoryArgs{
		Title: "Orphan entry", Category: string(memory.CategoryPitfall), Content: "body text", Origin: "model",
	})); !strings.Contains(out, "no workspace path") {
		t.Fatalf("missing workspace must be reported: %q", out)
	}

	disabledService := &Service{
		memoryStore:  service.memoryStore,
		memoryConfig: &memoryConfigStub{enabled: false},
	}
	stream := &ActiveStream{WorkspacePaths: []string{`d:\proj`}}
	if out := disabledService.saveMemoryResult(stream, mustArgsJSON(t, saveMemoryArgs{
		Title: "Blocked save", Category: string(memory.CategoryPitfall), Content: "body", Origin: "model",
	})); out != "Project memory is disabled." {
		t.Fatalf("disabled save must short-circuit: %q", out)
	}
	if out := disabledService.searchMemoryResult(stream, mustArgsJSON(t, searchMemoryArgs{Query: "any"})); out != "Project memory is disabled." {
		t.Fatalf("disabled search must short-circuit: %q", out)
	}
}

func TestSearchMemoryEmptyQueryReturnsOverview(t *testing.T) {
	service, stream, _ := newMemoryTestSetup(t)
	if out := service.searchMemoryResult(stream, mustArgsJSON(t, searchMemoryArgs{})); !strings.Contains(out, "No project memories recorded yet") {
		t.Fatalf("empty store overview: %q", out)
	}
	if out := service.saveMemoryResult(stream, mustArgsJSON(t, saveMemoryArgs{
		Title: "Prettier config", Category: string(memory.CategoryCodeSpec),
		Keywords: []string{"prettier"}, Content: "Use prettier with 100 col width.", Origin: "model",
	})); !strings.HasPrefix(out, "Saved memory") {
		t.Fatalf("save failed: %q", out)
	}
	overview := service.searchMemoryResult(stream, mustArgsJSON(t, searchMemoryArgs{}))
	if !strings.Contains(overview, "## development_code_specification") || !strings.Contains(overview, "Prettier config [prettier]") {
		t.Fatalf("overview missing entry:\n%s", overview)
	}
}

func TestMemoryToolsCatalogGating(t *testing.T) {
	modes := []agentv1.AgentMode{
		agentv1.AgentMode_AGENT_MODE_AGENT,
		agentv1.AgentMode_AGENT_MODE_ASK,
		agentv1.AgentMode_AGENT_MODE_PLAN,
		agentv1.AgentMode_AGENT_MODE_DEBUG,
		agentv1.AgentMode_AGENT_MODE_MULTITASK,
	}
	for _, name := range []string{"SaveMemory", "SearchMemory"} {
		if !isKnownToolName(name) {
			t.Fatalf("%s must be known", name)
		}
		if !isLocalStateTool(name) {
			t.Fatalf("%s must be local state", name)
		}
		for _, mode := range modes {
			if !isToolAllowedInMode(mode, "", name) {
				t.Fatalf("%s must be allowed in %s", name, mode.String())
			}
		}
		// Child subagent (subagentTypeName != "") dùng map agent -> phải được duyệt.
		if !isToolAllowedInMode(agentv1.AgentMode_AGENT_MODE_AGENT, "explore", name) {
			t.Fatalf("%s must be allowed for child subagents", name)
		}
	}
}

func TestBuildMemoryToolCallCard(t *testing.T) {
	invocation := runtimecore.ToolInvocation{
		CallID:   "call-mem-1",
		ToolName: "SaveMemory",
		ArgsJSON: []byte(`{"title":"Build with pnpm","category":"build_command","origin":"user"}`),
	}
	// Completed card: format MCP, đúng tên/args/result text.
	completed := buildMemoryToolCall(invocation, "Saved memory: Build with pnpm (build_command) at memories/projects/demo/build_command/build-with-pnpm.md")
	mcp := completed.GetMcpToolCall()
	if mcp == nil {
		t.Fatal("completed call must use MCP card variant")
	}
	if mcp.GetArgs().GetToolName() != "SaveMemory" || mcp.GetArgs().GetProviderIdentifier() != memoryToolServerIdentifier {
		t.Fatalf("args identity mismatch: %+v", mcp.GetArgs())
	}
	if got := mcp.GetArgs().GetArgs()["title"].GetStringValue(); got != "Build with pnpm" {
		t.Fatalf("display args missing title: %v", mcp.GetArgs().GetArgs())
	}
	success := mcp.GetResult().GetSuccess()
	if success == nil || success.GetIsError() {
		t.Fatal("success result expected")
	}
	if len(success.GetContent()) != 1 || !strings.Contains(success.GetContent()[0].GetText().GetText(), "Saved memory") {
		t.Fatalf("text content wrong: %+v", success.GetContent())
	}
	// Result lỗi -> card phải đánh dấu IsError.
	failed := buildMemoryToolCall(invocation, "SaveMemory failed: unsupported category").GetMcpToolCall().GetResult().GetSuccess()
	if failed == nil || !failed.GetIsError() {
		t.Fatal("failure result must set IsError")
	}
	// Started card (result rỗng) qua buildStartedToolCall.
	started := buildStartedToolCall(invocation)
	startedMCP := started.GetMcpToolCall()
	if startedMCP == nil || startedMCP.GetResult() != nil {
		t.Fatalf("started card must exist without result: %+v", startedMCP)
	}
	// JSON args hỏng vẫn dựng được card (không panic, title thiếu).
	broken := buildMemoryToolCall(runtimecore.ToolInvocation{CallID: "c", ToolName: "SearchMemory", ArgsJSON: []byte("{oops")}, "no matches")
	if broken.GetMcpToolCall().GetArgs().GetToolName() != "SearchMemory" {
		t.Fatal("broken args must still render identity card")
	}
}
