package agy

import (
	"strings"
)

// LaunchOptions định nghĩa các tham số cần thiết khi khởi tạo một tiến trình agy CLI.
type LaunchOptions struct {
	// BinaryPath là đường dẫn tới executable agy. Nếu rỗng, mặc định tìm "agy" trong PATH.
	BinaryPath string

	// CWD là thư mục làm việc của dự án (workspace root directory). Bắt buộc phải tồn tại.
	CWD string

	// ConversationID là định danh phiên làm việc giữa Cursor và AGY.
	ConversationID string

	// ModelID là tên model yêu cầu (vd: "gemini-2.5-pro", "claude-3-7-sonnet").
	ModelID string

	// Mode là chế độ hoạt động của agent: "agent", "plan", "debug", "ask".
	Mode string

	// Effort là mức độ suy nghĩ/reasoning: "low", "medium", "high", "xhigh".
	Effort string

	// Sandbox bật chế độ cách ly an toàn nếu được cấu hình.
	Sandbox bool

	// AddDirectories danh sách các thư mục bổ sung cần cấp quyền cho AGY.
	AddDirectories []string

	// DangerouslySkipPermissions tự động cho phép thực thi lệnh mà không dừng chờ CLI prompt.
	DangerouslySkipPermissions bool

	// ExtraArgs các cờ tùy biến bổ sung khi cần thiết.
	ExtraArgs []string
}

// BuildArgs xây dựng danh sách đối số dòng lệnh cho agy CLI dựa trên cấu hình.
func (opt *LaunchOptions) BuildArgs() []string {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
	}

	if id := strings.TrimSpace(opt.ConversationID); id != "" {
		args = append(args, "--conversation", id)
	}
	if model := strings.TrimSpace(opt.ModelID); model != "" {
		args = append(args, "--model", model)
	}
	if mode := strings.TrimSpace(opt.Mode); mode != "" {
		args = append(args, "--mode", mode)
	}
	if effort := strings.TrimSpace(opt.Effort); effort != "" {
		args = append(args, "--effort", effort)
	}
	if opt.Sandbox {
		args = append(args, "--sandbox")
	}
	for _, dir := range opt.AddDirectories {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			args = append(args, "--add-dir", trimmed)
		}
	}
	if opt.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, opt.ExtraArgs...)
	return args
}