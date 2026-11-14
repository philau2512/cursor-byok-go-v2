package agy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// maxScanLineCapacity là kích thước bộ đệm tối đa cho 1 dòng JSON từ AGY (16MB).
	maxScanLineCapacity = 16 * 1024 * 1024
	// initialScanBufferSize kích thước ban đầu (64KB).
	initialScanBufferSize = 64 * 1024
	// processShutdownGracePeriod thời gian chờ tiến trình tự đóng trước khi SIGKILL.
	processShutdownGracePeriod = 2 * time.Second
)

// InboundUserMessage đại diện cho payload gửi vào stdin của agy CLI.
type InboundUserMessage struct {
	Event   string          `json:"event"`
	Message UserRoleMessage `json:"message"`
}

type UserRoleMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Transport chịu trách nhiệm kết nối và truyền nhận I/O với tiến trình agy CLI.
type Transport interface {
	// Messages trả về channel nhận các bản tin JSON stream từ stdout của agy.
	Messages() <-chan map[string]any

	// SendUserMessage gửi một tin nhắn người dùng vào stdin của agy.
	SendUserMessage(ctx context.Context, text string) error

	// SendRawMessage gửi bất kỳ đối tượng JSON nào vào stdin của agy.
	SendRawMessage(ctx context.Context, payload any) error

	// StderrOutput trả về toàn bộ log stderr đã thu thập được từ tiến trình.
	StderrOutput() string

	// Close ngắt kết nối an toàn và giải phóng tiến trình con.
	Close() error

	// Wait hoàn thành khi tiến trình agy kết thúc.
	Wait() <-chan error
}

type processTransport struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	messages  chan map[string]any
	done      chan error
	closeOnce sync.Once
	mu        sync.Mutex
	stderrBuf strings.Builder
}

// StartProcess khởi chạy tiến trình agy con và bắt đầu luồng đọc/ghi I/O bất đồng bộ.
func StartProcess(ctx context.Context, opts LaunchOptions) (Transport, error) {
	binary := opts.BinaryPath
	if strings.TrimSpace(binary) == "" {
		var err error
		binary, err = exec.LookPath("agy")
		if err != nil {
			return nil, fmt.Errorf("không tìm thấy binary 'agy' trong PATH hệ thống: %w", err)
		}
	}

	if info, err := os.Stat(opts.CWD); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("thư mục làm việc CWD không tồn tại hoặc không hợp lệ %q: %w", opts.CWD, err)
	}

	cmd := exec.CommandContext(ctx, binary, opts.BuildArgs()...)
	cmd.Dir = opts.CWD

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("không thể mở stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return nil, fmt.Errorf("không thể mở stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		return nil, fmt.Errorf("không thể mở stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return nil, fmt.Errorf("khởi chạy tiến trình agy thất bại: %w", err)
	}

	pt := &processTransport{
		cmd:      cmd,
		stdin:    stdinPipe,
		messages: make(chan map[string]any, 256),
		done:     make(chan error, 1),
	}

	// Đọc stderr để lưu vết chẩn đoán lỗi
	go pt.readStderr(stderrPipe)

	// Đọc stdout từng dòng stream-json
	go pt.readStdout(stdoutPipe)

	// Chờ tiến trình kết thúc
	go pt.waitProcess()

	return pt, nil
}

func (pt *processTransport) Messages() <-chan map[string]any {
	return pt.messages
}

func (pt *processTransport) SendUserMessage(ctx context.Context, text string) error {
	msg := InboundUserMessage{
		Event: "user",
		Message: UserRoleMessage{
			Role:    "user",
			Content: text,
		},
	}
	return pt.SendRawMessage(ctx, msg)
}

func (pt *processTransport) SendRawMessage(ctx context.Context, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal message failed: %w", err)
	}

	pt.mu.Lock()
	defer pt.mu.Unlock()

	if pt.stdin == nil {
		return errors.New("stdin is already closed")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if _, err := pt.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("ghi stdin thất bại: %w", err)
	}
	return nil
}

func (pt *processTransport) readStdout(r io.Reader) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, initialScanBufferSize)
	scanner.Buffer(buf, maxScanLineCapacity)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err == nil {
			pt.messages <- payload
		} else {
			pt.mu.Lock()
			pt.stderrBuf.WriteString(fmt.Sprintf("[stdout-unmarshal-err] %s: %s\n", err.Error(), line))
			pt.mu.Unlock()
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		pt.mu.Lock()
		pt.stderrBuf.WriteString(fmt.Sprintf("[scanner-err] %s\n", err.Error()))
		pt.mu.Unlock()
	}
	close(pt.messages)
}

func (pt *processTransport) readStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		text := scanner.Text()
		pt.mu.Lock()
		pt.stderrBuf.WriteString(text)
		pt.stderrBuf.WriteByte('\n')
		pt.mu.Unlock()
	}
}

func (pt *processTransport) waitProcess() {
	err := pt.cmd.Wait()
	pt.done <- err
	close(pt.done)
}

func (pt *processTransport) StderrOutput() string {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return pt.stderrBuf.String()
}

func (pt *processTransport) Wait() <-chan error {
	return pt.done
}

func (pt *processTransport) Close() error {
	var closeErr error
	pt.closeOnce.Do(func() {
		pt.mu.Lock()
		if pt.stdin != nil {
			_ = pt.stdin.Close()
			pt.stdin = nil
		}
		pt.mu.Unlock()

		if pt.cmd != nil && pt.cmd.Process != nil {
			select {
			case <-pt.done:
			case <-time.After(processShutdownGracePeriod):
				_ = pt.cmd.Process.Kill()
			}
		}
	})
	return closeErr
}