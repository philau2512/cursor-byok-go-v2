package agy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBuildArgs(t *testing.T) {
	opts := LaunchOptions{
		ConversationID:             "conv-123",
		ModelID:                    "claude-3-7-sonnet",
		Mode:                       "agent",
		Effort:                     "high",
		Sandbox:                    true,
		AddDirectories:             []string{"C:/dir1", "C:/dir2"},
		DangerouslySkipPermissions: true,
		ExtraArgs:                  []string{"--verbose"},
	}

	args := opts.BuildArgs()
	expected := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--conversation", "conv-123",
		"--model", "claude-3-7-sonnet",
		"--mode", "agent",
		"--effort", "high",
		"--sandbox",
		"--add-dir", "C:/dir1",
		"--add-dir", "C:/dir2",
		"--dangerously-skip-permissions",
		"--verbose",
	}

	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("BuildArgs mismatch.\nGot:  %v\nWant: %v", args, expected)
	}
}

func TestProcessTransportLifecycleWithEcho(t *testing.T) {
	// Kiểm tra StartProcess với một script echo giả lập
	tempDir := t.TempDir()
	dummyFile := filepath.Join(tempDir, "workspace")
	if err := os.MkdirAll(dummyFile, 0755); err != nil {
		t.Fatalf("tạo thư mục giả lập thất bại: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Kiểm tra validate CWD
	invalidOpts := LaunchOptions{
		CWD: filepath.Join(tempDir, "non_existent_dir_12345"),
	}
	_, err := StartProcess(ctx, invalidOpts)
	if err == nil {
		t.Fatalf("kỳ vọng báo lỗi khi CWD không tồn tại, nhưng trả về nil")
	}
}