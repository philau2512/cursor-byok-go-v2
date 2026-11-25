# RESUME — sync proto Cursor 3.32.4 (nhánh `feat/sync-client-proto-v3.32`)

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
   protoc trong `generate:proto` (build/Taskfile.yml, làm y như `byok_local_v1.proto`).
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
