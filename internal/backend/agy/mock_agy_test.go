package agy_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"cursor/internal/backend/agy"
)

// createMockAgyBinary biên dịch một binary test tạm thời hoạt động như agy CLI.
func createMockAgyBinary(t *testing.T) string {
	tempDir := t.TempDir()
	mockSrc := filepath.Join(tempDir, "mock_agy.go")
	mockBin := filepath.Join(tempDir, "mock_agy.exe")

	code := `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	// 1. Phát event init
	fmt.Println("{\"event\":\"init\",\"conversation_id\":\"conv-mock-123\"}")

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		if msg["event"] == "user" {
			// 2. Phát thinking
			fmt.Println("{\"event\":\"step_update\",\"step_update\":{\"step_type\":\"thinking\",\"thinking_delta\":\"I am analyzing the repository...\"}}")
			time.Sleep(10 * time.Millisecond)

			// 3. Phát tool execution
			fmt.Println("{\"event\":\"step_update\",\"step_update\":{\"step_type\":\"tool\",\"tool_name\":\"read_file\",\"tool_id\":\"call-1\",\"status\":\"running\",\"parameters\":{\"path\":\"main.go\"}}}")
			time.Sleep(10 * time.Millisecond)
			fmt.Println("{\"event\":\"step_update\",\"step_update\":{\"step_type\":\"tool\",\"tool_name\":\"read_file\",\"tool_id\":\"call-1\",\"status\":\"success\"}}")
			time.Sleep(10 * time.Millisecond)

			// 4. Phát agent response
			fmt.Println("{\"event\":\"step_update\",\"step_update\":{\"step_type\":\"agent_response\",\"text_delta\":\"Here is the solution to your task.\"}}")
			time.Sleep(10 * time.Millisecond)

			// 5. Phát result turn ended
			fmt.Println("{\"event\":\"result\",\"status\":\"success\",\"usage\":{\"input_tokens\":500,\"output_tokens\":120}}")
			return
		}
	}
}
`
	if err := os.WriteFile(mockSrc, []byte(code), 0644); err != nil {
		t.Fatalf("ghi mock_agy.go thất bại: %v", err)
	}

	buildCmd := exec.Command("go", "build", "-o", mockBin, mockSrc)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		// Thử tìm go qua full path nếu go không trong PATH môi trường test
		goExec := "C:\\Program Files\\Go\\bin\\go.exe"
		buildCmd = exec.Command(goExec, "build", "-o", mockBin, mockSrc)
		if out2, err2 := buildCmd.CombinedOutput(); err2 != nil {
			t.Fatalf("biên dịch mock_agy thất bại: %v (out: %s, out2: %s)", err, string(out), string(out2))
		}
	}

	return mockBin
}

func TestMockAgyProcessTransport(t *testing.T) {
	mockBin := createMockAgyBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := agy.LaunchOptions{
		BinaryPath: mockBin,
		CWD:        t.TempDir(),
		ModelID:    "gemini-2.5-pro",
	}

	transport, err := agy.StartProcess(ctx, opts)
	if err != nil {
		t.Fatalf("StartProcess thất bại: %v", err)
	}
	defer transport.Close()

	if err := transport.SendUserMessage(ctx, "Please fix bug in main.go"); err != nil {
		t.Fatalf("SendUserMessage thất bại: %v", err)
	}

	receivedEvents := 0
	for msg := range transport.Messages() {
		receivedEvents++
		event, _ := msg["event"].(string)
		if event == "result" {
			break
		}
	}

	if receivedEvents < 4 {
		t.Errorf("Kỳ vọng nhận ít nhất 4 sự kiện, thực tế nhận: %d", receivedEvents)
	}
}