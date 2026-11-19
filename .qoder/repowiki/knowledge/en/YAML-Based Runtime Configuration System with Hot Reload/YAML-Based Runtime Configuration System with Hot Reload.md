---
kind: configuration_system
name: YAML-Based Runtime Configuration System with Hot Reload
category: configuration_system
scope:
    - '**'
source_files:
    - internal/backend/server/config/types.go
    - internal/backend/server/config/store.go
    - internal/backend/server/config/manager.go
    - internal/backend/server/config/resolver.go
    - internal/backend/server/config/legacy_runtime.go
    - internal/client/config_transfer.go
    - cursor-tab-server/config.yaml
    - internal/runtime/defaults.go
---

## Overview

The application uses a custom, in-memory YAML-backed configuration system centered on `internal/backend/server/config`. It loads a single user-config YAML file, normalizes and validates it, exposes it atomically to consumers, and optionally hot-reloads on disk change. A secondary standalone service (`cursor-tab-server`) has its own minimal YAML config.

## Key Files and Packages

- `internal/backend/server/config/types.go` — `Config`, `ModelAdapterConfig`, `HomeMetricsConfig` structs; default constants (`DefaultBackendListenAddr`, `DefaultProxyListenAddr`, `DefaultProviderStreamIdleTimeoutSeconds`); all normalization/validation helpers (`NormalizeConfig`, `NormalizeModelAdapterConfigs`, `normalizeListenAddr`, `normalizeProviderStreamIdleTimeout`, `validateJSONMap`, `validateHeadersJSON`).
- `internal/backend/server/config/store.go` — `Store`: reads/writes the YAML file via `gopkg.in/yaml.v3`; implements `Load`, `Save`, `snapshot()` (modTime+size), and `shouldPersistNormalizedConfig` which decides whether normalized output should be written back.
- `internal/backend/server/config/manager.go` — `Manager`: wraps `Store`, holds an `atomic.Pointer[Config]`, provides `Current()`, `Load()`, `Save()`, `Subscribe(listener)`, and hot-reload via `reloadIfChanged` with a 500 ms debounce (`configHotReloadMinInterval`).
- `internal/backend/server/config/resolver.go` — `SelectChannelForModel`: resolves a requested model ID against configured `ModelAdapterConfig`s into a `legacyruntime.ResolvedChannel` using `modelchannel.ResolveAdapterIndex`.
- `internal/backend/server/config/legacy_runtime.go` — `Store.LegacyRuntimeSnapshot`: maps the new config shape onto the older `legacyruntime.RuntimeConfigSnapshot` for backward compatibility.
- `internal/client/config_transfer.go` — `ExportUserConfig` / `ImportUserConfig`: CLI/API surface for exporting/importing the full config as a `.yaml` file, with size limit (`maxConfigTransferFileSize = 4 MiB`), strict single-document YAML decoding (`KnownFields(true)`), and import blocked while backend/proxy services are running.
- `cursor-tab-server/config.yaml` — tiny standalone config for the tab-completion proxy server (just a `token` field).
- `internal/runtime/defaults.go` — hardcoded local-mode account token/email injected by the runtime layer (not user-editable).

## Architecture and Conventions

1. **Single source of truth**: `Store` owns one YAML path and a logs root. `NewStore(path, logsRoot)` constructs it; `Path()`/`LogsRoot()` expose them safely even if the receiver is nil.
2. **Normalization-first loading**: Every load path goes through `NormalizeConfig`, which applies defaults, trims whitespace, validates listen addresses (`host:port`, port 1–65535), clamps `providerStreamIdleTimeout` to `[MinProviderStreamIdleTimeoutSeconds, ∞)`, and normalizes each `ModelAdapterConfig`.
3. **Validation rules enforced in `NormalizeModelAdapterConfigs`**:
   - `displayName`, `type`, `apiKey`, `tooltipData`, `modelID` must be non-empty.
   - `type` must be exactly `openai` or `anthropic` (case-insensitive).
   - For `openai`: `reasoningEffort` ∈ {`""`, `low`, `medium`, `high`, `xhigh`, `max`}; `openAIEndpoint` must be `/v1/responses`, `/v1/chat/completions`, or `/custom`.
   - For `anthropic`: `anthropicThinkingEffort` ∈ {`low`, `medium`, `high`, `xhigh`, `max`}.
   - When extra params are enabled, `openAIExtraParamsJSON` / `anthropicExtraParamsJSON` must parse as a JSON object; `customHeadersJSON` must parse as a `map[string]string` with non-empty keys.
   - Duplicate channel IDs (derived from `BaseURL + ModelID + APIKey + DisplayName + OpenAIEndpoint`) are rejected.
   - Adapter sort order is renumbered sequentially after sorting by the user-provided `Sort` field.
4. **Atomic in-memory state**: `Manager.current` is an `atomic.Pointer[Config]`; `Current()` returns a copy, so callers cannot mutate shared state.
5. **Hot reload with debouncing**: `reloadIfChanged` checks `store.snapshot()` against a cached snapshot and only reloads if modTime/size changed, with a minimum 500 ms interval between reload attempts. Errors during reload are logged once and remembered so repeated failures don't spam logs.
6. **Listener notification**: `Subscribe(fn)` registers a callback invoked synchronously after a successful reload; returning a closure unsubscribes by slotting a nil function in place.
7. **Atomic file persistence**: `saveLocked` writes to `<path>.tmp` then `os.Rename` to the target, ensuring partial writes never corrupt the live config. Exported configs use a similar temp-file + replace pattern with `0o600` permissions.
8. **Selective write-back**: `shouldPersistNormalizedConfig` avoids rewriting the file unless it contains the legacy `routing` key, is missing `backendListenAddr`/`proxyListenAddr`, those fields changed, or `providerStreamIdleTimeout` was explicitly present and differs.
9. **Legacy compatibility**: `LegacyRuntimeSnapshot` (on both `Manager` and `Store`) translates the new `Config`/`ModelAdapterConfig` shapes into the older `legacyruntime.RuntimeConfigSnapshot` used by the legacy runtime subsystem.
10. **Import/export contract**: Imported YAML is decoded with `KnownFields(true)` and must contain exactly one top-level mapping node; unknown fields cause rejection. Export appends `.yaml` if no extension is given and writes via a temp file.

## Secondary Config Sources

- `cursor-tab-server/config.yaml` — loaded by the standalone tab-completion proxy; currently only carries a bearer `token` value.
- `internal/runtime/defaults.go` — injects a fake JWT (`InjectAuthToken`) and email (`InjectAccountEmail`) for local mode; these are compile-time constants, not user-editable.

## Not Observed

- No environment-variable based configuration loader.
- No `.env` files, TOML, JSON config, or feature-flag framework.
- No secrets manager integration beyond plain-text YAML fields (`apiKey`, tokens).
- No schema validation library (e.g., go-playground/validator); validation is hand-written in the normalize functions.