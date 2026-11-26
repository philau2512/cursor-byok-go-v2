# RESUME — sync proto Cursor 3.32.4 (nhánh `feat/sync-client-proto-v3.32`)

**CẬP NHẬT 2026-11-26: TOÀN BỘ 4 BƯỚC DƯỚI ĐÃ XONG.** Snapshot `proto/` + `gen/` đã
là bản 3.32.4 thật (kèm compat file), build/vet/test/check_proto_sync đều xanh.
Chi tiết mục "Kết quả cuối (2026-11-26)" ở cuối file. Phần còn lại duy nhất:
smoke test tay với client cài thật (mục 4 cũ) và commit khi user yêu cầu.

Trạng thái chốt ở commit **`f8654dc`** (2026-11-25). Build + test xanh ở commit đó.
**proto snapshot và `gen/` vẫn là bản CŨ** — dùng client bản cũ hoàn toàn an toàn.

## Đã xong

- `proto/ext_tool/compact.go`: parse descriptor nén của Cursor ≥3.30 (`static $(){return["Name|1 field 9...", refs]}`), scope prefix/package theo webpack module, bảng tên qualified làm ground truth, detector module factory shape mới `3917(t,e,r){...}`.
- `proto/ext_tool/merge.go`: merge định nghĩa từ nhiều bundle; đã sửa **bug trùng tên field** (`CreatePlanResult.plan_uri` #3 vs #4 do hai biến thể success/error và accepted/rejected cùng tên type) + test hồi quy `TestMergeBundlesDropsDuplicateFieldName`.
- `proto/ext_tool/main.go`: `-input` lặp nhiều lần, `ExtractProtosFromFiles`.
- Kênh chat bidi `run_bidi.go` + kênh `local_subscription_tools.go` (+ test) — route cũ BidiAppend/RunSSE vẫn giữ.
- `tmp/breakcheck.sh`: đã sửa regex symbol bắt cả const block gofmt căn padding.

## Bằng chứng mới nhất (merged5, 16:5x→17:0x)

Lệnh extract 4 bundle (bundle thật máy này, Cursor 3.32.4):

```bash
EXT="C:/Users/ADMIN/AppData/Local/Programs/cursor/resources/app/extensions"
go run ./proto/ext_tool \
  -input "$EXT/cursor-always-local/dist/main.js" \
  -input "$EXT/cursor-agent-host/dist/main.js" \
  -input "$EXT/cursor-agent-exec/dist/main.js" \
  -input "$EXT/cursor-local-agent-runtime/dist/main.js" \
  -output "C:/PROJECTS/cursor-byok/cursor-byok-go-v2/tmp/merged5" \
  -skip-format -strict=false

bash tmp/breakcheck.sh tmp/merged5   # -> tmp/breakcheck-merged5.log
```

Kết quả: **7884 message, 831 enum, 15 service**; protoc OK.
BREAK agentv1 = **0**; BREAK aiserverv1 = **21**, chia 2 nhóm:

1. **14 symbol chỉ bị ĐỔI TÊN** (không phải mất): descriptor nén giữ nguyên prefix đầy đủ
   `ERROR_DETAILS_ERROR_CUSTOM_MESSAGE`, `UPLOAD_RESPONSE_STATUS_SUCCESS`... trong khi
   snapshot/`internal/` dùng tên ngắn `ERROR_CUSTOM_MESSAGE`. Chứng cứ: `ErrorDetails`
   (message + nested enum `Error`) vẫn tồn tại trong merged5, field number giống hệt,
   chỉ khác tên value.
2. **7 symbol MẤT THẬT khỏi client 3.32**: `IsConnectedResponse` (`IsConnectedRequest`
   cũng mất nhưng code không tham chiếu nên không vào BREAK),
   `RepositoryStatus{Request,Response}` + nested `RepositoryStatusResponse_Synced(_)`
   wrappers, `BatchRepositoryStatus{Request,Response}` — 0 hit trong CẢ 4 bundle +
   workbench.desktop.main.js. `internal/` vẫn dùng ở:
   `repository_index_handler.go`, `upload_docs_handler.go`, `docs_index_handler.go`,
   `host.go` (route `NetworkService/IsConnected` + mock `aiserver.v1.IsConnectedResponse`),
   `upstream/client.go:454`.

## Việc còn lại — làm theo đúng thứ tự

1. **Normalize tên enum value trong `compact.go`** (`compactNormalizeEnumValues`, dòng ~998):
   cắt prefix `<PARENT>_<ENUM>_` thừa khỏi tên value để gen Go giữ tên cũ. Chạy lại lệnh
   extract trên → `tmp/merged6`, breakcheck phải còn đúng **7** symbol nhóm 2.
2. **Compat file cho 8 type mất thật** (7 symbol BREAK + `IsConnectedRequest`): tạo
   `proto/aiserver_compat_v1.proto`
   (`package aiserver.v1; option go_package = "cursor/gen/aiserverv1;aiserverv1";`) chép
   nguyên khối các message trên từ `proto/aiserver_v1.proto` cũ (kèm nested Synced
   oneof). Không trùng tên với bản extract nên không panic registry. Thêm file vào lệnh
   protoc trong `generate:proto` (build/Taskfile.yml). Tiền lệ `byok_local_v1.proto` đã
   được xóa sau sync 3.32 vì snapshot chính thức chứa đủ message với field number giống
   hệt — xem CHANGELOG 2026-11-26.
3. **Sync thật**: `task sync:proto` (cp from_extensions → proto/, rewrite go_package,
   check_proto_sync, generate). Lưu ý `extract:proto`/script hiện chỉ nhận 1 input —
   hoặc nâng script nhận nhiều `-input`, hoặc chạy tay lệnh merged trước rồi cp vào
   `proto/from_extensions/`.
4. **Verify**: `go build ./...`; `go test ./internal/... ./proto/...`;
   `bash proto/check_proto_sync.sh`; smoke test client cũ (legacy route còn) + client
   3.32 (kênh bidi Run).

## Ghi chú môi trường / lỗi đã gặp

- `placeholders=2` trong log extract là **false positive** (`Unknown unknown`,
  `unknown_tool_error_template` là tên thật upstream) — bỏ qua.
- `cursor-agent-worker` / `cursor-retrieval` KHÔNG có compact descriptor (0 `static $()`),
  khỏi cần merge.
- Sandbox không xoá được file `nul` (Windows giữ tên này làm thiết bị hệ thống) — đã cho
  vào `.gitignore`; muốn xoá hẳn: chạy `cmd //c del \\.\nul` ở shell ngoài sandbox.
- `tmp/` đã gitignore; mọi rác thử nghiệm (merged2..5, scoped-*, reg-*, iofix-*) nằm
  trong `tmp/`, không vào commit.
- Strict mode (`-strict=true`) sẽ FAIL trên merged output vì hard conflict biến thể
  DA/mf khác bundle — đang chạy `-strict=false`; khi nào extractor xử lý xong biến thể
  trùng tên thì mới cân nhắc bật lại strict.
  → ĐÃ XỬ LÝ (2026-11-26): variant gate mức định nghĩa + hạ xung đột field/enum/method
  cross-bundle xuống soft (first-wins, bundle chuẩn đặt đầu), `-strict` bật thường trực
  trong script và pass exit=0.

## Kết quả cuối (2026-11-26)

1. **Enum normalize**: `compactNormalizeEnumValues` cắt prefix `<PARENT>_<ENUM>_` của
   enum lồng; re-extract merged6 → BREAK aiserverv1 còn đúng **7** (đúng dự đoán),
   agentv1 = 0.
2. **Compat**: tạo `proto/aiserver_compat_v1.proto` (IsConnected req/resp,
   RepositoryStatus req/resp + nested Synced oneof, BatchRepositoryStatus req/resp),
   wired vào `generate:proto` trong `build/Taskfile.yml`.
3. **Pipeline thật**: `proto/extract_extensions_proto.sh` nhận nhiều bundle +
   auto-detect compact + Windows root + `BUNDLE_PRIORITY` (cursor-always-local đứng
   đầu để first-wins khớp merged6); Taskfile gọi `bash` tường minh, bỏ precondition
   macOS-only. `task -t build/Taskfile.yml sync:proto` chạy trọn vẹn exit=0 với
   `-strict` (unresolved=0, placeholders=0 sau khi siết `placeholderRe`).
4. **Verify**: fallout compile do field client đổi được xử lý:
   - `agentv1.Diagnostic.severity` giờ là enum → convert số trong
     `internal/backend/agent/bridge/exec/bridge.go` (`DiagnosticSeverity(item.GetSeverity())`).
   - `RequestedModel.is_variant_string_representation` bị client 3.32 xóa → thay bằng
     suy luận cú pháp model_id: helper `requestedModelUsesVariantString` trong
     `internal/backend/forwarder/service.go` (dạng `name[...]` và `name:<effort>`),
     5 call site + 4 fixture test cập nhật theo.
   - `go build ./...`, `go vet ./...` sạch; `go test ./internal/... ./proto/...`
     toàn bộ ok; `bash proto/check_proto_sync.sh` exit=0; đối chiếu symbol
     internal/ dùng vs gen/ mới: missing = **0** cả agentv1 lẫn aiserverv1.
   - `git_forge_v1.proto`/`origin_v1.proto` biến mất khỏi extract (client không còn
     khai): internal/ không import package đó nên an toàn.
5. **Lỗi chat thật của client 3.32 (không phải do proto)**: bản release chết ở
   `[invalid_argument] unsupported client message kind: ` với kind **rỗng**. Ba nguyên
   nhân, cả ba nằm trong internal/, snapshot cũ/mới đều giống nhau nên re-extract
   không hề sửa được:
   - `BidiAppendRequest` có hai ô chở payload: `data` (hex) và `data_binary` (bytes).
     Cả repo chỉ đọc `GetData()` → frame nhị phân parse ra `AgentClientMessage` rỗng →
     kind "" → mọi switch rơi xuống default. Đã thêm
     `protocol.DecodeAgentClientMessageEnvelope(hex, binary)` (dùng ở `Service.BidiAppend`
     và decoder của cursor-proxy-debugger), debug log vẫn giữ bản hex của carrier thật.
   - Frame lạ/rỗng từng là **fatal** ở đường legacy BidiAppend trong khi đường bidi Run
     lại dung sai. Gộp thành predicate `inboundKindCarriesRun`: chỉ frame mang run
     (`run_request`/`prewarm_request`) decode lỗi mới được ngắt stream; frame quản trị
     thì ack + log `inbound_intent_ignored` / `agent_run_inbound_ignored`.
   - Thông báo lỗi in toàn chuỗi rỗng nên không tự giải thích được; giờ fallback sang
     tên wrapper oneof Go (`agentClientMessageCase`).
   Test hồi quy: `internal/backend/forwarder/bidi_append_payload_test.go` (4 case).
   **Việc còn lại của bạn**: chạy app thật với client 3.32 để xác nhận end-to-end.

6. **Sau code review toàn diện (26/11), đã làm nốt 3 việc**:
   - `server.Any("/agent.v1.AgentService/*")` + `agentServiceFallbackAction()`
     (`internal/backend/host.go`): 14 RPC agent.v1 chưa mount từng 404 (lỗi Class A)
     nay trả message rỗng đúng framing và LOG tên procedure ⇒ mỗi lần client chạy
     thật sẽ tự sinh bằng chứng RPC nào cần port thật sự. Test:
     `internal/backend/host_agent_fallback_test.go` (framing 3 kiểu + wildcard không
     che route đã mount).
   - `InjectContextAction` (`internal/backend/forwarder/context_injection.go`):
     payload `user_context`/`system_context` cho run đang chạy nay append thành
     prompt_context entry (cùng cơ chế mode_change, giữ prefix cache); ba event
     `inject_context_applied|empty|dropped` bịt mọi đường im lặng. Test:
     `context_injection_test.go`. `request_context` bên trong user_context CHƯA có
     seam áp dụng — chỉ log `request_context_present`, cần port tiếp nếu traffic
     thật cho thấy client dùng.
   - Siết `requestedModelUsesVariantString` về token canonical
     (`isCanonicalRuntimeThinkingEffortToken`) + `rawHex` log đúng carrier đã decode
     + `conversationActionKind` đủ 15 nhánh. Test:
     `variant_string_inference_test.go`, `internal/backend/agent/protocol/inbound_test.go`.

   Việc còn mở: capture traffic thật (xem log `agent.v1 procedure chưa có handler`)
   để quyết định port RPC nào, và xử lý `goal_continuation_action` (message rỗng,
   hiện chỉ có label debug) nếu client dùng nó làm tín hiệu continue.
