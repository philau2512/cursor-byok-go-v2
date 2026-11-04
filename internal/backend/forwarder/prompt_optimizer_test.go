package forwarder

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPromptOptimizeHTTPHandler(t *testing.T) {
	service := &Service{}
	handler := service.PromptOptimizeHTTPHandler()

	// 1. Test CORS OPTIONS
	reqOpt := httptest.NewRequest(http.MethodOptions, "/api/prompt/optimize", nil)
	recOpt := httptest.NewRecorder()
	handler.ServeHTTP(recOpt, reqOpt)
	if recOpt.Code != http.StatusOK {
		t.Errorf("expected 200 for OPTIONS, got %d", recOpt.Code)
	}
	if recOpt.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("missing CORS header")
	}

	// 2. Test Invalid JSON
	reqBad := httptest.NewRequest(http.MethodPost, "/api/prompt/optimize", bytes.NewBufferString("not-json"))
	recBad := httptest.NewRecorder()
	handler.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad json, got %d", recBad.Code)
	}

	// 3. Test empty prompt error
	body, _ := json.Marshal(PromptOptimizeRequest{Prompt: "   "})
	reqEmpty := httptest.NewRequest(http.MethodPost, "/api/prompt/optimize", bytes.NewBuffer(body))
	recEmpty := httptest.NewRecorder()
	handler.ServeHTTP(recEmpty, reqEmpty)
	if recEmpty.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when provider uninitialized/empty, got %d", recEmpty.Code)
	}
}

func TestStripPromptOptimizerCodeFence(t *testing.T) {
	raw := "```markdown\n# Objective\nDo something\n```"
	stripped := stripPromptOptimizerCodeFence(raw)
	expected := "# Objective\nDo something"
	if stripped != expected {
		t.Errorf("expected %q, got %q", expected, stripped)
	}

	raw2 := "```\nPlain text\n```"
	stripped2 := stripPromptOptimizerCodeFence(raw2)
	expected2 := "Plain text"
	if stripped2 != expected2 {
		t.Errorf("expected %q, got %q", expected2, stripped2)
	}
}
