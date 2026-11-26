# Local Changes

> Record local changes by date, newest first. Each change uses a `## YYYY-MM-DD` heading and a separate `###` entry.

## 2026-11-26

### Changed: version bumped to 0.0.51

- Nguồn sự thật `build/config.yml` (`info.version`) và toàn bộ metadata đóng gói đã đồng bộ: `build/windows/info.json`, `build/windows/nsis/wails_tools.nsh`, `build/windows/wails.exe.manifest`, `build/darwin/Info.plist`, `build/darwin/Info.dev.plist`, `build/linux/nfpm/nfpm.yaml`.
- `APP_VERSION` mà Taskfile dẫn xuất đã đọc lại ra `0.0.51` (`go run ./scripts/release version -config ./build/config.yml`), nên ldflags `-X cursor/internal/buildinfo.Version` và tên artifact release sẽ theo bản mới.
- `frontend/package.json` vẫn `0.0.0` (placeholder, không liên quan tới version app). Khi phát hành chính thức có thể chạy `task common:update:build-assets` (wails3 update build-assets) để wails tự sinh lại metadata.

### Changed: removed hand-ported `proto/byok_local_v1.proto`

- **Điều kiện tự xóa đã thỏa**: header của file ghi "Xóa file này khi bản sync (a) đã chứa các message gốc". Sau `sync:proto` Cursor 3.32.4, `proto/agent_v1.proto` đã khai báo đủ 5 message (`LocalSubscriptionToolDefinition`, `ListLocalSubscriptionTools{Request,Response}`, `CallLocalSubscriptionTool{Request,Response}`) ở dòng 2189/2197/7123/7127/7331 và **diff field-level với bản chép tay là rỗng** — cùng field number, cùng tên, cùng `optional` — nên bản hand-port không còn giá trị tương thích wire.
- **Lợi ích ngoài dự đoán**: `gen/agentv1/agentv1connect` giờ sinh sẵn `AgentServiceListLocalSubscriptionToolsProcedure` / `AgentServiceCallLocalSubscriptionToolProcedure` (`:65`/`:38`) và 2 method trong `AgentServiceHandler`, nên hai constant tự viết trong `local_subscription_tools.go` được thay bằng constant generated — path không thể lệch khỏi bản sync nữa.
- **Blast radius**: chỉ `internal/backend/forwarder/local_subscription_tools.go` (18 tham chiếu) và test của nó (7); `host.go:326/331` + `module.go:38/39` vẫn dùng tên constant cũ nên không đổi. Không có nơi nào hardcode tên package `byok.agent.v1` ngoài file generated, và kênh này chưa từng có `connect` stub (file không khai `service`) ⇒ wiring hand-made giữ nguyên.
- **Dọn pipeline**: `build/Taskfile.yml` `generate:proto` bỏ file khỏi lệnh protoc, bỏ `gen/byoklocalv1` khỏi `rm -rf` và `generates`, xóa 3 dòng comment "chỉ cần giữ tới khi bản sync chứa các message này".
- **Bằng chứng runtime (chưa kết luận được)**: `app.log` và các file debug trong `history/` có **0** bản ghi `local_subscription_tool*`, tức chưa thấy client thật gọi kênh này trên máy dev. Route vẫn được mount nên hành vi không đổi; đây là "không có bằng chứng được dùng", không phải "chứng minh không dùng".

### Added: agent.v1 unknown-procedure fallback + InjectContextAction port (kết quả code review)

### Changes

- **Fallback cho procedure `agent.v1` chưa mount** (`internal/backend/host.go`): client 3.32 khai báo 20 RPC trong `agent.v1.AgentService` nhưng BYOK mới cài 6; 14 RPC còn lại trả HTTP 404 (lỗi "Class A" — tính năng chết hẳn, không triệu chứng). Thêm `server.Any("/agent.v1.AgentService/*")` → `agentServiceFallbackAction()`: trả message rỗng đúng framing theo content-type (unary proto = body rỗng, JSON = `{}`, `connect+` = end-of-stream envelope) **và log tên procedure**, biến mỗi lần chạy thật thành bằng chứng RPC nào client thực sự gọi. Route đã mount không bị che (đã test).
- **`InjectContextAction` được xử lý thay vì bỏ im lặng** (`internal/backend/forwarder/context_injection.go` + field `InboundIntent.InjectContext`): action mang `user_context{user_message,request_context}` / `system_context{producer,content}` cho run ĐANG chạy (ô `expected_run_id`); trước đây rơi vào `default:` → metadata → mất nội dung không một dòng log. Nay payload được append thành `prompt_context` entry bọc `<system_reminder>` — cùng cơ chế giữa-run mà `mode_change` đang dùng, giữ bất biến "history chỉ append" để không phá prefix cache. `request_context` của user_context chưa có seam nên được log rõ (`request_context_present`) chứ không âm thầm bỏ.
- **Không còn đường im lặng**: event runtime mới `inject_context_applied` / `inject_context_empty` / `inject_context_dropped(reason=…)` ở cả hai chỗ bỏ rơi thật (`dispatchInboundIntent` khi chưa có stream, `handleMetadataIntent` khi stream đã đóng/chưa khởi tạo).
- **`requestedModelUsesVariantString` siết về token canonical** (`isCanonicalRuntimeThinkingEffortToken`): chỉ `disabled/low/medium/high/xhigh/max`. Bằng chứng: 3.32 vẫn giữ `RequestedModel.parameters` (field 3) làm kênh effort/context và đã bỏ flag, nên variant string là di sản client cũ; alias `no/off/none/false/0/disable` chỉ là dung sai input ở kênh khác, dùng chúng sẽ cắt nhầm model id BYOK kiểu `proxy:0`, `vertex:none`, `openai:gpt-4o-mini`.
- **`rawHex` debug log phản chiếu carrier thật đã decode**: nếu client set cả `data` lẫn `data_binary`, decode dùng binary mà log ghi hex của `data` → bản ghi mô tả sai payload server vừa xử lý.
- **`conversationActionKind` đủ 15 nhánh**: thêm `subscription_notification_action`, `goal_continuation_action`, `inject_context_action` để debug log tự giải thích thay vì in `%T`.

### Verification

```powershell
go build ./...
go vet ./...
go test ./internal/... ./proto/... ./cursor-proxy-debugger/... -count=1
# test mới: host_agent_fallback_test.go (framing + route precedence),
# context_injection_test.go (decode/entries/blank payload),
# variant_string_inference_test.go (canonical vs alias, id nguyên vẹn),
# protocol/inbound_test.go (carrier hex/binary/both/lỗi)
```

### Added: Proto snapshot and `gen/` synced to installed Cursor 3.32.4

### Changes

- **Multi-bundle extraction is now the real pipeline**: `proto/extract_extensions_proto.sh` accepts many bundles, auto-detects compact-descriptor bundles under the macOS *or* Windows installed-Cursor extensions root, and orders them by `BUNDLE_PRIORITY` (`cursor-always-local` first) so first-wins variant selection is stable. `build/Taskfile.yml` invokes the shell scripts via explicit `bash` (Windows cannot exec `.sh` by shebang) and drops the macOS-only precondition.
- **Compact descriptor fidelity**: `compactNormalizeEnumValues` strips the redundant `<PARENT>_<ENUM>_` prefix from nested enum value names so generated Go symbols keep the short names `internal/` already uses; `placeholderRe` tightened to stop flagging real upstream names (`Unknown unknown`, `unknown_tool_error_template`) as placeholders.
- **Merge policy for same-name type variants**: definition-level variant gate (`fieldNamesSubset`) plus downgrading cross-bundle field/enum/method drift to soft conflicts make `-strict` a genuine gate (protoc validation + unresolved refs only) instead of ~950 noise lines; regression tests updated in `proto/ext_tool/merge_test.go`.
- **Compat proto for client-removed types**: new `proto/aiserver_compat_v1.proto` hand-keeps `IsConnected{Request,Response}`, `RepositoryStatus{Request,Response}` (incl. nested `Synced` oneof) and `BatchRepositoryStatus{Request,Response}` — still served to older clients although 3.32.4 bundles deleted them — and is wired into `generate:proto`.
- **Compile fallout handled**: `Diagnostic.severity` became an enum (`DiagnosticSeverity(item.GetSeverity())` numeric bridge in `internal/backend/agent/bridge/exec/bridge.go`); client-deleted `RequestedModel.is_variant_string_representation` is replaced by syntax inference `requestedModelUsesVariantString` (bracket `name[...]` and colon `name:<effort>` forms) in `internal/backend/forwarder/service.go`, used at all former call sites and test fixtures.

### Fixed: `[invalid_argument] unsupported client message kind: ` when chatting with Cursor 3.32

Symptom on the released build: the first user message aborts with
`RetriableError: [invalid_argument] unsupported client message kind: ` (kind printed **blank**), so the agent loop
never starts. Root causes, none of which are proto-snapshot issues:

- `BidiAppendRequest` carries the client frame in either `data` (hex) **or** `data_binary` (raw bytes). Only
  `GetData()` was ever read anywhere in the repo, so a binary-carrier frame decoded to an empty
  `AgentClientMessage` and every kind check fell through to the blank-kind error.
  `protocol.DecodeAgentClientMessageEnvelope(hexData, dataBinary)` now prefers `data_binary` and falls back to hex;
  `Service.BidiAppend` and the `cursor-proxy-debugger` decoder both use it, and the debug recorder keeps a hex copy
  of whichever carrier arrived.
- Unrecognized or empty frames were **fatal** on the legacy unary route while the new bidi `Run` route tolerated
  them. Shared predicate `inboundKindCarriesRun` now decides both places: only `run_request` / `prewarm_request`
  decode failures abort the stream; management frames are acked, logged as `inbound_intent_ignored` /
  `agent_run_inbound_ignored` and dropped.
- The error text was unhelpful because the blank kind was all it printed; the message now falls back to
  `agentClientMessageCase(message)` (Go oneof wrapper name) so a future unknown branch names itself.

Regression coverage: `internal/backend/forwarder/bidi_append_payload_test.go` — binary carrier accepted, unknown
oneof branch acked, empty payload acked, and a genuinely broken `run_request` still rejected with
`InvalidArgument` naming `conversation_id`.

### Verification

```powershell
task -t build/Taskfile.yml sync:proto   # exit 0, -strict extraction, check_proto_sync green
go build ./...
go vet ./...
go test ./internal/... ./proto/... -count=1
go test ./internal/backend/forwarder/ -run BidiAppend -count=1   # carrier + dung sai frame
bash proto/check_proto_sync.sh
# symbol audit: every agentv1/aiserverv1 symbol used by internal/ exists in regenerated gen/ (missing = 0)
```

## 2026-11-25

### Added: Local subscription tool channel for BYOK-owned tools

### Changes

- **New registry seam**: `internal/backend/forwarder/local_subscription_tools.go` owns the `ListLocalSubscriptionTools` / `CallLocalSubscriptionTool` channel and holds the only list of BYOK-owned server-side tools; adding a tool is one spec entry, not a new route.
- **One source of tool truth**: `tool_name`, `description` and `input_schema_json` are read from `prompt/tools.json` through `prompt.ReadTools`, so the subscription view cannot drift from the catalog the model already sees.
- **Memory tools migrated onto the channel**: `SaveMemory` / `SearchMemory` now execute through `CallLocalSubscriptionTool`, reusing `saveMemoryResult` / `searchMemoryResult` verbatim instead of duplicating logic.
- **Error taxonomy**: an unknown `tool_name` is a protocol error (`NOT_FOUND`), while a business condition (memory disabled, no workspace scope) returns `is_error=true` with readable text so the model can act on it.
- **Conversation-scoped stream lookup**: `StreamBroker.ActiveStreamForConversation` resolves the `ActiveStream` from `conversation_id` (preferred non-terminal, then most recently updated) because this channel carries no `request_id`.
- **Scoped proto port**: `proto/byok_local_v1.proto` defines the four messages with the upstream field numbers but its own package, so it cannot collide with a future `sync:proto`; it is wired into `generate:proto` and documented for deletion once the real sync lands.

### Open assumption

- `result_json` shape is not observable from the schema alone; the spike returns `{"text": ...}`. Verify against the client bundle when proto sync (phase a) runs.

### Regression coverage

- Added `internal/backend/forwarder/local_subscription_tools_test.go`: catalog validity, list gating on the memory flag, unknown-tool rejection, disabled-memory business error, and a full save→search round trip through the channel.

### Verification

```powershell
go build ./...
go test ./internal/backend/forwarder/ -run Subscription -count=1
go test ./internal/... -count=1
```

### Added: Bidirectional `agent.v1.AgentService/Run` chat channel for newer Cursor clients

### Changes

- **New bidi entry point**: `internal/backend/forwarder/run_bidi.go` implements `Service.Run` for `/agent.v1.AgentService/Run`, where inbound `AgentClientMessage` and outbound `AgentServerMessage` share one stream. Newer Cursor builds removed `aiserver.v1.BidiService/BidiAppend` (the input half of the old two-call channel), so `run_request` now only has this carrier.
- **Per-stream request id**: the handler mints `agent-run-<uuid>` and shares it across both directions, so `exec` / `interaction` / `kv` results correlate with `run_request` without the legacy `append_seqno` idempotency tracker.
- **Cursor reset invariant**: dispatching an intent that starts a run resets the local read cursor, because `OpenStream` clears the backlog and zeroes subscriber cursors.
- **Fault tolerance by frame kind**: an undecodable `run_request` / `prewarm_request` aborts the stream with `INVALID_ARGUMENT`, while heartbeat and result frames are tolerated so a live provider loop is not killed by one bad frame.
- **Legacy routes preserved**: `BidiAppend` and `RunSSE` stay registered for older clients; `Module.AgentRunHandler` is wired in `host.go` next to them.

### Regression coverage

- Added `internal/backend/forwarder/run_bidi_test.go`: h2c round-trips proving the stream closes cleanly with no frames, dispatches a real `run_request` without deadlocking, honours the cursor-reset contract, and classifies normal stream end.

### Verification

```powershell
go build ./...
go test ./internal/backend/... -count=1
go test -race ./internal/backend/forwarder/ -run "Bidi|AgentRun" -count=1
```

## 2026-08-19

### Fixed: Checkpoint blob lockup on failed turns and prompt deduplication

### Changes

- **Immediate failed terminal events**: `failActiveStream` now discards pending checkpoint blob synchronization and triggers the failed terminal event (`broker.Fail` / `StreamEvent.End`) immediately upon upstream errors, preventing Cursor UI from hanging indefinitely.
- **Prepend user messages & deduplication**: Supported extracting `PrependUserMessages` from user actions and deduplicating completed message IDs against conversation history to avoid replaying prior turns on unqueue.

### Regression coverage

- Updated `TestCheckpointBlobSyncPublishesFailedTerminalImmediately` and `TestCheckpointBlobTimeoutStillPublishesFailedTerminal` in `internal/backend/forwarder`.
- Added tests for prepend user messages and queue deduplication.

### Verification

```powershell
go test ./internal/backend/forwarder/...
go test ./...
```

## 2026-08-18

### Changed: Performance optimization, shell recovery, and tool caching

### Changes

- **Prompt and tool caching**: Introduced caching for baseline tools, sanitized prompts, and tool catalogs across modes to eliminate redundant disk reads.
- **Shell dispatch recovery**: Added a recovery timer and mechanism when client fails to acknowledge shell execution within the dispatch deadline.
- **Compact context serialization**: Serialized conversation context snapshots with compact JSON (omitting extra whitespace) to save memory, disk I/O, and CPU time.
- **Active conversation protection**: Added logic and tests to ignore empty resume run requests while a turn is actively running.
- **Enhanced checkpoint blob management**: Refactored timer tokens and pending blob metadata tracking with lifecycle metrics.
- **MITM disconnect logging**: Streamlined client disconnect log filters.

## 2026-08-17

### Changed: Forwarder lifecycle cleanup and task guidelines

### Changes

- **Forwarder host module management**: Added formal forwarder service lifecycle management and graceful flush/closure in `UsageFileStore` upon host shutdown.
- **Prompt and task management guidelines**: Enhanced common prefix prompt documentation with clear rules for proactive concurrent subagent execution, task delegation, and structured lossless conversation compaction.

## 2026-08-15

### Changed: Commit prompt and compaction refinement

### Changes

- Clarified instructions and formatting rules for commit message generation.
- Expanded conversation compaction guidelines for structured retention.

## 2026-08-14

### Changed: Latency instrumentation and plan tracking

### Changes

- **Provider pass latency metrics**: Instrumented compile, TTFT, cache hit, and total duration metrics for provider-pass diagnostics while maintaining lossless replay.
- **Untracked local plans**: Configured `.gitignore` to keep local planning artifacts out of git tracking.

## 2026-08-13

### Fixed: Retain MCP registry when request context is absent

### Changes

- Ensured MCP tool registry snapshot is retained and persisted even when inbound request context is omitted.

## 2026-08-12

### Changed: Upstream v0.0.48 sync, per-user CA, and content-addressed images

### Changes

- **Sync upstream v0.0.48**: Merged upstream release updates and build configurations.
- **Per-installation Root CA**: Generated unique, per-installation root CA certificates rather than using a shared static certificate.
- **Content-addressed Read images**: Supported image reading via local tools with content hash persistence.
- **Deterministic MCP snapshotting**: Snapshotted MCP registry per request, filtering out disabled servers and normalizing server aliases.
- **Language policy cleanup**: Reverted downstream language policy injection in favor of shared rules.

## 2026-08-11

### Changed: Optional reasoning effort and plan execution directive

### Changes

- **Optional reasoning effort**: Supported models/providers that do not accept reasoning effort parameters.
- **Execute plan directive**: Introduced directive to execute current plans directly within conversation actions.
- **AwaitShell polling refinement**: Finalized polling until shell completion.

## 2026-08-10

### Changed: Sync upstream and expand the conversation forwarder

### Changes

Merged `upstream/main` and integrated changes for checkpoint/projection, append-only compaction, imported history, the state database, provider adapters, upstream mocks, and UI/localization/build configuration.

The forwarder now provides improved conversation recovery, transcript replay, and terminal state management. It also adds handling for shell streaming, cancellation, terminal events, interrupted output, and continuation/checkpoint cases while the Agent is running.

The MCP tool registry is normalized by server identifier and tool name, and can resolve aliases from server names for compatibility with descriptors provided by Cursor.

### Fixed: AwaitShell waited duration and timeout handling

`AwaitShell` now polls the shell state every 50ms until the shell completes, the requested pattern matches, or `block_until_ms` expires. Previously it returned a single snapshot immediately while reporting the requested wait duration, which could make a 1–3 second shell appear to have waited for repeated 30-second or 2-minute intervals.

The default wait timeout remains 30 seconds per `AwaitShell` call. A completed shell returns immediately, while `block_until_ms: 0` performs an immediate snapshot. Foreground shell recovery keeps the existing 1.5-second grace period after the configured wait deadline.

### Regression coverage

- Added tests for checkpoint blobs, append-only compaction, imported history, and the state database.
- Added tests for shell stream deltas, interrupted output, terminal lifecycle, and provider thinking/reasoning carriers.
- Updated tests for the MCP registry, model streaming, and upstream mocks.

### Verification

The following commands completed successfully:

```powershell
go build ./...
go test ./...
yarn build
go test ./internal/backend/forwarder
```

`git diff --cached --check` also passed. `go vet ./...` continues to report a baseline error/warning at `internal/backend/agent/bridge/interaction/bridge.go:328` because `json.Marshal` copies a lock value in `SwitchModeArgs`; no new errors from the merge were found. E2E testing with the Cursor client and a real provider has not been run.

## 2026-08-05

### Changed: Sync upstream v0.0.45 and use inline checkpoints

### Changes

Merged `upstream/main` v0.0.45. The conversation checkpoint mechanism was changed from blob-backed turns to inline turns from upstream to apply the fix for disappearing conversations.

Blob synchronization, imported blobs, and the checkpoint blob timeout were removed under the new contract. Conversation state import and replay now decode inline turns/steps directly from the checkpoint.

### Preserved fork behavior

- Shared reasoning from a provider pass is persisted in only one tool call; `tool_result` retains reasoning as a fallback only when the corresponding `tool_call` is missing.
- Late partial/delta updates are ignored after a tool has completed.
- Hidden `PatchEdit` and `Write` operations continue to recover when the transport closes before the terminal result, while also completing the operation when the turn is canceled.
- The terminal stream does not emit duplicate terminal events or allow subscriptions after it has ended.

### Upstream updates

- Bumped the release version to `0.0.45`.
- Added the `CURSOR_BYOK_DISABLE_WEBVIEW_SANDBOX` environment variable to disable the WebView sandbox for affected VDI environments.

### Verification

```powershell
go test ./... -count=1 -timeout 180s
```

The command completed successfully. `go vet ./...` continues to report two existing warnings at `internal/backend/agent/bridge/interaction/bridge.go`; they are unrelated to this merge.

## 2026-08-03

### Fixed: Reasoning/progress repeated before multiple tools in one Agent turn

### Symptom

After syncing upstream, a reasoning/progress block could appear repeatedly before each tool in the same Agent turn. This was especially visible when the provider returned multiple consecutive tool calls.

### Cause

Provider reasoning was accumulated at the provider-pass level but was previously copied into every `ToolLikeCompleted`. As a result, the same reasoning data was persisted multiple times in `tool_call` and was also written to `tool_result` when the tool completed.

The new Cursor transcript sync feature from upstream renders persisted `tool_call` entries directly, making this latent issue visible as repeated progress/tool rows in the UI.

### Fix

The reasoning buffer now has explicit ownership: it is consumed exactly once by the first tool in the provider pass. If the provider also emits text, the reasoning is attached to `assistant_text` and is not copied to the tool.

`tool_result` retains `reasoning_content` only when the corresponding `tool_call` is missing, preserving replay support for older or incomplete history.

### Verification

Added regression tests covering shared reasoning across multiple tools, single-render transcript reasoning, and `tool_result` fallback only when the corresponding `tool_call` is missing:

```text
TestTakeProviderOutputForToolConsumesReasoningOnce
TestToolResultReasoningFallbackOnlyPersistsWhenToolCallIsMissing
TestProjectorKeepsSharedReasoningOnOnlyOneOfMultipleToolCalls
TestProjectCursorTranscriptJSONLKeepsSharedReasoningOnOnlyOneTool
```

Test command:

```powershell
go test ./internal/backend/forwarder -count=1 -timeout 60s
```

### Fixed: Cursor Agent transcript repeated or lost content while responding

### Symptom

While the Agent was streaming a response or calling a tool, the Cursor UI could display repeated progress/file rows. Some content that had already appeared could also disappear or be replaced by an older snapshot.

### Cause

Local conversation history synchronization to the Cursor transcript wrote the entire transcript after every history change. This happened concurrently with the Cursor client appending stream events for the same turn, causing two flows to update the transcript:

- The Cursor client rendered stream events directly.
- The backend re-projected history and overwrote the transcript file.

An intermediate history snapshot might not contain all of the latest events, causing content to repeat or disappear from the UI.

### Fix

Transcript sync is now deferred while the Agent turn is active (`running`, `waiting_tool`, or `checkpointing`). The backend writes the transcript only after the turn reaches a terminal state such as `turn_completed`, `failed`, `provider_error`, or `canceled`.

The terminal snapshot also includes `turn_ended`, so the completed transcript matches the final state of the chat turn.

### Verification

Added a regression test confirming that the transcript is not created during an active turn and is written only after `turn_completed`:

```text
TestConversationFileStoreDefersCursorTranscriptSyncUntilTurnEnds
```

Test command:

```powershell
go test ./internal/backend/forwarder -run "TestConversationFileStore(DefersCursorTranscriptSyncUntilTurnEnds|SyncsCursorTranscript|BackfillsTranscriptOnStartup)" -count=1 -timeout 30s
```
