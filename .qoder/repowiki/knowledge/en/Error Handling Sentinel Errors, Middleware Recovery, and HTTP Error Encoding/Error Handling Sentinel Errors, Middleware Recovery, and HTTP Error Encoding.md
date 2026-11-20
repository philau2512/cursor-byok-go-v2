---
kind: error_handling
name: 'Error Handling: Sentinel Errors, Middleware Recovery, and HTTP Error Encoding'
category: error_handling
scope:
    - '**'
source_files:
    - internal/backend/server/middleware.go
    - internal/backend/server/errors.go
    - internal/backend/server/response_writer.go
    - internal/runtime/local_runtime.go
    - internal/backend/agent/model/http_error.go
    - internal/backend/forwarder/tool_error_completion.go
    - internal/backend/forwarder/types.go
    - internal/backend/forwarder/compaction.go
---

## Overview

The Cursor BYOK Go v2 desktop application uses a layered error-handling approach across its Go codebase:

1. **Sentinel `error` values** (via `errors.New`) for domain-level failure modes that callers need to distinguish.
2. **`fmt.Errorf` with `%w` wrapping** for contextualized errors that propagate through call stacks.
3. **A single `recover()` middleware** in the HTTP server layer that converts panics into structured error responses.
4. **An `ErrorEncoder` middleware** that maps sentinel errors to HTTP status codes and messages.
5. **Custom error structs** in the forwarder layer for terminal/recoverable tool invocation failures.
6. **HTTP response body summarization** for upstream provider errors.

There is no dedicated `errors/` package; sentinel errors live next to the domains they describe.

## Key Files and Packages

- `internal/backend/server/errors.go` — defines `ErrInvalidBidiAppendPayload`, the only sentinel in this package.
- `internal/backend/server/middleware.go` — implements `Recover()`, `ServerContext()`, and `ErrorEncoder()` middlewares; centralizes panic-to-error conversion and HTTP status mapping.
- `internal/backend/server/response_writer.go` — wraps `http.ResponseWriter` to track whether headers have been written (`trackedResponseWriter`), preventing double-write during error encoding.
- `internal/runtime/local_runtime.go` — exposes `ErrInvalidSystemSetting` and `ErrChannelNotAvailable` as cross-package sentinel errors used by the server layer.
- `internal/backend/agent/model/http_error.go` — `buildHTTPStatusError` reads non-2xx upstream responses (up to 8 KiB) and builds a descriptive error string including status code and truncated body.
- `internal/backend/forwarder/types.go` — defines `providerTerminalError`.
- `internal/backend/forwarder/tool_error_completion.go` — defines `recoverableToolInvocationError`.
- `internal/backend/forwarder/compaction.go` — defines `compactionTerminalError`.

## Architecture and Conventions

### Panic recovery at the HTTP boundary

Only one `recover()` exists in the codebase, inside `internal/backend/server/middleware.go`:

```go
func Recover() Middleware {
    return func(next HandlerFunc) HandlerFunc {
        return func(ctx *Context) (err error) {
            defer func() {
                if recovered := recover(); recovered != nil {
                    err = fmt.Errorf("panic: %v\n%s", recovered, string(debug.Stack()))
                }
            }()
            return next(ctx)
        }
    }
}
```

Panics elsewhere in the codebase (e.g. `prompt/embed.go` lines 170–378) are treated as unrecoverable initialization failures — they abort process startup rather than being caught locally. The server's `Recover()` middleware is the sole safety net for request-time panics.

### Error propagation via `%w` wrapping

Throughout the codebase, errors are wrapped with `fmt.Errorf(..., "%w", err)` to preserve the original error value so `errors.Is` can match sentinel errors through layers. Examples include `cursor-proxy-debugger/proxy.go`, `cursor-tab-server/main.go`, and `internal/appdata/migrate.go`.

### Sentinel errors for cross-layer classification

Domain-specific sentinel errors are exported from their owning packages and matched with `errors.Is` at boundaries:

- `internal/runtime.ErrInvalidSystemSetting` — returned when system settings are invalid; mapped to `500 Internal Server Error` by the server's `writeServerError`.
- `internal/runtime.ErrChannelNotAvailable` — returned when no model channel is available; mapped to `503 Service Unavailable`.
- `internal/backend/server.ErrInvalidBidiAppendPayload` — returned for malformed bidi append payloads; mapped to `400 Bad Request`.

These sentinels are the primary mechanism for classifying errors across package boundaries rather than using custom error interfaces or error codes.

### HTTP error encoding

The `ErrorEncoder` middleware intercepts every handler error and writes it via `writeServerError`. It first checks `responseWriterHasWrittenHeader` (defined in `response_writer.go`) to avoid writing a second response after streaming has begun. Status mapping is centralized:

| Condition | Status | Message |
|---|---:|---|
| `err == nil` | 200 | empty |
| `"empty raw url"` | 400 | "invalid raw url" |
| `errors.Is(err, ErrInvalidBidiAppendPayload)` | 400 | "invalid bidi append payload" |
| `errors.Is(err, legacyruntime.ErrInvalidSystemSetting)` | 500 | "invalid system setting" |
| `errors.Is(err, legacyruntime.ErrChannelNotAvailable)` | 503 | "no available channel" |
| default | 502 | "bad gateway" |

### Custom error types in the forwarder

Three small typed errors exist in the forwarder layer, each implementing the `error` interface:

- `compactionTerminalError` (`internal/backend/forwarder/compaction.go`) — signals a terminal failure during compaction.
- `recoverableToolInvocationError` (`internal/backend/forwarder/tool_error_completion.go`) — marks a tool invocation error as recoverable.
- `providerTerminalError` (`internal/backend/forwarder/types.go`) — marks an upstream provider error as terminal.

These are used internally within the forwarder pipeline to control retry/termination behavior rather than being surfaced to HTTP clients.

### Upstream HTTP error summarization

`internal/backend/agent/model/http_error.go` provides `buildHTTPStatusError`, which reads up to `maxErrorBodyBytes` (8192 bytes) of the upstream response body and formats a descriptive error string including status code, optional retry summary, and truncated body text. This is used to turn non-2xx HTTP responses from model providers into Go errors carrying enough diagnostic information.

## Conventions and Constraints

- **Sentinel errors are created with `errors.New`**, not `fmt.Errorf`, when they need to be matched with `errors.Is` across package boundaries. See `internal/runtime/local_runtime.go` and `internal/backend/server/errors.go`.
- **Contextual error messages use `fmt.Errorf` with `%w` wrapping**, preserving the underlying error for `errors.Is` matching. This pattern is consistent across `cursor-proxy-debugger`, `cursor-tab-server`, and `internal/appdata`.
- **Panic recovery is centralized**: there is exactly one `recover()` call in `internal/backend/server/middleware.go`; other panics (e.g. in `prompt/embed.go`) are intentionally unrecovered and signal fatal initialization failures.
- **HTTP status codes are derived from sentinel error identity** via `errors.Is` in `writeServerError`, not from error strings or type assertions. Adding a new classified error requires adding a case to `writeServerError`.
- **Double-response writes are guarded**: `responseWriterHasWrittenHeader` walks the writer chain via `Unwrap()` and the `responseWrittenTracker` interface to detect prior writes before emitting an error response.
- **Upstream provider errors are summarized** (status + truncated body) rather than propagated verbatim, capped at 8 KiB to prevent memory issues on large error bodies.