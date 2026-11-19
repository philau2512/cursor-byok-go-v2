---
kind: logging_system
name: Structured Logging via slog + tint with Dual Console/File Sinks
category: logging_system
scope:
    - '**'
source_files:
    - internal/logger/logger.go
    - internal/app/runner.go
    - internal/appdata/paths.go
    - frontend/bindings/log/slog/index.js
    - frontend/bindings/log/slog/models.js
    - cursor-tab-server/main.go
    - cmd/cursor-proxy-debugger/main.go
---

## Overview

The application uses Go's standard `log/slog` structured logger, wrapped in a small internal package (`internal/logger`) that configures two sinks — colored console output and a rotating app log file — and exposes simple `Info`/`Error`/`Infof`/`Errorf` helpers. The frontend (Wails) bridges to the same `slog` instance through generated bindings under `frontend/bindings/log/slog/`.

## Core Files

- `internal/logger/logger.go` — full logger implementation: handler setup, multi-handler fan-out, line-windowed file writer.
- `internal/appdata/paths.go` — defines `LogsRootPath()` used as the directory for `app.log`.
- `internal/app/runner.go` — calls `logger.Init()` at process startup (line 121).
- `frontend/bindings/log/slog/index.js` + `models.js` — Wails-generated JS bindings so the Vue frontend can call into `slog` from Go.
- `cursor-tab-server/main.go`, `cmd/cursor-proxy-debugger/*`, `internal/backend/forwarder/*.go` — legacy `log.Printf` usage in auxiliary services.

## Architecture

### Initialization

`logger.Init()` is idempotent via `sync.Once`. On first call it:

1. Creates a `tint.NewHandler` writing to `colorable.NewColorableStdout()` with level `slog.LevelInfo` and time format `15:04:05.000`.
2. Builds a file handler via `buildFileHandler()` which opens `<LogsRootPath>/app.log`.
3. If both exist, wraps them in an internal `multiHandler` that fans records to every enabled sink; otherwise uses the single handler directly.
4. Calls `slog.SetDefault(...)` so all subsequent `slog.Info/Error/...` calls route through this configuration.
5. Replaces the standard library `log` sink by calling `stdlog.SetFlags(0)` — but note the code does **not** set a custom writer on `stdlog`, so stdlib `log` still goes to stderr unless callers explicitly redirect it elsewhere.
6. Emits an initial info record with fields `path` and `pid` once the file path is known.

### Sinks

| Sink | Destination | Level | Format |
|---|---|---:|---|
| Console | stdout (colorized) | `slog.LevelInfo` | `tint` human-readable, time `15:04:05.000` |
| File | `<LogsRootPath>/app.log` | `slog.LevelInfo` | `tint` with `NoColor: true`, time `time.RFC3339` |

Both handlers are created with `Level: slog.LevelInfo`; there is no runtime mechanism to raise or lower the threshold.

### File Rotation / Line Windowing

`lineWindowFileWriter` implements `io.Writer` and enforces a maximum of `appLogMaxLines = 10000` lines, reserving the last `appLogTrimReserveLine = 1000` lines when trimming. It tracks whether the current line is open (no trailing newline) to avoid double-counting partial writes. When the limit would be exceeded, it reads the whole file, drops the oldest lines, rewrites the remainder, and reopens the file — all under a per-write mutex.

### Color Control

Console color is disabled when any of these conditions holds:
- Environment variable `NO_COLOR` is set (non-empty).
- `TERM=dumb`.
- `os.Stdout` is not a TTY (checked via `isatty.IsTerminal` and `isatty.IsCygwinTerminal`).

### Structured Fields

Call sites pass key-value pairs as `slog` attributes. Examples observed:
- `logger.Infof("应用版本：v%s", buildinfo.CurrentVersion())`
- `logger.Errorf("自动启动服务失败: %v", err)`
- `logger.Infof("RepositoryService SyncMerkleSubtreeV2 matched codebase_id=%s partial_paths=%d", ...)`
- Initial log record: `"应用日志已写入文件"` with fields `path` and `pid`.

The `slog` default JSON/text handler is used (no custom encoder configured), so attributes appear as `key=value` pairs in the `tint` output.

### Frontend Bridge

The Wails-generated bindings under `frontend/bindings/log/slog/` expose `slog` to the Vue frontend, allowing the UI to read logs emitted by the Go side.

## Conventions Observed

- Application-level logging goes through `internal/logger` (`logger.Info`, `logger.Error`, `logger.Infof`, `logger.Errorf`); these wrappers call `Init()` lazily before delegating to `slog`.
- Startup code in `internal/app/runner.go` invokes `logger.Init()` early in `Run()`.
- Auxiliary standalone binaries (`cursor-tab-server`, `cmd/cursor-proxy-debugger`) use the standard library `log` package directly (`log.Printf`, `log.Fatal`) rather than `internal/logger`, because they are separate processes.
- Some hot-path components in `internal/backend/forwarder/` also use `log.Printf` directly (e.g. `actor.go`, `checkpoint_blobs.go`, `usage_store.go`, `history_maintenance.go`), bypassing the structured logger.
- Log files live under the platform-appropriate assistant home directory returned by `appdata.LogsRootPath()`, specifically `app.log`.
- There is no log-level flag or environment variable to change verbosity at runtime; the only knob is the compile-time constants `appLogMaxLines` and `appLogTrimReserveLine`.
- No log rotation by time or size beyond the fixed 10k-line window; no syslog, no network sink, no JSON formatter.

## Rules Enforced by Code

- `logger.Init()` is called exactly once across the process lifetime (enforced by `sync.Once`).
- Console output is suppressed when `NO_COLOR` is set, `TERM=dumb`, or stdout is non-TTY (enforced in `disable()`).
- The file log is capped at 10,000 lines, preserving the most recent 1,000 lines during trim (constants `appLogMaxLines`, `appLogTrimReserveLine`).
- Both console and file handlers are hard-coded to `slog.LevelInfo`; records below INFO are dropped by both sinks.
- The file path is always `<appdata.LogsRootPath()>/app.log` (enforced in `buildFileHandler`).
- The Wails frontend accesses logging exclusively through the generated bindings under `frontend/bindings/log/slog/`.