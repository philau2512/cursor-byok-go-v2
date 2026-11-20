package forwarder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptassets "cursor/prompt"
)

const (
	commitMessageDiffTotalLimit           = 120_000
	commitMessageSingleDiffLimit          = 40_000
	commitMessagePreviousCommitLimit      = 12
	commitMessageExplicitContextLimit     = 20_000
	commitMessageBaseOutputTokens         = 512
	commitMessagePerFileOutputTokens      = 40
	commitMessageMaxOutputTokens          = 1_536
	commitMessageManifestMaxFiles         = 200
	commitMessageGeneratedRequestIDPrefix = "commit-message-"
)

var errCommitMessageToolInvocation = errors.New("commit message generation must not invoke tools")

// WriteGitCommitMessage handles Cursor's SCM "Generate Commit Message" action.
func (service *Service) WriteGitCommitMessage(ctx context.Context, req *connect.Request[aiserverv1.WriteGitCommitMessageRequest]) (*connect.Response[aiserverv1.WriteGitCommitMessageResponse], error) {
	if service == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := commitMessageGeneratedRequestIDPrefix + uuid.NewString()
	recorder, err := newCommitMessageLogRecorder(service.commitMessageHistoryRoot(), requestID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if req == nil || req.Msg == nil {
		return nil, commitMessageConnectError(recorder, connect.CodeInvalidArgument, fmt.Errorf("write git commit message request is required"))
	}
	if err := recordCommitMessageIncomingRequest(recorder, requestID, req.Msg); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	diffs := truncateCommitMessageDiffs(req.Msg.GetDiffs(), commitMessageDiffTotalLimit, commitMessageSingleDiffLimit)
	if len(diffs) == 0 {
		return nil, commitMessageConnectError(recorder, connect.CodeInvalidArgument, fmt.Errorf("diffs are required"))
	}
	if service.provider == nil {
		return nil, commitMessageConnectError(recorder, connect.CodeInternal, fmt.Errorf("provider gateway is not initialized"))
	}
	messages, changedFileCount, err := buildCommitMessagePrompt(req.Msg, diffs)
	if err != nil {
		return nil, commitMessageConnectError(recorder, connect.CodeInternal, err)
	}
	modelCallID := requestID + "-model"
	modelID, modelSource, modelSourceValue := service.resolveCommitMessageModelID(ctx)
	accumulated := ""
	artifactPaths := &modeladapter.LLMArtifactPaths{}
	err = service.provider.StartStream(ctx, ProviderRequest{
		RequestID:      requestID,
		RunID:          requestID,
		ModelCallID:    modelCallID,
		ModelID:        modelID,
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		Messages:       messages,
		Tools:          nil,
		MaxTokens:      commitMessageOutputBudget(changedFileCount),
		CompileSummary: fmt.Sprintf("generate commit message diffs=%d previous_commits=%d model_source=%s model_source_value=%s", len(diffs), len(req.Msg.GetPreviousCommitMessages()), modelSource, modelSourceValue),
		Observer:       recorder,
		ArtifactPaths:  artifactPaths,
	}, func(event modeladapter.ModelEvent) error {
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			accumulated += event.Text
			return nil
		case modeladapter.ModelEventKindThinkingDelta, modeladapter.ModelEventKindThinkingCompleted, modeladapter.ModelEventKindTurnFinished:
			return nil
		case modeladapter.ModelEventKindToolLikeCompleted, modeladapter.ModelEventKindPartialToolCall, modeladapter.ModelEventKindToolCallDelta:
			return errCommitMessageToolInvocation
		case modeladapter.ModelEventKindProviderError:
			if event.Err != nil {
				return providerTerminalError{cause: event.Err}
			}
			return providerTerminalError{cause: fmt.Errorf("provider error")}
		default:
			return nil
		}
	})
	if err != nil {
		if errors.Is(err, errCommitMessageToolInvocation) {
			return nil, commitMessageConnectError(recorder, connect.CodeInternal, errCommitMessageToolInvocation)
		}
		return nil, commitMessageConnectError(recorder, connect.CodeUnknown, err)
	}
	commitMessage := cleanGeneratedCommitMessage(accumulated)
	if firstCommitMessageLine(commitMessage) == "" {
		return nil, commitMessageConnectError(recorder, connect.CodeInternal, fmt.Errorf("generated commit message is empty"))
	}
	if _, err := recorder.appendEvent("final_response", map[string]any{
		"request_id":       requestID,
		"model_call_id":    modelCallID,
		"model_source":     modelSource,
		"model_id":         modelID,
		"raw_text":         accumulated,
		"commit_message":   commitMessage,
		"artifact_request": artifactPaths.RequestPath,
		"artifact_sse":     artifactPaths.ResponsePath,
		"artifact_summary": artifactPaths.SummaryPath,
	}); err != nil {
		log.Printf("forwarder failed to write commit message final log request_id=%s error=%v", requestID, err)
	}
	return connect.NewResponse(&aiserverv1.WriteGitCommitMessageResponse{
		CommitMessage: commitMessage,
	}), nil
}

func buildCommitMessagePrompt(req *aiserverv1.WriteGitCommitMessageRequest, diffs []string) ([]modeladapter.Message, int, error) {
	system, err := promptassets.ReadCommitPrompt()
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(system) == "" {
		return nil, 0, fmt.Errorf("commit prompt asset is empty")
	}
	sections := []string{"Generate a Git commit message for the following changes."}
	previous := truncateCommitMessagePreviousCommits(req.GetPreviousCommitMessages(), commitMessagePreviousCommitLimit)
	if len(previous) > 0 {
		sections = append(sections, "Recent commit messages:\n"+strings.Join(previous, "\n"))
	}
	if contextJSON, err := marshalCommitMessageExplicitContext(req.GetExplicitContext()); err != nil {
		return nil, 0, err
	} else if contextJSON != "" {
		sections = append(sections, "Explicit context:\n"+contextJSON)
	}
	// Manifest tính từ diffs GỐC (chưa truncate) để các file bị cắt khỏi budget vẫn
	// hiện diện trong scope — bullets của model có neo thực tế thay vì suy đoán.
	changedFiles := summarizeCommitMessageFiles(req.GetDiffs())
	if manifest := formatCommitMessageManifest(changedFiles); manifest != "" {
		sections = append(sections, manifest)
	}
	diffSections := make([]string, 0, len(diffs))
	for index, diff := range diffs {
		diffSections = append(diffSections, fmt.Sprintf("--- Diff %d ---\n%s", index+1, diff))
	}
	sections = append(sections, "Diffs:\n"+strings.Join(diffSections, "\n\n"))
	return []modeladapter.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: strings.Join(sections, "\n\n")},
	}, len(changedFiles), nil
}

// commitMessageOutputBudget nới dần output budget theo số file thay đổi, đủ cho
// body 3-8 bullets kể cả khi model tiêu token cho reasoning; có trần cứng.
func commitMessageOutputBudget(changedFileCount int) int {
	if changedFileCount < 0 {
		changedFileCount = 0
	}
	budget := commitMessageBaseOutputTokens + changedFileCount*commitMessagePerFileOutputTokens
	if budget > commitMessageMaxOutputTokens {
		return commitMessageMaxOutputTokens
	}
	return budget
}

type commitMessageFileChange struct {
	Path      string
	Additions int
	Deletions int
}

// summarizeCommitMessageFiles quét header "diff --git a/... b/..." và đếm dòng +/-
// của từng file trên toàn bộ diffs (kể cả diff sẽ bị truncate sau này).
func summarizeCommitMessageFiles(diffs []string) []commitMessageFileChange {
	var files []commitMessageFileChange
	indexes := make(map[string]int)
	current := -1
	for _, diff := range diffs {
		for _, line := range strings.Split(diff, "\n") {
			if path, ok := commitMessageDiffHeaderPath(line); ok {
				if idx, exists := indexes[path]; exists {
					current = idx
					continue
				}
				indexes[path] = len(files)
				files = append(files, commitMessageFileChange{Path: path})
				current = len(files) - 1
				continue
			}
			if current < 0 {
				continue
			}
			switch {
			case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			case strings.HasPrefix(line, "+"):
				files[current].Additions++
			case strings.HasPrefix(line, "-"):
				files[current].Deletions++
			}
		}
	}
	return files
}

// commitMessageDiffHeaderPath trích phía "b/" của header diff --git.
// Best-effort với path chứa khoảng trắng (git quote những path đó); manifest
// chỉ dùng để mô tả scope nên tên hiển thị không cần chính xác tuyệt đối.
func commitMessageDiffHeaderPath(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "diff --git ") {
		return "", false
	}
	parts := strings.Fields(strings.TrimPrefix(trimmed, "diff --git "))
	if len(parts) == 0 {
		return "", false
	}
	path := parts[len(parts)-1]
	path = strings.TrimPrefix(path, "b/")
	if path == "" {
		return "", false
	}
	return path, true
}

func formatCommitMessageManifest(files []commitMessageFileChange) string {
	if len(files) == 0 {
		return ""
	}
	shown := files
	omitted := 0
	if len(files) > commitMessageManifestMaxFiles {
		omitted = len(files) - commitMessageManifestMaxFiles
		shown = files[:commitMessageManifestMaxFiles]
	}
	lines := make([]string, 0, len(shown)+2)
	lines = append(lines, fmt.Sprintf("Files changed (%d):", len(files)))
	for _, file := range shown {
		lines = append(lines, fmt.Sprintf("- %s (+%d/-%d)", file.Path, file.Additions, file.Deletions))
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("- ... and %d more files", omitted))
	}
	return strings.Join(lines, "\n")
}

func (service *Service) commitMessageHistoryRoot() string {
	if service == nil || service.store == nil {
		return ""
	}
	return strings.TrimSpace(service.store.HistoryDir())
}

// resolveCommitMessageModelID chọn model cho tác vụ phụ (commit message, optimize prompt)
// theo chain ưu tiên:
//  1. commitMessageModel       — user cấu hình tường minh trong config (model ID bất kỳ)
//  2. explorer_override_live   — snapshot subagent_model_overrides mới nhất từ client,
//     resolve đúng chain mà Task subagent routing dùng (explore -> generalPurpose)
//  3. explorer_override        — lastExplorerModelHash đã persist (rơi lại khi app vừa
//     restart, chưa có run_request/prewarm nào mang overrides)
//  4. last_agent_model_hash    — model chat chính gần nhất (hành vi cũ)
//  5. default_fallback         — để resolver tự chọn channel mặc định
//
// Ba giá trị trả về: channel ID đã validate, tên nguồn, giá trị gốc của nguồn.
func (service *Service) resolveCommitMessageModelID(ctx context.Context) (string, string, string) {
	if service == nil || service.modelMemory == nil || service.resolver == nil {
		return "", "default_fallback", ""
	}
	candidates := []struct {
		source string
		value  string
		// strict=true yêu cầu channel resolve ra phải đúng ID đã lưu (hash bền vững);
		// giá trị config tường minh được phép là model ID resolve sang channel bất kỳ.
		strict bool
	}{
		{source: "commit_message_config", value: strings.TrimSpace(service.modelMemory.CommitMessageModel()), strict: false},
		{source: "explorer_override_live", value: service.liveExplorerModelID(), strict: false},
		{source: "explorer_override", value: strings.TrimSpace(service.modelMemory.LastExplorerModelHash()), strict: true},
		{source: "last_agent_model_hash", value: strings.TrimSpace(service.modelMemory.LastAgentModelHash()), strict: true},
	}
	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		channel, err := service.resolver.SelectChannelForModel(ctx, candidate.value)
		if err != nil || channel == nil || strings.TrimSpace(channel.ID) == "" {
			if err != nil {
				log.Printf("forwarder commit message ignored candidate source=%s value=%s error=%v", candidate.source, candidate.value, err)
			}
			continue
		}
		if candidate.strict && strings.TrimSpace(channel.ID) != candidate.value {
			log.Printf("forwarder commit message ignored stale candidate source=%s value=%s resolved_channel=%s", candidate.source, candidate.value, strings.TrimSpace(channel.ID))
			continue
		}
		return strings.TrimSpace(channel.ID), candidate.source, candidate.value
	}
	return "", "default_fallback", ""
}

func recordCommitMessageIncomingRequest(recorder *commitMessageLogRecorder, requestID string, request *aiserverv1.WriteGitCommitMessageRequest) error {
	_, err := recorder.appendEvent("incoming_request", map[string]any{
		"request_id": requestID,
	})
	return err
}

func commitMessageConnectError(recorder *commitMessageLogRecorder, code connect.Code, err error) error {
	if err == nil {
		err = fmt.Errorf("commit message generation failed")
	}
	if _, logErr := recorder.appendEvent("error", map[string]any{
		"code":  code.String(),
		"error": err.Error(),
	}); logErr != nil {
		log.Printf("forwarder failed to write commit message error log error=%v original_error=%v", logErr, err)
	}
	return connect.NewError(code, err)
}

func truncateCommitMessageDiffs(input []string, totalLimit int, singleLimit int) []string {
	result := make([]string, 0, len(input))
	remaining := totalLimit
	for _, raw := range input {
		diff := strings.TrimSpace(raw)
		if diff == "" || remaining <= 0 {
			continue
		}
		limit := singleLimit
		if remaining < limit {
			limit = remaining
		}
		truncated := truncateCommitMessageText(diff, limit)
		if truncated == "" {
			continue
		}
		result = append(result, truncated)
		remaining -= len([]rune(truncated))
	}
	return result
}

func truncateCommitMessagePreviousCommits(input []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	result := make([]string, 0, limit)
	for _, raw := range input {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		result = append(result, "- "+value)
		if len(result) >= limit {
			break
		}
	}
	return result
}

func marshalCommitMessageExplicitContext(contextValue *aiserverv1.ExplicitContext) (string, error) {
	if contextValue == nil {
		return "", nil
	}
	payload, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(contextValue)
	if err != nil {
		return "", fmt.Errorf("marshal explicit context failed: %w", err)
	}
	return truncateCommitMessageText(string(payload), commitMessageExplicitContextLimit), nil
}

func truncateCommitMessageText(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || limit <= 0 {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n...[truncated]"
}

func cleanGeneratedCommitMessage(value string) string {
	result := strings.TrimSpace(value)
	result = stripCommitMessageCodeFence(result)
	prefixes := []string{
		"commit message:",
		"git commit message:",
		"message:",
	}
	for {
		lower := strings.ToLower(strings.TrimSpace(result))
		matched := ""
		for _, prefix := range prefixes {
			if strings.HasPrefix(lower, prefix) {
				matched = prefix
				break
			}
		}
		if matched == "" {
			break
		}
		result = strings.TrimSpace(result[len(matched):])
	}
	result = normalizeCommitMessageFormatting(result)
	return strings.TrimSpace(result)
}

func normalizeCommitMessageFormatting(value string) string {
	rawLines := strings.Split(value, "\n")
	var cleanedLines []string
	for _, l := range rawLines {
		cleanedLines = append(cleanedLines, strings.TrimRight(l, "\r"))
	}

	var subjectParts []string
	bodyStartIndex := len(cleanedLines)

	for i, line := range cleanedLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			bodyStartIndex = i + 1
			break
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			bodyStartIndex = i
			break
		}
		subjectParts = append(subjectParts, trimmed)
	}

	subject := strings.Join(subjectParts, " ")
	if subject == "" {
		return strings.TrimSpace(value)
	}

	var bodyLines []string
	for i := bodyStartIndex; i < len(cleanedLines); i++ {
		bodyLines = append(bodyLines, cleanedLines[i])
	}
	body := strings.TrimSpace(strings.Join(bodyLines, "\n"))

	if body == "" {
		return subject
	}
	return subject + "\n\n" + body
}

func stripCommitMessageCodeFence(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return trimmed
	}
	lines = lines[1:]
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func firstCommitMessageLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
