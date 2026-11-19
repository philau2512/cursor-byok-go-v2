---
kind: dependency_management
name: Multi-Module Dependency Management (Go Modules + Yarn/npm Mirror)
category: dependency_management
scope:
    - '**'
source_files:
    - go.mod
    - go.sum
    - cursor-tab-server/go.mod
    - cursor-tab-server/go.sum
    - frontend/package.json
    - frontend/yarn.lock
    - frontend/.yarnrc
---

## Systems and Tools

The repository manages dependencies across three separate module roots:

1. **Root Go module** (`cursor`) — the main desktop application.
2. **`cursor-tab-server/`** — a standalone Go HTTP proxy with its own `go.mod`.
3. **`frontend/`** — a Vue 3 + Vite + Wails frontend managed by Yarn, pinned to an npm mirror registry.

There is no workspace-level dependency manager; each module root declares its own dependencies independently.

## Key Files

- `go.mod`, `go.sum` — root Go module manifest and checksums.
- `cursor-tab-server/go.mod` — second Go module (single dependency: `gopkg.in/yaml.v3`).
- `frontend/package.json` — frontend dependencies and devDependencies.
- `frontend/yarn.lock` — deterministic lockfile for the frontend.
- `frontend/.yarnrc` — Yarn configuration pointing at a Chinese npm mirror.

## Architecture and Conventions

### Go modules

- The root module uses **Go 1.25.0** (`go 1.25.0` in `go.mod`); the sibling `cursor-tab-server` module uses `go 1.25` (no patch version).
- Dependencies are declared explicitly via `require` blocks in `go.mod`; transitive dependencies appear as `// indirect` entries. There are no `replace` directives in `go.mod`, so versions come directly from the public Go module proxy.
- No `GOPRIVATE`, `GONOSUMCHECK`, or `GONOSUMDB` environment variables are set anywhere in the repo, and no private Go registries are configured. All Go dependencies resolve from the standard Go module proxy.
- There is **no vendoring directory** (`vendor/` does not exist). The build relies on downloading modules at build time.
- Generated code under `gen/` (protobuf/connect output) is committed alongside the source; it is not produced by a vendored toolchain.

### Frontend (Yarn)

- The frontend uses **Yarn** (lockfile `frontend/yarn.lock`) rather than npm/pnpm.
- `frontend/.yarnrc` configures a **Chinese npm mirror**: `registry "https://registry.npmmirror.com"`. This overrides the default npm registry for all Yarn operations in this directory.
- `network-timeout` is set to `120000` ms in `.yarnrc`, indicating a longer-than-default timeout for fetching packages from the mirror.
- `package.json` pins runtime dependencies with caret ranges (e.g. `vue ^3.5.22`, `chart.js ^4.5.1`) and devDependencies similarly with carets. One notable exception is `@wailsio/runtime` which uses `latest` — this is intentionally loose.
- The frontend has its own `node_modules/` directory (not shared with other modules).

### Private registries / access control

- No private Go module proxies or authentication tokens are present in the repository.
- No `.npmrc` or equivalent npm registry auth is present; only the Yarn mirror is configured.
- The only non-public-facing network behavior related to dependency resolution is the Yarn mirror redirect.

## Constraints and Rules Observed

- Each Go subdirectory that contains Go code owns its own `go.mod`; the root and `cursor-tab-server/` are independent Go modules, not part of a multi-module workspace.
- Go dependencies are resolved from the public module proxy — there are no `replace` directives or private proxy configuration in the repo.
- Frontend package resolution goes through the Yarn mirror at `https://registry.npmmirror.com` (enforced by `frontend/.yarnrc`).
- Lockfiles are committed: `frontend/yarn.lock` exists; Go checksums live in `go.sum` and `cursor-tab-server/go.sum`.
- No vendoring strategy is used for either Go or Node dependencies.