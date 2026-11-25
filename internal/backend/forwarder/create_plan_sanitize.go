package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	runtimecore "cursor/internal/backend/agent/core"
)

func (service *Service) sanitizeCreatePlanInvocationForCurrentPlan(stream *ActiveStream, invocation runtimecore.ToolInvocation) (runtimecore.ToolInvocation, error) {
	if strings.TrimSpace(invocation.ToolName) != "CreatePlan" {
		return invocation, nil
	}
	args, err := runtimecore.DecodeCreatePlanArgsJSON(invocation.ArgsJSON)
	if err != nil {
		return invocation, newRecoverableToolInvocationError(fmt.Errorf("decode CreatePlan args failed: %w", err))
	}
	// Chặn cuộc gọi rỗng: nếu để lọt, Cursor vẫn trả planUri và conversation bị
	// hasCurrentPlan()==true với plan trống, khiến `name` bị strip vĩnh viễn ở các lần sau.
	if strings.TrimSpace(args.GetPlan()) == "" && len(flattenCreatePlanTodos(args)) == 0 {
		return invocation, newRecoverableToolInvocationError(fmt.Errorf(
			"CreatePlan requires a non-empty `plan` body and a `todos` checklist; resend it with both fields filled in"))
	}
	if strings.TrimSpace(args.GetName()) == "" {
		return invocation, nil
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return invocation, err
	}
	if !hasCurrentPlan(conversation) {
		return invocation, nil
	}
	args.Name = ""
	sanitized, err := json.Marshal(args)
	if err != nil {
		return invocation, newRecoverableToolInvocationError(fmt.Errorf("encode sanitized CreatePlan args failed: %w", err))
	}
	invocation.ArgsJSON = sanitized
	return invocation, nil
}
