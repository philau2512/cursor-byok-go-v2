package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

const promptOptimizerSystemPrompt = `You are a Universal Prompt Optimizer.

Your ONLY job is to transform the user's raw prompt into a clear, precise, and actionable prompt while preserving the original intent.

CRITICAL DIRECTIVES:
- DO NOT EXECUTE the task described in the prompt.
- DO NOT write implementation code, modify project files, create pull requests, or perform the requested work.
- DO NOT run build, test, or deployment commands to solve the user's task.
- Your sole output is the rewritten/optimized prompt itself.

Default Mode Rules (No --code):
1. Optimize using only the user's raw prompt. Do not inspect, search, or infer implementation details from any external codebase.
2. Do not introduce fake or assumed filenames, classes, functions, APIs, or database tables unless the user explicitly provided them.
3. Understand the user's actual intent. Extract implicit requirements without inventing fake features.
4. Completeness & Precision: Make explicit relevant goals, entities, constraints, business rules, edge cases, and expected outcomes.
5. Preserve genuine ambiguity; do not silently guess critical missing decisions.
6. For coding-related tasks, organize the optimized prompt cleanly with:
   - Objective / Goal
   - Context & Affected Areas (derived strictly from what the user mentioned)
   - Current vs. Expected Behavior
   - Business Rules & Requirements
   - Constraints & Edge Cases
   - Verification & Acceptance Criteria
7. Output format:
   - Output ONLY the optimized prompt directly copyable and ready to feed into an AI agent.
   - Do NOT include conversational filler, meta-announcements, or intro like "Here is your optimized prompt:".`

type PromptOptimizeRequest struct {
	Prompt string `json:"prompt"`
}

type PromptOptimizeResponse struct {
	OptimizedPrompt string `json:"optimized_prompt,omitempty"`
	Error           string `json:"error,omitempty"`
}

// OptimizePrompt 使用 Prompt Optimizer 提示词模板将用户的原始 prompt 转换为高清晰度、可执行的 prompt。
func (service *Service) OptimizePrompt(ctx context.Context, rawPrompt string) (string, error) {
	if service == nil || service.provider == nil {
		return "", fmt.Errorf("provider gateway is not initialized")
	}
	cleanPrompt := strings.TrimSpace(rawPrompt)
	if cleanPrompt == "" {
		return "", fmt.Errorf("prompt is empty")
	}

	requestID := "prompt-opt-" + uuid.NewString()
	modelCallID := requestID + "-model"
	modelID, _, _ := service.resolveCommitMessageModelID(ctx)

	messages := []modeladapter.Message{
		{Role: "system", Content: promptOptimizerSystemPrompt},
		{Role: "user", Content: cleanPrompt},
	}

	accumulated := ""
	artifactPaths := &modeladapter.LLMArtifactPaths{}
	err := service.provider.StartStream(ctx, ProviderRequest{
		RequestID:      requestID,
		RunID:          requestID,
		ModelCallID:    modelCallID,
		ModelID:        modelID,
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		Messages:       messages,
		Tools:          nil,
		MaxTokens:      4096,
		CompileSummary: "optimize user prompt",
		ArtifactPaths:  artifactPaths,
	}, func(event modeladapter.ModelEvent) error {
		if event.Kind == modeladapter.ModelEventKindTextDelta {
			accumulated += event.Text
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("provider stream failed: %w", err)
	}

	result := strings.TrimSpace(accumulated)
	result = stripPromptOptimizerCodeFence(result)
	return strings.TrimSpace(result), nil
}

func stripPromptOptimizerCodeFence(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "```markdown") && strings.HasSuffix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```markdown")
		trimmed = strings.TrimSuffix(trimmed, "```")
	} else if strings.HasPrefix(trimmed, "```") && strings.HasSuffix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
	}
	return strings.TrimSpace(trimmed)
}

// PromptOptimizeHTTPHandler 提供供前端调用的 REST HTTP 处理程序。
func (service *Service) PromptOptimizeHTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req PromptOptimizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(PromptOptimizeResponse{Error: "Invalid JSON request"})
			return
		}

		optimized, err := service.OptimizePrompt(r.Context(), req.Prompt)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(PromptOptimizeResponse{Error: err.Error()})
			return
		}

		_ = json.NewEncoder(w).Encode(PromptOptimizeResponse{OptimizedPrompt: optimized})
	})
}
