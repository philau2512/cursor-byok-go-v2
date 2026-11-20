---
kind: business_term
name: Business Glossary
category: business_term
scope:
    - '**'
---

### MCP FileSystem
- Definition：The legacy Cursor MCP discovery mode where tool descriptors are supplied via `request_context.mcpFileSystemOptions` containing only `toolName` and `definitionPath`. The model must read the JSON schema file from disk before calling a tool; this mode deliberately omits inline schemas to save tokens and protect the prefix cache.
- Aliases：filesystem mode、mcp_file_system

### MCP Meta-Tools
- Definition：The modern Cursor MCP discovery mode using `request_context.mcpMetaToolOptions`, which carries full tool descriptors including `inputSchemaJson` / `InputSchema` inline. The prompt engine renders these as `<mcp_meta_tools>` and the model calls tools directly via `CallMcpTool` without a prior `GetMcpTools` step.
- Aliases：meta-tools mode、mcp_meta_tools

### hydrated MCP descriptor
- Definition：An MCP tool descriptor whose `description` has been filled in by reading its `definition_path` JSON file (or falling back to `getFallbackMcpToolDescription`). Hydration is performed by `hydrateLocalMCPDescriptors` and caches results keyed by absolute path with mod-time invalidation.
- Aliases：descriptor hydration、MCP description hydration

### static section
- Definition：A block of context assembled once per request and emitted into the prompt before any realtime sections (user info, transcripts, rules, skills, subagents, MCP descriptors). Static sections are deduplicated across turns by comparing their joined string content, protecting the model's prefix cache.
- Aliases：static context、static sections

### realtime section
- Definition：A block of context appended after static sections that may change per turn — currently user intent summary, hooks additional context, current file contents, commit attribution, and PR attribution.
- Aliases：realtime context、realtime sections
