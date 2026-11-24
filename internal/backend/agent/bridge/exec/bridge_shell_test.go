package execbridge

import (
	"testing"

	runtimecore "cursor/internal/backend/agent/core"
)

func TestEnrichCommandForStreaming(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "python script execution preserved without env injection",
			input:    ".\\.venv\\Scripts\\python.exe manage.py test apps --no-input",
			expected: ".\\.venv\\Scripts\\python.exe manage.py test apps --no-input",
		},
		{
			name:     "node script execution preserved without env injection",
			input:    "node server.js",
			expected: "node server.js",
		},
		{
			name:     "trims surrounding whitespace",
			input:    "  git status  ",
			expected: "git status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enrichCommandForStreaming(tt.input)
			if got != tt.expected {
				t.Errorf("enrichCommandForStreaming(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestOpenShellEnrichesCommand(t *testing.T) {
	bridge := NewBridge()
	toolCall := runtimecore.ToolInvocation{
		CallID:   "call-shell-1",
		ToolName: "Shell",
		ArgsJSON: []byte(`{"command": "  .\\.venv\\Scripts\\python.exe manage.py test  "}`),
	}

	msg, pending, err := bridge.OpenExec(OpenExecContext{}, toolCall)
	if err != nil {
		t.Fatalf("OpenExec() error = %v", err)
	}

	shellArgs := msg.GetExecServerMessage().GetShellStreamArgs()
	if shellArgs == nil {
		t.Fatal("expected ShellStreamArgs message")
	}

	if got, want := shellArgs.GetCommand(), ".\\.venv\\Scripts\\python.exe manage.py test"; got != want {
		t.Fatalf("ShellStreamArgs.Command = %q, want %q", got, want)
	}
	if pending.ExecKind != "shell" {
		t.Fatalf("pending ExecKind = %q, want shell", pending.ExecKind)
	}
}

func TestDefaultShellTimeout(t *testing.T) {
	if timeout := defaultShellTimeout(shellResultArgs{Command: "python test.py"}); timeout != 30000 {
		t.Fatalf("defaultShellTimeout() = %d, want 30000", timeout)
	}
}
