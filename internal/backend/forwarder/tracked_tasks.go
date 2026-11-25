package forwarder

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type trackedTaskBoard struct {
	mu       sync.Mutex
	nextID   int
	order    []string
	tasks    map[string]*agentv1.TrackedTaskView
	statuses []*agentv1.TrackedTaskStatusView
	tags     []*agentv1.TrackedTaskTagView
}

func newTrackedTaskBoard() *trackedTaskBoard {
	return &trackedTaskBoard{
		tasks: make(map[string]*agentv1.TrackedTaskView),
		statuses: []*agentv1.TrackedTaskStatusView{
			{Id: "todo", Label: "Todo", Category: "todo"},
			{Id: "in_progress", Label: "In Progress", Category: "in_progress"},
			{Id: "done", Label: "Done", Category: "completed"},
			{Id: "cancelled", Label: "Cancelled", Category: "cancelled"},
		},
	}
}

func (service *Service) getTrackedTaskBoard(conversationID string) *trackedTaskBoard {
	key := strings.TrimSpace(conversationID)
	if key == "" {
		key = "_default"
	}
	if existing, ok := service.trackedTasks.Load(key); ok {
		return existing.(*trackedTaskBoard)
	}
	board := newTrackedTaskBoard()
	actual, _ := service.trackedTasks.LoadOrStore(key, board)
	return actual.(*trackedTaskBoard)
}

func isTrackedTaskTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "ListTrackedTasks", "CreateTrackedTasks", "UpdateTrackedTasks",
		"DeleteTrackedTasks", "UpdateTrackedTaskStatuses", "UpdateTrackedTaskTags":
		return true
	default:
		return false
	}
}

func unmarshalTrackedTaskArgs(raw []byte, dst proto.Message) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	return opts.Unmarshal([]byte(trimmed), dst)
}

func buildTrackedTaskStartedToolCall(invocation runtimecore.ToolInvocation) *agentv1.ToolCall {
	switch strings.TrimSpace(invocation.ToolName) {
	case "ListTrackedTasks":
		args := &agentv1.ListTrackedTasksArgs{}
		_ = unmarshalTrackedTaskArgs(invocation.ArgsJSON, args)
		args.ToolCallId = invocation.CallID
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_ListTrackedTasksToolCall{
				ListTrackedTasksToolCall: &agentv1.ListTrackedTasksToolCall{Args: args},
			},
		}
	case "CreateTrackedTasks":
		args := &agentv1.CreateTrackedTasksArgs{}
		_ = unmarshalTrackedTaskArgs(invocation.ArgsJSON, args)
		args.ToolCallId = invocation.CallID
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_CreateTrackedTasksToolCall{
				CreateTrackedTasksToolCall: &agentv1.CreateTrackedTasksToolCall{Args: args},
			},
		}
	case "UpdateTrackedTasks":
		args := &agentv1.UpdateTrackedTasksArgs{}
		_ = unmarshalTrackedTaskArgs(invocation.ArgsJSON, args)
		args.ToolCallId = invocation.CallID
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_UpdateTrackedTasksToolCall{
				UpdateTrackedTasksToolCall: &agentv1.UpdateTrackedTasksToolCall{Args: args},
			},
		}
	case "DeleteTrackedTasks":
		args := &agentv1.DeleteTrackedTasksArgs{}
		_ = unmarshalTrackedTaskArgs(invocation.ArgsJSON, args)
		args.ToolCallId = invocation.CallID
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_DeleteTrackedTasksToolCall{
				DeleteTrackedTasksToolCall: &agentv1.DeleteTrackedTasksToolCall{Args: args},
			},
		}
	case "UpdateTrackedTaskStatuses":
		args := &agentv1.UpdateTrackedTaskStatusesArgs{}
		_ = unmarshalTrackedTaskArgs(invocation.ArgsJSON, args)
		args.ToolCallId = invocation.CallID
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_UpdateTrackedTaskStatusesToolCall{
				UpdateTrackedTaskStatusesToolCall: &agentv1.UpdateTrackedTaskStatusesToolCall{Args: args},
			},
		}
	case "UpdateTrackedTaskTags":
		args := &agentv1.UpdateTrackedTaskTagsArgs{}
		_ = unmarshalTrackedTaskArgs(invocation.ArgsJSON, args)
		args.ToolCallId = invocation.CallID
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_UpdateTrackedTaskTagsToolCall{
				UpdateTrackedTaskTagsToolCall: &agentv1.UpdateTrackedTaskTagsToolCall{Args: args},
			},
		}
	default:
		return nil
	}
}

func (service *Service) handleTrackedTaskToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	board := service.getTrackedTaskBoard(stream.ConversationID)
	switch strings.TrimSpace(invocation.ToolName) {
	case "ListTrackedTasks":
		return service.execListTrackedTasks(stream, board, invocation)
	case "CreateTrackedTasks":
		return service.execCreateTrackedTasks(stream, board, invocation)
	case "UpdateTrackedTasks":
		return service.execUpdateTrackedTasks(stream, board, invocation)
	case "DeleteTrackedTasks":
		return service.execDeleteTrackedTasks(stream, board, invocation)
	case "UpdateTrackedTaskStatuses":
		return service.execUpdateTrackedTaskStatuses(stream, board, invocation)
	case "UpdateTrackedTaskTags":
		return service.execUpdateTrackedTaskTags(stream, board, invocation)
	default:
		return fmt.Errorf("unsupported tracked task tool: %s", invocation.ToolName)
	}
}

func (b *trackedTaskBoard) resolveStatusLocked(raw string) (string, string) {
	q := strings.ToLower(strings.TrimSpace(raw))
	if q == "" {
		return b.statuses[0].Id, b.statuses[0].Label
	}
	for _, st := range b.statuses {
		if strings.ToLower(st.Id) == q || strings.ToLower(st.Label) == q || strings.ToLower(st.Category) == q {
			return st.Id, st.Label
		}
	}
	// Alias phổ biến từ LLM (completed -> done, pending -> todo)
	switch q {
	case "completed":
		for _, st := range b.statuses {
			if st.Category == "completed" {
				return st.Id, st.Label
			}
		}
	case "pending", "open":
		for _, st := range b.statuses {
			if st.Category == "todo" {
				return st.Id, st.Label
			}
		}
	}
	return b.statuses[0].Id, b.statuses[0].Label
}

func (b *trackedTaskBoard) recomputeCountsLocked() ([]*agentv1.TrackedTaskStatusView, []*agentv1.TrackedTaskTagView) {
	statusCounts := make(map[string]uint32)
	tagCounts := make(map[string]uint32)
	for _, t := range b.tasks {
		statusCounts[t.StatusId]++
		for _, tg := range t.Tags {
			tagCounts[tg]++
		}
		if t.Tag != "" {
			tagCounts[t.Tag]++
		}
	}
	statuses := make([]*agentv1.TrackedTaskStatusView, 0, len(b.statuses))
	for _, st := range b.statuses {
		cp := proto.Clone(st).(*agentv1.TrackedTaskStatusView)
		cp.TaskCount = statusCounts[st.Id]
		statuses = append(statuses, cp)
	}
	tags := make([]*agentv1.TrackedTaskTagView, 0, len(b.tags))
	for _, tg := range b.tags {
		cp := proto.Clone(tg).(*agentv1.TrackedTaskTagView)
		cp.TaskCount = tagCounts[tg.Id]
		tags = append(tags, cp)
	}
	return statuses, tags
}

func marshalProtoSummary(msg proto.Message) string {
	b, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(msg)
	if err != nil {
		fallback, _ := json.Marshal(map[string]string{"status": "ok"})
		return string(fallback)
	}
	return string(b)
}

func (service *Service) execListTrackedTasks(stream *ActiveStream, board *trackedTaskBoard, invocation runtimecore.ToolInvocation) error {
	args := &agentv1.ListTrackedTasksArgs{}
	if err := unmarshalTrackedTaskArgs(invocation.ArgsJSON, args); err != nil {
		res := &agentv1.ListTrackedTasksResult{Result: &agentv1.ListTrackedTasksResult_Error{Error: &agentv1.ListTrackedTasksError{Error: err.Error()}}}
		tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_ListTrackedTasksToolCall{ListTrackedTasksToolCall: &agentv1.ListTrackedTasksToolCall{Args: args, Result: res}}}
		return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(res), tc)
	}
	args.ToolCallId = invocation.CallID

	board.mu.Lock()
	keyFilter := make(map[string]bool, len(args.Keys))
	for _, k := range args.Keys {
		keyFilter[strings.TrimSpace(k)] = true
	}
	catFilter := make(map[string]bool, len(args.StatusCategories))
	for _, c := range args.StatusCategories {
		catFilter[strings.ToLower(strings.TrimSpace(c))] = true
	}
	statusCatByID := make(map[string]string, len(board.statuses))
	for _, st := range board.statuses {
		statusCatByID[st.Id] = strings.ToLower(st.Category)
	}

	var matched []*agentv1.TrackedTaskView
	for _, k := range board.order {
		t, ok := board.tasks[k]
		if !ok {
			continue
		}
		if len(keyFilter) > 0 && !keyFilter[t.Key] {
			continue
		}
		if len(catFilter) > 0 && !catFilter[statusCatByID[t.StatusId]] {
			continue
		}
		if args.Owner != nil && strings.TrimSpace(*args.Owner) != "" && !strings.EqualFold(t.Owner, strings.TrimSpace(*args.Owner)) {
			continue
		}
		matched = append(matched, proto.Clone(t).(*agentv1.TrackedTaskView))
	}
	statuses, tags := board.recomputeCountsLocked()
	board.mu.Unlock()

	success := &agentv1.ListTrackedTasksSuccess{
		Tasks:         matched,
		Statuses:      statuses,
		Tags:          tags,
		MatchingCount: uint32(len(matched)),
		Message:       fmt.Sprintf("Found %d tracked task(s)", len(matched)),
	}
	res := &agentv1.ListTrackedTasksResult{Result: &agentv1.ListTrackedTasksResult_Success{Success: success}}
	tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_ListTrackedTasksToolCall{ListTrackedTasksToolCall: &agentv1.ListTrackedTasksToolCall{Args: args, Result: res}}}
	return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(success), tc)
}

func (service *Service) execCreateTrackedTasks(stream *ActiveStream, board *trackedTaskBoard, invocation runtimecore.ToolInvocation) error {
	args := &agentv1.CreateTrackedTasksArgs{}
	if err := unmarshalTrackedTaskArgs(invocation.ArgsJSON, args); err != nil {
		res := &agentv1.CreateTrackedTasksResult{Result: &agentv1.CreateTrackedTasksResult_Error{Error: &agentv1.CreateTrackedTasksError{Error: err.Error()}}}
		tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_CreateTrackedTasksToolCall{CreateTrackedTasksToolCall: &agentv1.CreateTrackedTasksToolCall{Args: args, Result: res}}}
		return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(res), tc)
	}
	args.ToolCallId = invocation.CallID

	nowMs := uint64(time.Now().UnixMilli())
	board.mu.Lock()
	created := make([]*agentv1.TrackedTaskCreated, 0, len(args.Tasks))
	refToKey := make(map[string]string)
	for _, item := range args.Tasks {
		if item == nil {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = strings.TrimSpace(item.GetInternalNotes())
		}
		if title == "" {
			title = strings.TrimSpace(item.GetClientRef())
		}
		if title == "" {
			continue
		}
		item.Title = title
		board.nextID++
		key := fmt.Sprintf("TASK-%d", board.nextID)
		statusRaw := ""
		if item.Status != nil {
			statusRaw = *item.Status
		}
		statusID, statusLabel := board.resolveStatusLocked(statusRaw)
		parentKey := ""
		if item.Parent != nil {
			p := strings.TrimSpace(*item.Parent)
			if mapped, ok := refToKey[p]; ok {
				parentKey = mapped
			} else {
				parentKey = p
			}
		}
		view := &agentv1.TrackedTaskView{
			Key:           key,
			Title:         title,
			StatusId:      statusID,
			StatusLabel:   statusLabel,
			Owner:         strings.TrimSpace(item.GetOwner()),
			ParentKey:     parentKey,
			UpdatedAtMs:   nowMs,
			Version:       1,
			Tag:           strings.TrimSpace(item.GetTag()),
			InternalNotes: strings.TrimSpace(item.GetInternalNotes()),
			Tags:          append([]string(nil), item.Tags...),
		}
		board.tasks[key] = view
		board.order = append(board.order, key)
		clientRef := strings.TrimSpace(item.GetClientRef())
		if clientRef != "" {
			refToKey[clientRef] = key
		}
		created = append(created, &agentv1.TrackedTaskCreated{
			ClientRef: clientRef,
			Key:       key,
			Version:   1,
		})
	}
	board.mu.Unlock()

	success := &agentv1.CreateTrackedTasksSuccess{
		Created: created,
		Message: fmt.Sprintf("Created %d tracked task(s)", len(created)),
	}
	res := &agentv1.CreateTrackedTasksResult{Result: &agentv1.CreateTrackedTasksResult_Success{Success: success}}
	tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_CreateTrackedTasksToolCall{CreateTrackedTasksToolCall: &agentv1.CreateTrackedTasksToolCall{Args: args, Result: res}}}
	return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(success), tc)
}

func (service *Service) execUpdateTrackedTasks(stream *ActiveStream, board *trackedTaskBoard, invocation runtimecore.ToolInvocation) error {
	args := &agentv1.UpdateTrackedTasksArgs{}
	if err := unmarshalTrackedTaskArgs(invocation.ArgsJSON, args); err != nil {
		res := &agentv1.UpdateTrackedTasksResult{Result: &agentv1.UpdateTrackedTasksResult_Error{Error: &agentv1.UpdateTrackedTasksError{Error: err.Error()}}}
		tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_UpdateTrackedTasksToolCall{UpdateTrackedTasksToolCall: &agentv1.UpdateTrackedTasksToolCall{Args: args, Result: res}}}
		return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(res), tc)
	}
	args.ToolCallId = invocation.CallID

	nowMs := uint64(time.Now().UnixMilli())
	board.mu.Lock()
	var updated []*agentv1.TrackedTaskUpdated
	var conflicts []*agentv1.TrackedTaskConflict
	for _, upd := range args.Updates {
		if upd == nil {
			continue
		}
		k := strings.TrimSpace(upd.Key)
		t, ok := board.tasks[k]
		if !ok {
			conflicts = append(conflicts, &agentv1.TrackedTaskConflict{
				Key:          k,
				Field:        "key",
				CurrentValue: "not_found",
			})
			continue
		}
		if upd.Version > 0 && upd.Version != t.Version {
			conflicts = append(conflicts, &agentv1.TrackedTaskConflict{
				Key:            k,
				Field:          "version",
				CurrentValue:   fmt.Sprintf("%d", t.Version),
				ChangedAtMs:    t.UpdatedAtMs,
				CurrentVersion: t.Version,
			})
			continue
		}
		if upd.Title != nil && strings.TrimSpace(*upd.Title) != "" {
			t.Title = strings.TrimSpace(*upd.Title)
		}
		if upd.Status != nil {
			t.StatusId, t.StatusLabel = board.resolveStatusLocked(*upd.Status)
		}
		if upd.Owner != nil {
			t.Owner = strings.TrimSpace(*upd.Owner)
		}
		if upd.Parent != nil {
			t.ParentKey = strings.TrimSpace(*upd.Parent)
		}
		if upd.Tag != nil {
			t.Tag = strings.TrimSpace(*upd.Tag)
		}
		if upd.InternalNotes != nil {
			t.InternalNotes = strings.TrimSpace(*upd.InternalNotes)
		}
		if upd.Tags != nil {
			t.Tags = append([]string(nil), upd.Tags.Ids...)
		}
		t.Version++
		t.UpdatedAtMs = nowMs
		updated = append(updated, &agentv1.TrackedTaskUpdated{
			Key:         t.Key,
			Version:     t.Version,
			Title:       t.Title,
			StatusLabel: t.StatusLabel,
		})
	}
	board.mu.Unlock()

	success := &agentv1.UpdateTrackedTasksSuccess{
		Updated:   updated,
		Conflicts: conflicts,
		Message:   fmt.Sprintf("Updated %d tracked task(s), %d conflict(s)", len(updated), len(conflicts)),
	}
	res := &agentv1.UpdateTrackedTasksResult{Result: &agentv1.UpdateTrackedTasksResult_Success{Success: success}}
	tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_UpdateTrackedTasksToolCall{UpdateTrackedTasksToolCall: &agentv1.UpdateTrackedTasksToolCall{Args: args, Result: res}}}
	return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(success), tc)
}

func (service *Service) execDeleteTrackedTasks(stream *ActiveStream, board *trackedTaskBoard, invocation runtimecore.ToolInvocation) error {
	args := &agentv1.DeleteTrackedTasksArgs{}
	if err := unmarshalTrackedTaskArgs(invocation.ArgsJSON, args); err != nil {
		res := &agentv1.DeleteTrackedTasksResult{Result: &agentv1.DeleteTrackedTasksResult_Error{Error: &agentv1.DeleteTrackedTasksError{Error: err.Error()}}}
		tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_DeleteTrackedTasksToolCall{DeleteTrackedTasksToolCall: &agentv1.DeleteTrackedTasksToolCall{Args: args, Result: res}}}
		return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(res), tc)
	}
	args.ToolCallId = invocation.CallID

	board.mu.Lock()
	toDelete := make(map[string]bool, len(args.Keys))
	var deletedKeys []string
	for _, rawKey := range args.Keys {
		k := strings.TrimSpace(rawKey)
		if _, exists := board.tasks[k]; exists {
			toDelete[k] = true
			delete(board.tasks, k)
			deletedKeys = append(deletedKeys, k)
		}
	}
	if len(toDelete) > 0 {
		nextOrder := make([]string, 0, len(board.order))
		for _, k := range board.order {
			if !toDelete[k] {
				nextOrder = append(nextOrder, k)
			}
		}
		board.order = nextOrder
	}
	var unparentedKeys []string
	for _, t := range board.tasks {
		if toDelete[t.ParentKey] {
			t.ParentKey = ""
			unparentedKeys = append(unparentedKeys, t.Key)
		}
	}
	sort.Strings(unparentedKeys)
	board.mu.Unlock()

	success := &agentv1.DeleteTrackedTasksSuccess{
		DeletedKeys:    deletedKeys,
		UnparentedKeys: unparentedKeys,
		Message:        fmt.Sprintf("Deleted %d tracked task(s)", len(deletedKeys)),
	}
	res := &agentv1.DeleteTrackedTasksResult{Result: &agentv1.DeleteTrackedTasksResult_Success{Success: success}}
	tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_DeleteTrackedTasksToolCall{DeleteTrackedTasksToolCall: &agentv1.DeleteTrackedTasksToolCall{Args: args, Result: res}}}
	return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(success), tc)
}

func (service *Service) execUpdateTrackedTaskStatuses(stream *ActiveStream, board *trackedTaskBoard, invocation runtimecore.ToolInvocation) error {
	args := &agentv1.UpdateTrackedTaskStatusesArgs{}
	if err := unmarshalTrackedTaskArgs(invocation.ArgsJSON, args); err != nil {
		res := &agentv1.UpdateTrackedTaskStatusesResult{Result: &agentv1.UpdateTrackedTaskStatusesResult_Error{Error: &agentv1.UpdateTrackedTaskStatusesError{Error: err.Error()}}}
		tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_UpdateTrackedTaskStatusesToolCall{UpdateTrackedTaskStatusesToolCall: &agentv1.UpdateTrackedTaskStatusesToolCall{Args: args, Result: res}}}
		return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(res), tc)
	}
	args.ToolCallId = invocation.CallID

	board.mu.Lock()
	if len(args.Statuses) > 0 {
		next := make([]*agentv1.TrackedTaskStatusView, 0, len(args.Statuses))
		for i, st := range args.Statuses {
			if st == nil || strings.TrimSpace(st.Label) == "" {
				continue
			}
			id := strings.TrimSpace(st.GetId())
			if id == "" {
				id = fmt.Sprintf("status_%d", i+1)
			}
			cat := strings.TrimSpace(st.Category)
			if cat == "" {
				cat = "todo"
			}
			next = append(next, &agentv1.TrackedTaskStatusView{
				Id:          id,
				Label:       strings.TrimSpace(st.Label),
				Category:    cat,
				Description: st.Description,
			})
		}
		if len(next) > 0 {
			board.statuses = next
		}
	}
	statuses, _ := board.recomputeCountsLocked()
	board.mu.Unlock()

	success := &agentv1.UpdateTrackedTaskStatusesSuccess{
		Statuses: statuses,
		Message:  fmt.Sprintf("Configured %d tracked task status(es)", len(statuses)),
	}
	res := &agentv1.UpdateTrackedTaskStatusesResult{Result: &agentv1.UpdateTrackedTaskStatusesResult_Success{Success: success}}
	tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_UpdateTrackedTaskStatusesToolCall{UpdateTrackedTaskStatusesToolCall: &agentv1.UpdateTrackedTaskStatusesToolCall{Args: args, Result: res}}}
	return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(success), tc)
}

func (service *Service) execUpdateTrackedTaskTags(stream *ActiveStream, board *trackedTaskBoard, invocation runtimecore.ToolInvocation) error {
	args := &agentv1.UpdateTrackedTaskTagsArgs{}
	if err := unmarshalTrackedTaskArgs(invocation.ArgsJSON, args); err != nil {
		res := &agentv1.UpdateTrackedTaskTagsResult{Result: &agentv1.UpdateTrackedTaskTagsResult_Error{Error: &agentv1.UpdateTrackedTaskTagsError{Error: err.Error()}}}
		tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_UpdateTrackedTaskTagsToolCall{UpdateTrackedTaskTagsToolCall: &agentv1.UpdateTrackedTaskTagsToolCall{Args: args, Result: res}}}
		return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(res), tc)
	}
	args.ToolCallId = invocation.CallID

	board.mu.Lock()
	if len(args.Tags) > 0 {
		next := make([]*agentv1.TrackedTaskTagView, 0, len(args.Tags))
		for i, tg := range args.Tags {
			if tg == nil || strings.TrimSpace(tg.Label) == "" {
				continue
			}
			id := strings.TrimSpace(tg.GetId())
			if id == "" {
				id = fmt.Sprintf("tag_%d", i+1)
			}
			next = append(next, &agentv1.TrackedTaskTagView{
				Id:    id,
				Label: strings.TrimSpace(tg.Label),
				Color: strings.TrimSpace(tg.GetColor()),
			})
		}
		board.tags = next
	}
	_, tags := board.recomputeCountsLocked()
	board.mu.Unlock()

	success := &agentv1.UpdateTrackedTaskTagsSuccess{
		Tags:    tags,
		Message: fmt.Sprintf("Configured %d tracked task tag(s)", len(tags)),
	}
	res := &agentv1.UpdateTrackedTaskTagsResult{Result: &agentv1.UpdateTrackedTaskTagsResult_Success{Success: success}}
	tc := &agentv1.ToolCall{Tool: &agentv1.ToolCall_UpdateTrackedTaskTagsToolCall{UpdateTrackedTaskTagsToolCall: &agentv1.UpdateTrackedTaskTagsToolCall{Args: args, Result: res}}}
	return service.completeImmediateToolResult(stream, invocation, marshalProtoSummary(success), tc)
}
