# Backend 架构说明

`internal/backend` 只支持本地助手模式。

关于 backend agent「最小事实集合」的第一阶段研究文档，见 [`../../docs/backend-agent-minimum-facts-phase1.md`](../../docs/backend-agent-minimum-facts-phase1.md)。

核心边界：

- `server`
  - 本地 HTTP/Connect 入口层
  - 负责路由、中间件、错误编码和少量本地 mock
- `forwarder`
  - 本地协议兼容与 LLM 转发内核
  - 负责 `BidiAppend`、`RunSSE`、history JSON、prompt 编译、provider 流式调用和广播
- `host`
  - 唯一组装点
  - 负责把 `server/config.Manager`、`forwarder.Module` 和根路由装起来

当前实现不再支持：

- Pro / `cursor-byok`
- HTTP/protocol trace debug UI
- DB-backed store、会话索引和 searchable conversation memory

## 目录结构

```text
internal/backend/
  README.md
  host.go

  server/
    context.go
    errors.go
    local.go
    middleware.go
    route.go
    url.go

    config/
      manager.go
      store.go
      types.go
      legacy_runtime.go
      resolver.go

    upstream/
      action.go
      mocks.go
      types.go

  forwarder/
    artifacts.go
    broker.go
    compiler.go
    events.go
    file_store.go
    legacy_stream.go
    module.go
    projector.go
    provider.go
    reminders.go
    service.go
    tool_catalog.go
    types.go

  agent/
    bridge/
      exec/
        bridge.go
      interaction/
        bridge.go

    core/
      types.go

    model/
      router.go
      openai.go
      anthropic.go
      artifacts.go
      provider_limits.go
      http_error.go
      types.go

    prompt/
      engine.go
      replay.go

    protocol/
      inbound.go
```

## 持久化布局

助手目录固定为：

- `~/.cursor-local-assistant-v2/config.yaml`
- `~/.cursor-local-assistant-v2/data/ca.crt`
- `~/.cursor-local-assistant-v2/data/ca.key`
- `~/.cursor-local-assistant-v2/data/ads/`
- `~/.cursor-local-assistant-v2/history/`
- `~/.cursor-local-assistant-v2/logs/`

约定：

- `config.yaml` 是用户配置
- `data/ca.crt` 是首次运行时为当前用户生成、注入给宿主的 CA 证书
- `data/ca.key` 是与该证书配套的本地私钥，权限固定为 `0600`，不得打包或提交到仓库
- `data/ads/` 是广告包与资源缓存目录
- `history/` 是会话事实与全局 usage JSON 目录，不属于日志
- `logs/` 只保留必要文本运行日志

当前 `history/` 目录布局：

```text
history/
  usage.json
  <conversation_id>/
    state.json
    context.json
    conversation.lock
```

`state.json` 只表达当前 loop 状态与持久化内存，不保存可投射给 LLM 的历史内容。当前 loop status 语义为：

- `idle`：没有正在推进的 loop。
- `running`：已落入本轮输入或中间上下文，正在等待/发起模型推进。
- `waiting_tool`：已落完整 tool call，正在等待工具结果。
- `completed`：本轮已正常完成。
- `canceled`：本轮被取消，不制造 assistant 输出。
- `provider_error`：provider/LLM 调用失败，错误作为 context tag 记录。
- `failed`：本地内部失败，例如投影、持久化、usage JSON 写入或桥接收口失败；它不等同于 provider 错误。

## 请求流

1. 请求进入 backend 根路由。
2. `ServerContext` 解析 MITM 带入的原始目标地址，路由始终执行本地 action。
3. `BidiAppend` / `RunSSE` 进入 `forwarder`。
4. `forwarder` 先把当前 loop 状态写入 `state.json`，再把已发生语义事件追加到 `context.json`。
5. 发给 LLM 的 prompt 只由 `context.json` 投射生成；`state.json` 不保存可投射历史。
6. provider usage/cache 与聚合统计写入 `history/usage.json`，不从 conversation 文件现场扫描。
7. `checkpoint` 只表示同一 backend 进程内的 live state。

## 模型渠道

- 用户在配置里填写 `displayName`、`baseURL`、`apiKey`、`modelID`
- 运行时渠道唯一 ID 不再由 `modelID` 决定
- 当前唯一 ID 是 `url + modelID + key + name` 的短 `SHA-256` hash（前 16 个十六进制字符）
- `modelID` 仅表示 provider model

## 项目记忆（Project Memory）

本地模式的跨会话工作区记忆（方案：`plans/20261120-byok-project-memory/`）。

- **存储**：`~/.cursor-local-assistant-v2/memories/projects/<workspace-slug>/<category>/<title-slug>.md`，每条记忆一个带 YAML frontmatter（`title, category, keywords, usage_scenario, origin, created_at, updated_at`）的 markdown 文件；用户可直接编辑/删除，store 通过目录指纹感知外部修改。
- **三条数据通路**：
  1. **概览注入**（`compiler_memory.go`）：`category → titles [keywords]` 树经 `prompt_context`（source `project_memories`）发布，仅在内容相对上次持久化版本变化时发出，append-only，不破坏前缀缓存。
  1b. **Memory nudge**（同文件）：当 user message 命中 directive/correction 信号（词表 `memoryNudgeMarkers`，越英双语），经 `prompt_context`（source `memory_nudge`）发一条单行提醒模型在本 turn 结束前考虑 `SaveMemory`；每条命中 turn 发一次（turn 内去重由既有 source+hash filter 保证），受 `memoryEnabled` kill-switch 控制。
  2. **工具**（`memory_tools.go`）：`SaveMemory` / `SearchMemory` 服务端执行（local-state，不经过客户端回环），含密钥拦截、拒绝模型伪造 `origin=auto`、每 run 30 次保存限流。
  3. **压缩自动沉淀**（`compaction_memory_capture.go`）：compaction 后用确定性解析器把 `Primary Target`、`user preferences & directives`、`HARD CONSTRAINTS`、`Rejected approaches & dead ends` 提炼为 `origin=auto` 条目（上限 8、内容去重、跳过 `[Unobserved]/[Unverified]`）。
- **配置键**（热加载）：`memoryEnabled`（默认开）、`memoryAutoCapture`（默认开）、`memoryOverviewMaxBytes`（默认 4096，区间 512–16384）。关闭 `memoryEnabled` 同时停用三条通路。
- **origin 语义**：`user`=用户要求记住；`model`=模型主动保存；`auto`=系统从压缩摘要提取。所有文件均可按 origin 审计。
- **人工验证**：见 `scripts/memory-verify.md`。
