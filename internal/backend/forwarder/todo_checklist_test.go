package forwarder

// Bài test cho cơ chế "chạy từng bước theo checklist": schema CreatePlan phải khớp decoder,
// reminder tracking chỉ bật khi còn bước chưa xong, và CreatePlan rỗng bị chặn trước khi
// nó đầu độc hasCurrentPlan().

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	promptassets "cursor/prompt"
)

func todoChecklistFunction(t *testing.T, toolName string) map[string]any {
	t.Helper()
	raw, err := promptassets.ReadToolsCatalog()
	if err != nil {
		t.Fatalf("ReadToolsCatalog: %v", err)
	}
	var catalog struct {
		Tools []struct {
			Function map[string]any `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}
	for _, item := range catalog.Tools {
		if strings.TrimSpace(fmt.Sprint(item.Function["name"])) == toolName {
			return item.Function
		}
	}
	t.Fatalf("tool %q missing from catalog", toolName)
	return nil
}

func todoChecklistParameters(t *testing.T, toolName string) (map[string]any, map[string]any) {
	t.Helper()
	function := todoChecklistFunction(t, toolName)
	parameters, ok := function["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no parameters object", toolName)
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no properties", toolName)
	}
	return parameters, properties
}

func stringSliceFromSchema(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

// Schema phải khai báo đủ field bắt buộc và dùng đúng bộ enum status như TodoWrite,
// để model không phải đoán trạng thái bước.
func TestCreatePlanSchemaDeclaresRequiredFieldsAndTodoStatus(t *testing.T) {
	parameters, properties := todoChecklistParameters(t, "CreatePlan")

	required := stringSliceFromSchema(t, parameters["required"])
	if strings.Join(required, ",") != "plan,overview,todos" {
		t.Fatalf("CreatePlan required = %v, want [plan overview todos]", required)
	}

	todosSchema, ok := properties["todos"].(map[string]any)
	if !ok {
		t.Fatal("CreatePlan missing todos")
	}
	if minItems, _ := todosSchema["minItems"].(float64); minItems < 1 {
		t.Fatalf("CreatePlan todos minItems = %v, want >= 1", todosSchema["minItems"])
	}
	items, ok := todosSchema["items"].(map[string]any)
	if !ok {
		t.Fatal("CreatePlan todos has no items schema")
	}
	itemProperties, _ := items["properties"].(map[string]any)
	statusSchema, ok := itemProperties["status"].(map[string]any)
	if !ok {
		t.Fatal("CreatePlan todos.items missing status")
	}
	gotStatus := strings.Join(stringSliceFromSchema(t, statusSchema["enum"]), ",")
	if gotStatus != "pending,in_progress,completed,cancelled" {
		t.Fatalf("CreatePlan todo status enum = %q", gotStatus)
	}

	// TodoWrite là công cụ tick trạng thái; hai schema phải khớp nhau để model không loạn.
	_, writeProperties := todoChecklistParameters(t, "TodoWrite")
	writeTodos, _ := writeProperties["todos"].(map[string]any)
	writeItems, _ := writeTodos["items"].(map[string]any)
	writeItemProperties, _ := writeItems["properties"].(map[string]any)
	writeStatus, _ := writeItemProperties["status"].(map[string]any)
	if writeStatus == nil || strings.Join(stringSliceFromSchema(t, writeStatus["enum"]), ",") != gotStatus {
		t.Fatal("CreatePlan and TodoWrite expose different todo status enums")
	}
}

// Mọi field CreatePlan khai ra phải được backend decode thật, không có field "trang trí".
func TestCreatePlanSchemaFieldsDecodeIntoArgs(t *testing.T) {
	_, properties := todoChecklistParameters(t, "CreatePlan")
	for _, field := range []string{"name", "overview", "plan", "todos"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("schema lost field %q", field)
		}
	}

	args, err := runtimecore.DecodeCreatePlanArgsJSON([]byte(`{
		"name":"Plan checklist",
		"overview":"Make step-by-step execution reliable",
		"plan":"### Goal\nShip it",
		"todos":[{"id":"t1","content":"Update schema","status":"in_progress"},{"id":"t2","content":"Add reminder","status":"pending"}]
	}`))
	if err != nil {
		t.Fatalf("DecodeCreatePlanArgsJSON: %v", err)
	}
	if args.GetName() != "Plan checklist" || args.GetOverview() == "" || args.GetPlan() == "" {
		t.Fatalf("declared fields not decoded: %#v", args)
	}
	if len(args.GetTodos()) != 2 {
		t.Fatalf("todos not decoded: %#v", args.GetTodos())
	}
	if args.GetTodos()[0].GetStatus() != agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS {
		t.Fatalf("string status not decoded: %v", args.GetTodos()[0].GetStatus())
	}
}

func todoChecklistConversation(t *testing.T, statuses ...agentv1.TodoStatus) *ConversationFile {
	t.Helper()
	args := &agentv1.CreatePlanArgs{
		Plan:     "### Goal\nChạy từng bước theo checklist",
		Overview: "overview text",
	}
	for index, status := range statuses {
		args.Todos = append(args.Todos, &agentv1.TodoItem{
			Id:      fmt.Sprintf("t%d", index+1),
			Content: fmt.Sprintf("Bước %d: cập nhật schema CreatePlan trong prompt/tools.json", index+1),
			Status:  status,
		})
	}
	toolCall := &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_CreatePlanToolCall{
			CreatePlanToolCall: &agentv1.CreatePlanToolCall{
				Args: args,
				Result: &agentv1.CreatePlanResult{
					PlanUri: "cursor://plan/1",
					Result:  &agentv1.CreatePlanResult_Success{Success: &agentv1.CreatePlanSuccess{}},
				},
			},
		},
	}
	encoded, err := protojson.Marshal(toolCall)
	if err != nil {
		t.Fatalf("marshal tool call: %v", err)
	}
	conversation := &ConversationFile{
		ConversationID:     "conv-todo-checklist",
		RootConversationID: "conv-todo-checklist",
		Mode:               "agent",
		NextTurnSeq:        2,
		NextEntrySeq:       1,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		newToolResultEntry(1, "request-1", "call-1", "CreatePlan", "{}", "plan created", "", encoded),
	})
	return conversation
}

func TestTodoTrackingReminderTextGatesOnOpenChecklist(t *testing.T) {
	if text := todoTrackingReminderText(nil); text != "" {
		t.Fatalf("nil conversation must not emit reminder: %q", text)
	}
	empty := &ConversationFile{ConversationID: "conv-none", Mode: "agent", NextTurnSeq: 2}
	if text := todoTrackingReminderText(empty); text != "" {
		t.Fatalf("no checklist must not emit reminder: %q", text)
	}
	done := todoChecklistConversation(t,
		agentv1.TodoStatus_TODO_STATUS_COMPLETED,
		agentv1.TodoStatus_TODO_STATUS_CANCELLED,
	)
	if text := todoTrackingReminderText(done); text != "" {
		t.Fatalf("finished checklist must not emit reminder: %q", text)
	}

	open := todoChecklistConversation(t,
		agentv1.TodoStatus_TODO_STATUS_COMPLETED,
		agentv1.TodoStatus_TODO_STATUS_PENDING,
		agentv1.TodoStatus_TODO_STATUS_PENDING,
	)
	text := todoTrackingReminderText(open)
	if !strings.Contains(text, "3 steps, 1 completed, 0 in_progress, 2 pending") {
		t.Fatalf("reminder lost counters: %q", text)
	}
	if !strings.Contains(text, "Current step (pending): t2") {
		t.Fatalf("reminder must point at the next pending id: %q", text)
	}
	// Nội dung tiếng Việt có dấu phải còn nguyên, không bị cắt hỏng UTF-8.
	if strings.Contains(text, "Bước 2") && strings.ContainsAny(text, "ï¿½") {
		t.Fatalf("reminder content mangled: %q", text)
	}

	resumed := todoChecklistConversation(t,
		agentv1.TodoStatus_TODO_STATUS_COMPLETED,
		agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS,
		agentv1.TodoStatus_TODO_STATUS_PENDING,
	)
	resumedText := todoTrackingReminderText(resumed)
	if !strings.Contains(resumedText, "Current step (in_progress): t2") {
		t.Fatalf("reminder must prefer the in_progress step: %q", resumedText)
	}
}

func TestReminderInjectorAddsTodoTrackingContextInAgentMode(t *testing.T) {
	injector := NewReminderInjector()
	conversation := todoChecklistConversation(t,
		agentv1.TodoStatus_TODO_STATUS_PENDING,
		agentv1.TodoStatus_TODO_STATUS_PENDING,
	)

	agentReminders := injector.Inject(agentv1.AgentMode_AGENT_MODE_AGENT, conversation, nil, "làm tiếp đi", nil)
	if !promptContextsContainSource(agentReminders.PromptContexts, promptContextSourceTodoTracking) {
		t.Fatalf("agent mode with open checklist must inject todo tracking: %#v", agentReminders.PromptContexts)
	}

	planReminders := injector.Inject(agentv1.AgentMode_AGENT_MODE_PLAN, conversation, nil, "lên plan tiếp", nil)
	if promptContextsContainSource(planReminders.PromptContexts, promptContextSourceTodoTracking) {
		t.Fatal("plan mode must not get the execution checklist nudge")
	}

	closed := todoChecklistConversation(t, agentv1.TodoStatus_TODO_STATUS_COMPLETED)
	closedReminders := injector.Inject(agentv1.AgentMode_AGENT_MODE_AGENT, closed, nil, "xong chưa", nil)
	if promptContextsContainSource(closedReminders.PromptContexts, promptContextSourceTodoTracking) {
		t.Fatal("finished checklist must not repeat the nudge")
	}
}

func promptContextsContainSource(contexts []PromptContextMessage, source string) bool {
	for _, context := range contexts {
		if strings.TrimSpace(context.Source) == source {
			return true
		}
	}
	return false
}

func TestSanitizeCreatePlanRejectsEmptyInvocation(t *testing.T) {
	service := &Service{}

	empty := runtimecore.ToolInvocation{ToolName: "CreatePlan", ArgsJSON: []byte(`{}`)}
	if _, err := service.sanitizeCreatePlanInvocationForCurrentPlan(&ActiveStream{}, empty); err == nil {
		t.Fatal("empty CreatePlan must be rejected before it poisons hasCurrentPlan")
	} else if _, ok := recoverableToolInvocationCause(err); !ok {
		t.Fatalf("rejection must be recoverable: %v", err)
	}

	planOnly := runtimecore.ToolInvocation{
		ToolName: "CreatePlan",
		ArgsJSON: []byte(`{"plan":"### Goal\nShip it","overview":"ov"}`),
	}
	if _, err := service.sanitizeCreatePlanInvocationForCurrentPlan(&ActiveStream{}, planOnly); err != nil {
		t.Fatalf("plan body without todos must stay accepted: %v", err)
	}

	todosOnly := runtimecore.ToolInvocation{
		ToolName: "CreatePlan",
		ArgsJSON: []byte(`{"todos":[{"id":"t1","content":"step one"}]}`),
	}
	if _, err := service.sanitizeCreatePlanInvocationForCurrentPlan(&ActiveStream{}, todosOnly); err != nil {
		t.Fatalf("checklist without plan body must stay accepted: %v", err)
	}
}

// Ghim lại ngữ nghĩa merge để không ai đổi âm thầm: merge=true giữ nguyên item không được nhắc
// (nên không bao giờ là cách mở task mới), merge=false bị chặn khi checklist cũ còn todo active,
// và "quên gửi merge" bị server tự suy thành merge=true.
func TestTodoMergeSemanticsGuardFreshChecklist(t *testing.T) {
	now := time.Now().UTC()
	existing := []*agentv1.TodoItem{
		{Id: "t1", Content: "cập nhật schema", Status: agentv1.TodoStatus_TODO_STATUS_COMPLETED},
		{Id: "t2", Content: "thêm reminder tracking", Status: agentv1.TodoStatus_TODO_STATUS_PENDING},
	}

	ticked, err := mergeTodoItems(existing, []*agentv1.TodoItem{
		{Id: "t2", Status: agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS},
	}, now)
	if err != nil {
		t.Fatalf("mergeTodoItems: %v", err)
	}
	if len(ticked) != 2 {
		t.Fatalf("merge=true must keep unmentioned todos, got %#v", ticked)
	}
	if ticked[1].GetStatus() != agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS || ticked[1].GetContent() != "thêm reminder tracking" {
		t.Fatalf("status-only tick lost or overwrote content: %#v", ticked[1])
	}

	appended, err := mergeTodoItems(existing, []*agentv1.TodoItem{
		{Id: "n1", Content: "task khác hoàn toàn"},
	}, now)
	if err != nil {
		t.Fatalf("mergeTodoItems append: %v", err)
	}
	if len(appended) != 3 || appended[2].GetId() != "n1" {
		t.Fatalf("merge=true appended a new task onto the old checklist (documented hazard): %#v", appended)
	}

	freshChecklist := []*agentv1.TodoItem{
		{Id: "n1", Content: "task khác hoàn toàn", Status: agentv1.TodoStatus_TODO_STATUS_PENDING},
	}
	if missing := strings.Join(missingActiveTodoReplacementIDs(existing, freshChecklist), ","); missing != "t2" {
		t.Fatalf("fresh checklist must be blocked by active t2, got missing=%q", missing)
	}
	if shouldMergeTodoUpdate(&agentv1.UpdateTodosArgs{Todos: freshChecklist}, false, existing) != true {
		t.Fatal("omitting merge with a dropped active id must fall back to merge=true")
	}
	if shouldMergeTodoUpdate(&agentv1.UpdateTodosArgs{Todos: freshChecklist, Merge: false}, true, existing) {
		t.Fatal("explicit merge=false must not be rewritten into merge=true by the fallback")
	}

	closedOut := []*agentv1.TodoItem{
		{Id: "t1", Content: "cập nhật schema", Status: agentv1.TodoStatus_TODO_STATUS_COMPLETED},
		{Id: "t2", Content: "thêm reminder tracking", Status: agentv1.TodoStatus_TODO_STATUS_CANCELLED},
	}
	if missing := missingActiveTodoReplacementIDs(closedOut, freshChecklist); len(missing) != 0 {
		t.Fatalf("terminal todos must not block a fresh checklist: %v", missing)
	}
}

func TestTodoTrackingReminderExplainsHowToSwitchToNewChecklist(t *testing.T) {
	open := todoChecklistConversation(t, agentv1.TodoStatus_TODO_STATUS_PENDING)
	text := todoTrackingReminderText(open)
	for _, fragment := range []string{"cancelled", "merge=false", "never the way to switch"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("reminder must teach the fresh-checklist protocol, missing %q in:\n%s", fragment, text)
		}
	}
}
