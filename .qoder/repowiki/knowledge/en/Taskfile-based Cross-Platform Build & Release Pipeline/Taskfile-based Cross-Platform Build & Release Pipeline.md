---
kind: build_system
name: Taskfile-based Cross-Platform Build & Release Pipeline
category: build_system
scope:
    - '**'
source_files:
    - Taskfile.yml
    - build/config.yml
    - build/darwin/Taskfile.yml
    - build/windows/Taskfile.yml
    - build/linux/Taskfile.yml
    - build.bat
    - build.ps1
    - scripts/release/main.go
    - cursor-tab-server/Dockerfile
---

## Overview

The project uses **Go + Wails v3** as the desktop application framework, with build orchestration entirely driven by **Taskfile** (go-task). There is no Makefile; `Taskfile.yml` at the repo root includes per-platform sub-taskfiles under `build/{darwin,windows,linux}/Taskfile.yml`. A single `scripts/release` Go CLI drives version resolution, changelog processing, update manifest generation, and GitHub Releases publishing.

## Key Files

- `Taskfile.yml` — top-level task definitions: `build`, `run`, `build:all`, `dev`, release tasks (`release:prepare`, `release:verify:assets`, `release:manifest`, `release:sync:readme`, `release:github`), plus shared vars (`APP_NAME`, `BIN_DIR`, `APP_VERSION`, `RELEASE_REPO`, `RELEASE_BASE_NAME`, `RELEASE_DIR`).
- `build/config.yml` — Wails app metadata (company/product name, identifier `com.cursor.wuxianxubei`, `version: "0.0.50"`) and dev-mode watch config.
- `build/darwin/Taskfile.yml` — macOS build via native `go build` (CGO enabled, `MACOSX_DEPLOYMENT_TARGET=10.15`), `.app` bundle creation, ad-hoc `codesign`, `create-dmg` / `hdiutil` fallback, universal binary via `lipo`.
- `build/windows/Taskfile.yml` — Windows build via native cross-compile (`CGO_ENABLED=0`, `-H windowsgui`), NSIS installer (`makensis`) and optional MSIX packaging (`wails3 tool msix`), icon/resource generation via `wails3 generate syso`.
- `build/linux/Taskfile.yml` — Linux build with two paths: native (requires gcc/clang + GTK/WebKit2GTK pkg-config) or Docker-based cross-compile using a prebuilt image `cursor-linux-amd64-cross` invoked through `build/docker/build-linux.sh`; only `linux/amd64` is supported.
- `build.bat` / `build.ps1` — thin Windows entrypoints that resolve the version via `go run ./scripts/release version -config ./build/config.yml` and delegate to `task build:windows:binary OUTPUT=<path>`.
- `scripts/release/main.go` — Go CLI used by all build/release steps for version, notes, and manifest generation.
- `cursor-tab-server/Dockerfile` — standalone multi-stage Dockerfile for the tab-proxy service (separate from the main app).
- `frontend/vite.config.js`, `frontend/package.json` — frontend asset build (Vite + Vue) invoked by the common task `common:build:frontend`.

## Architecture & Conventions

### Versioning
Version is not read from git tags. It is declared in `build/config.yml` under `info.version` and resolved at runtime via:
```
go run ./scripts/release version -config ./build/config.yml
```
The resulting value is injected into the binary at link time via `-ldflags="-X cursor/internal/buildinfo.Version=..."` in every platform's `BUILD_FLAGS` template.

### Platform Matrix
| Target | Arch(s) | Output format | Notes |
|---|---|---|---|
| macOS | arm64, amd64 | `.app` → `.dmg` (or `.tar.gz` for updater) | Universal builds via `lipo`; minimum OS 10.15 |
| Windows | 386, amd64 | `.exe` packaged into ZIP; optional NSIS installer or MSIX | CGO disabled; GUI window suppressed |
| Linux | amd64 only | tar.gz archive | Native or Docker cross-compile; requires GTK/WebKit2GTK dev deps |

### Build Flow
Every platform task follows the same dependency chain:
1. `common:go:mod:tidy` — ensure Go module state.
2. `common:build:frontend` — Vite build of the Vue/Wails frontend assets.
3. `common:generate:icons` — generate platform icons.
4. Platform-specific `go build` with production flags (`-tags production -trimpath -buildvcs=false -ldflags="-w -s -X cursor/internal/buildinfo.Version=..."`) or dev flags (`-gcflags=all="-l"`).
5. Packaging step (DMG / ZIP / tar.gz).

### Release Flow
`task release:prepare` orchestrates the full pipeline:
1. `release:clean` — wipe `bin/release/<version>/`.
2. `common:update:build-assets` — rebuild embedded frontend assets.
3. `release:notes` — validate `release-notes.md` and render it to `bin/release/<version>/.release-notes.md` via `scripts/release notes`.
4. Build artifacts for each target (`macos-arm64`, `macos-amd64`, `windows-amd64`, `linux-amd64`).
5. `release:verify:assets` — precondition checks that all four archives exist.
6. `release:manifest` — generate `update.json` via `scripts/release manifest` pointing at `leookun/cursor-byok` on GitHub.
7. `release:sync:readme` — push `README.md` to the public release repo via `gh api`.
8. `release:github` — create/update a GitHub Release tag `v<version>` and upload `update.json` plus all `.tar.gz`/`.zip` assets, with retry logic around transient GH API errors.

### Docker Usage
- Main app Linux builds go through a prebuilt `cursor-linux-amd64-cross` image built by `task setup:docker` (`common:setup:docker`); the actual build script is `build/docker/build-linux.sh`.
- The separate `cursor-tab-server` microservice has its own `Dockerfile` that builds a static Go binary (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`) and ships it in a `scratch` image exposing port 8041.

### Dev Mode
`task dev` runs `wails3 dev -config ./build/config.yml -port <VITE_PORT>` alongside `wails3 task common:dev:frontend`, which watches Go/JS/TS files and hot-reloads the Wails app. Dev builds disable VCS embedding and strip debug symbols via `-gcflags=all="-l"`.

## Conventions & Enforced Rules

- **Version source of truth**: `build/config.yml` `info.version` — all binaries are linked with this value via `-X cursor/internal/buildinfo.Version`. There is no git-tag-based versioning in the build.
- **Windows-only build command**: `build:all` has a precondition `[ "{{OS}}" = "darwin" ]` — building all targets must be run on macOS; Linux builds require a native Linux host.
- **Linux arch restriction**: `build/linux/Taskfile.yml` enforces `[ "{{.TARGET_ARCH}}" = "amd64" ]` — cross-compiling to other Linux architectures is rejected.
- **Docker prerequisite**: Linux Docker builds require `docker info >/dev/null` and the presence of the `cursor-linux-amd64-cross` image; otherwise they fail with a message directing you to run `task setup:docker`.
- **Native Linux prerequisites**: pkg-config, gcc or clang, and GTK 3 / WebKit2GTK 4.1 dev packages are required for native Linux builds (checked via `pkg-config --exists gtk+-3.0 webkit2gtk-4.1 gdk-3.0 gio-unix-2.0`).
- **Release asset naming**: Artifacts follow `<base>-<version>-<target>.(tar.gz|zip)` where base is `RELEASE_BASE_NAME` (`cursor-byok`) and target is `macos-arm64`, `macos-amd64`, `windows-amd64`, `linux-amd64`.
- **GitHub release target**: Hardcoded to `leookun/cursor-byok` with tag format `v<version>` and branch `main`.
- **Frontend build integration**: All platform builds depend on `common:build:frontend`, ensuring the Vite-built assets are embedded before Go compilation.
- **Production vs dev ldflags**: Production builds use `-tags production -trimpath -buildvcs=false -ldflags="-w -s ..."`; dev builds use `-gcflags=all="-l"` and keep VCS info.