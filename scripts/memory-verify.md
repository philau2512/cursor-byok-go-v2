# Hướng dẫn verify Project Memory trên app thật

Nguồn sự thật hành vi: `go test ./internal/backend/forwarder/ -run TestAcceptance -v` (6 test AC1–AC6).
Script này verify trên máy thật với provider thật — mỗi bước ghi rõ bằng chứng kỳ vọng.

## Bước 1 — Build & chạy

```bash
go build ./... && task dev   # hoặc build binary theo build.bat/ps1
```

Bật debug artifacts (tùy chọn nhưng khuyến nghị): thêm `log: true` vào
`~/.cursor-local-assistant-v2/config.yaml` (hot-reload, không cần khởi động lại).

## Bước 2 — Save qua tool (AC1)

1. Mở Cursor, chat trong một project bất kỳ (local mode bật).
2. Nói: "nhớ là build backend bằng ./gradlew.bat compileJava -q, lần sau khỏi hỏi lại".
3. Kỳ vọng: agent gọi tool `SaveMemory` — hiển thị dạng **card MCP `byok-memory:SaveMemory`** (tên tool + args + kết quả text; result lỗi sẽ có cờ IsError đỏ).
4. Bằng chứng file:
   `~/.cursor-local-assistant-v2/memories/projects/<slug>/project_build_configuration/gradle-compile-command.md`
   với frontmatter `origin: "user"`.

## Bước 3 — Recall qua conversation mới (AC1 + AC3)

1. Mở conversation MỚI cùng workspace, hỏi: "build backend thế nào?"
2. Kỳ vọng: model trả lời đúng lệnh mà không cần tra lại; trong request
   artifact (`history/<conversationId>/debug/*.jsonl`, tìm `prompt_context`
   source `project_memories`) có block `<project_memories scope="...">` ĐÚNG 1 LẦN.
3. Cache check: gửi 2 tin nhắn liên tiếp không thay đổi memory → block
   `project_memories` KHÔNG xuất hiện thêm bản thứ hai (history prefix ổn định).

## Bước 4 — Auto-capture từ compaction (AC4)

1. Chat chuỗi dài tới khi auto-compaction (hoặc gõ lệnh compact thủ công nếu UI có).
2. Kỳ vọng: entries mới `origin: "auto"` trong store (title tiền tố
   `Constraint:` / `Dead end:` / `Directive:` / `Target:`).
3. Debug log event: `memory_auto_capture {"captured":N,"skipped":M}`.
4. Chạy compact LẦN HAI cùng nội dung → `captured: 0` (idempotent).

## Bước 5 — Kill-switch

1. Đặt `memoryEnabled: false` trong config.yaml (hot-reload).
2. Conversation mới: request artifact không còn block `project_memories`;
   nếu model gọi `SaveMemory`/`SearchMemory` → result `Project memory is disabled.`;
   compaction không ghi entry auto.
3. Bật lại `true` → mọi đường hoạt động trở lại.

## Gỡ rối nhanh

- Không thấy tool `SaveMemory` trong catalog → rebuild binary (assets nhúng `prompt/tools.json`).
- Memory không xuất hiện ở conversation mới → kiểm tra `WorkspacePaths` được client gửi lên
  (debug event `memory_overview_built`/block rỗng nghĩa là slug khác — mở store dir xem slug thật).
- Muốn xóa trí nhớ của một project: xóa thư mục `<slug>` trong `memories/projects/` — hết, không để lại gì.
