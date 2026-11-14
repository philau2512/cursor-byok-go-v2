package forwarder

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"cursor/gen/agentv1"
	"cursor/internal/backend/agy"
)

// isAgyRequest kiểm tra xem model có thuộc định tuyến AGY hay không.
func (service *Service) isAgyRequest(modelID string) bool {
	trimmed := strings.TrimSpace(modelID)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "agy/") || strings.HasPrefix(lower, "antigravity/") {
		return true
	}
	if service != nil && service.resolver != nil {
		channel, err := service.resolver.SelectChannelForModel(context.Background(), trimmed)
		if err == nil && channel != nil && strings.EqualFold(channel.Provider, "agy") {
			return true
		}
	}
	return false
}

// driveAgy khởi chạy phiên làm việc với tiến trình con agy CLI và truyền stream về Cursor client.
func (service *Service) driveAgy(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}

	stream.mu.Lock()
	if stream.ProviderActive || isTerminalStreamStatus(stream.Status) {
		stream.mu.Unlock()
		return nil
	}
	stream.ProviderPassCount++
	stream.Status = StreamStatusStreaming
	stream.PendingProviderAction = providerActionNone
	stream.CurrentModelCallID = uuid.NewString()
	stream.CurrentProviderToken++
	currentToken := stream.CurrentProviderToken

	requestID := stream.RequestID
	conversationID := stream.ConversationID
	modelID := stream.ModelID
	modelCallID := stream.CurrentModelCallID
	mode := stream.Mode.String()
	effort := stream.ThinkingEffort
	userPrompt := stream.LatestUserText

	workspacePath := firstWorkspacePath(stream.WorkspacePaths)
	if workspacePath == "" {
		workspacePath = "."
	}
	stream.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	stream.mu.Lock()
	stream.ProviderActive = true
	stream.ProviderCancel = cancel
	stream.mu.Unlock()

	service.setTurnPhase(stream, TurnPhaseProviderRunning)

	cleanModelID := strings.TrimPrefix(strings.ToLower(modelID), "agy/")
	cleanModelID = strings.TrimPrefix(cleanModelID, "antigravity/")

	go service.runAgyStream(stream, currentToken, ctx, agy.LaunchOptions{
		CWD:                        workspacePath,
		ConversationID:             conversationID,
		ModelID:                    cleanModelID,
		Mode:                       mode,
		Effort:                     effort,
		DangerouslySkipPermissions: true,
	}, requestID, conversationID, modelCallID, userPrompt)

	return nil
}

// runAgyStream kết nối Transport và Translator, lắng nghe stdout và gửi message vào broker.
func (service *Service) runAgyStream(
	stream *ActiveStream,
	token uint64,
	ctx context.Context,
	opts agy.LaunchOptions,
	requestID string,
	conversationID string,
	modelCallID string,
	userPrompt string,
) {
	defer func() {
		stream.mu.Lock()
		stream.ProviderActive = false
		stream.ProviderCancel = nil
		stream.mu.Unlock()
		service.setTurnPhase(stream, TurnPhaseCompleted)
	}()

	transport, err := agy.StartProcess(ctx, opts)
	if err != nil {
		if service.debug != nil {
			service.debug.LogProvider(context.Background(), requestID, conversationID, "agy_start_failed", map[string]any{
				"error": err.Error(),
			})
		}
		_ = service.failStream(stream, "agy_start_error", err)
		return
	}
	defer transport.Close()

	// Khởi tạo bộ dịch protocol
	translator := agy.NewTranslator(requestID, conversationID, modelCallID, func(msg *agentv1.AgentServerMessage) error {
		return service.broker.Publish(requestID, StreamEvent{Message: msg})
	})

	// Gửi tin nhắn user prompt vào stdin của AGY
	if err := transport.SendUserMessage(ctx, userPrompt); err != nil {
		_ = service.failStream(stream, "agy_write_error", err)
		return
	}

	// Lắng nghe dòng sự kiện từ AGY CLI
	for {
		select {
		case <-ctx.Done():
			return
		case raw, ok := <-transport.Messages():
			if !ok {
				// Stdout đã đóng, tiến trình hoàn thành
				return
			}
			if err := translator.Translate(raw); err != nil {
				if service.debug != nil {
					service.debug.LogProvider(context.Background(), requestID, conversationID, "agy_translate_error", map[string]any{
						"error": err.Error(),
						"raw":   raw,
					})
				}
			}
		}
	}
}