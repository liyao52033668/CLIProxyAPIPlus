# CodeArts 集成修复与优化 Spec

## Why
CodeArts 是项目中唯一一个有完整 executor/auth/translator/OAuth web handler 但**缺少 CLI 登录命令** (`--codearts-login`) 的 provider。用户只能通过浏览器 Web OAuth 流程登录，且 token 无法通过 SDK 自动刷新机制续期。此外，executor 的 payload 构建与 Python 参考实现存在显著差距，签名算法有 bug，以及多处代码规范问题。

## What Changes
- 新增 `--codearts-login` CLI flag 及完整登录链路
- 新增 `sdk/auth/codearts.go` SDK Authenticator
- 在 `sdk/auth/refresh_registry.go` 注册 CodeArts refresh lead
- 在 `internal/cmd/auth_manager.go` 注册 CodeArts authenticator
- 修复 `signer.go` 中 body hash 始终为空的 bug（POST 请求签名不包含 body）
- 修复 `buildCodeArtsPayload` 与 Python 参考实现的差距（缺少 chat_id、task_parameters、is_delta_response 等关键字段）
- 修复 `buildCodeArtsPayload` 中 messages 格式与 Python 参考实现不一致（缺少 `type` 字段）
- 新增 CodeArts thinking provider
- 修复 `config.example.yaml` 和 `config.go` 注释中遗漏 `codearts` channel
- 修复 `oauth-excluded-models` 注释中不存在的 `qwen` channel
- 翻译 `server.go` 和 `watcher.go` 中的中文注释为英文

## Impact
- Affected specs: CodeArts 认证、token 自动刷新、thinking pipeline
- Affected code:
  - `cmd/server/main.go` — 新增 flag
  - `internal/cmd/codearts_login.go` — 新文件
  - `sdk/auth/codearts.go` — 新文件
  - `sdk/auth/refresh_registry.go` — 注册 codearts
  - `internal/cmd/auth_manager.go` — 注册 codearts authenticator
  - `internal/auth/codearts/signer.go` — 修复 body hash bug
  - `internal/runtime/executor/codearts_executor.go` — 修复 payload 构建
  - `internal/thinking/provider/codearts/apply.go` — 新文件
  - `internal/runtime/executor/helps/thinking_providers.go` — 注册 codearts
  - `config.example.yaml` — 修复注释
  - `internal/config/config.go` — 修复注释
  - `internal/api/server.go` — 翻译中文注释
  - `internal/watcher/watcher.go` — 翻译中文注释

## ADDED Requirements

### Requirement: CodeArts CLI Login Command
系统 SHALL 提供 `--codearts-login` 命令行 flag，允许用户通过 CLI 完成 CodeArts OAuth 登录流程。

#### Scenario: 用户通过 CLI 登录 CodeArts
- **WHEN** 用户执行 `./server --codearts-login`
- **THEN** 系统启动本地回调服务器，生成 ticket_id，打开浏览器到 HuaweiCloud 登录页面
- **AND** 用户完成登录后，系统轮询获取认证结果
- **AND** 系统将 AK/SK/SecurityToken 凭证保存到 auth 目录
- **AND** 输出 "CodeArts authentication successful!"

### Requirement: CodeArts SDK Authenticator
系统 SHALL 在 `sdk/auth/` 包中提供 `CodeArtsAuthenticator`，实现 `Authenticator` 接口。

#### Scenario: Authenticator 注册与使用
- **WHEN** 系统初始化 auth manager
- **THEN** `CodeArtsAuthenticator` 被注册到 manager 中，provider 为 "codearts"
- **AND** `RefreshLead()` 返回 4 小时（token 有效期 24h，提前 4h 刷新）

### Requirement: CodeArts Refresh Lead 注册
系统 SHALL 在 `sdk/auth/refresh_registry.go` 中注册 codearts 的 refresh lead。

#### Scenario: 自动刷新调度
- **WHEN** auto refresh loop 检查 codearts auth 的过期时间
- **THEN** `ProviderRefreshLead("codearts", nil)` 返回 4 小时 duration
- **AND** token 在过期前 4 小时自动触发刷新

### Requirement: Signer Body Hash 修复
`SignRequest` SHALL 对 POST 请求计算实际 body 的 SHA256 hash，而非始终使用空 body hash。

#### Scenario: POST 请求签名
- **WHEN** 对 POST 请求调用 `SignRequest`
- **THEN** body hash 应为请求 body 的 SHA256 哈希值
- **AND** 签名结果与 Python 参考实现一致

### Requirement: CodeArts Payload 构建对齐 Python 参考
`buildCodeArtsPayload` SHALL 生成与 Python 参考实现 (`CodeArts-2api.py`) 一致的请求格式。

#### Scenario: 完整 payload 构建
- **WHEN** 构建 CodeArts chat 请求
- **THEN** payload 包含 `chat_id`（UUID）、`client: "IDE"`、`task: "chat"`、`task_parameters`（含 is_intent_recognition、W3_Search、codebase_search、related_question、preferred_language、enable_code_interpreter、ide、routerVersion、isNewClient、features.support_end_tag 等）
- **AND** payload 包含 `is_delta_response: true`、`user_id`、`attempt: 1`、`parent_message_id: ""`
- **AND** messages 格式为 `[{type: "text", content: "..."}]`（而非当前的 `[{role: "...", content: "..."}]`）
- **AND** system 消息格式为 `[System]\n{content}`，assistant 消息格式为 `[Assistant]\n{content}`

### Requirement: CodeArts Thinking Provider
系统 SHALL 提供 CodeArts thinking provider，将 thinking level 映射到 CodeArts 的 `task_parameters.temperature` 或其他合适字段。

#### Scenario: Thinking suffix 应用
- **WHEN** 用户请求模型 `Glm-5-internal(high)`
- **THEN** thinking provider 将 level "high" 应用到 CodeArts 请求中

### Requirement: 文档注释更新
配置文件和代码注释 SHALL 正确列出所有支持的 channel。

#### Scenario: oauth-model-alias 和 oauth-excluded-models 注释
- **WHEN** 用户查看 `config.example.yaml` 或 `config.go`
- **THEN** `oauth-model-alias` 的 Supported channels 注释包含 `codearts`
- **AND** `oauth-excluded-models` 的 Supported channels 注释包含 `codearts` 且不包含不存在的 `qwen`

### Requirement: 中文注释翻译
代码中的中文注释 SHALL 被翻译为英文。

#### Scenario: server.go 和 watcher.go 注释
- **WHEN** 检查 `server.go` 和 `watcher.go` 中的注释
- **THEN** 所有注释均为英文

## MODIFIED Requirements

### Requirement: CodeArts Executor PrepareRequest
`PrepareRequest` SHALL 设置与 Python 参考实现一致的请求头，包括 `Accept: text/event-stream`、`Heartbeat-Enable: true`、`Ide-Name`、`Ide-Version`、`Is-Confidential`、`X-Language`、`X-Snap-Traceid` 等。

### Requirement: CodeArts Executor Non-Stream Mode
`Execute`（非流式）方法 SHALL 在 payload 中设置 `stream: true`（因为 CodeArts API 即使非流式请求也返回 SSE），并添加注释说明此行为。

## REMOVED Requirements
无
