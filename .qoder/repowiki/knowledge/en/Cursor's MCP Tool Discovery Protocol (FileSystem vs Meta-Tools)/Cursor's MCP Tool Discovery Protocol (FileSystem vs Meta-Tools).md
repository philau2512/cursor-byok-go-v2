---
kind: external_dependency
name: Cursor's MCP Tool Discovery Protocol (FileSystem vs Meta-Tools)
slug: cursor-mcp-protocol
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
source_files:
    - internal/backend/agent/prompt/engine.go
    - internal/backend/agent/prompt/local_discovery.go
---

### Cursor MCP Protocol
- Role: This repo implements the Cursor client's MCP tool discovery protocol so that the AI model can discover and call user-defined MCP servers (e.g. gridex).
- Two mutually exclusive transport modes exist:
  - `mcp_file_system`: older path where descriptors come from `request_context.mcpFileSystemOptions` with only `toolName` + `definitionPath`; schema is NOT inline — the model is told to browse descriptor files on disk.
  - `mcp_meta_tools`: newer path where descriptors come from `request_context.mcpMetaToolOptions` and include `inputSchemaJson` / `InputSchema` inline.
- The prompt engine prefers `mcp_meta_tools` over `mcp_file_system` when both are present; the filesystem mode emits `<mcp_file_system>` sections while meta-tools emit `<mcp_meta_tools>`.
- Schema hydration: `hydrateLocalMCPDescriptors` intentionally lazy-loads only `description` from definition JSON (the `arguments` field is dropped), preserving prefix-cache stability per the `prefix-cache-stability` skill.
- Stable integration rule: when adding or changing MCP descriptor handling, preserve the two-mode split and the deduplication of static sections at `engine.go:349`.