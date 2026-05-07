# CLIProxyAPIPlus Web Management Center Spec

## Why
当前 Management Center 是一个从 GitHub Releases 动态下载的单文件 SPA，无法内嵌到二进制中，离线环境不可用，且开发体验受限。需要基于 Next.js + shadcn/ui 构建一个现代化的 Web 管理面板，放在 `./web` 目录下，最终通过 `//go:embed` 内嵌到 Go 二进制中。

## What Changes
- 在 `./web` 下创建 Next.js + shadcn/ui 项目
- 实现完整的管理面板前端，对接现有 `/v0/management/*` REST API
- 在 Go 后端新增 `GET /v0/management/oauth-providers` 端点，返回支持 OAuth 认证的 Provider 列表
- 修改 Go 后端 `serveManagementControlPanel` 逻辑，支持从嵌入的静态资源 fallback 提供前端
- 使用 `//go:embed` 将 Next.js 静态导出产物嵌入 Go 二进制

## Impact
- Affected specs: Management API, OAuth authentication, static asset serving
- Affected code:
  - `internal/api/server.go` — 新增路由、修改静态文件服务逻辑
  - `internal/api/handlers/management/` — 新增 OAuth providers handler
  - `internal/managementasset/` — 新增 embed fallback 逻辑
  - `sdk/auth/manager.go` — 暴露已注册 provider 列表
  - 新增 `./web/` 目录 — Next.js 前端项目

## ADDED Requirements

### Requirement: Next.js Web Management Center
系统 SHALL 在 `./web` 目录下提供一个基于 Next.js + shadcn/ui 的 Web 管理面板，覆盖现有 Management API 的所有功能。

#### Scenario: 用户访问管理面板
- **WHEN** 用户通过浏览器访问 `/management.html` 或 `/`（根路径）
- **THEN** 系统返回嵌入的 Next.js 静态导出产物

#### Scenario: 用户查看 Dashboard
- **WHEN** 用户打开 Dashboard 页面
- **THEN** 显示服务器状态概览（版本、运行时间、配置摘要、Provider 状态）

#### Scenario: 用户管理 Auth Files
- **WHEN** 用户在 Auth Files 页面操作
- **THEN** 可以查看、上传、删除、启用/禁用认证文件，编辑 prefix/proxy_url/priority/note 字段

#### Scenario: 用户发起 OAuth 认证
- **WHEN** 用户点击某个 OAuth Provider 的登录按钮
- **THEN** 系统发起 OAuth 流程，打开认证 URL，轮询 `/get-auth-status` 直到完成

#### Scenario: 用户管理 API Keys
- **WHEN** 用户在 API Keys 页面操作
- **THEN** 可以 CRUD 管理 api-keys、gemini-api-key、claude-api-key、codex-api-key、vertex-api-key、openai-compatibility

#### Scenario: 用户查看 Usage 统计
- **WHEN** 用户打开 Usage 页面
- **THEN** 显示使用量统计，支持导出和导入

#### Scenario: 用户查看 Logs
- **WHEN** 用户打开 Logs 页面
- **THEN** 实时显示日志流，支持查看错误日志和请求日志

#### Scenario: 用户编辑配置
- **WHEN** 用户在 Config 页面操作
- **THEN** 可以查看和编辑 config.yaml，修改各项设置（debug、proxy-url、routing strategy 等）

### Requirement: OAuth Providers API Endpoint
系统 SHALL 提供 `GET /v0/management/oauth-providers` 端点，返回当前支持 OAuth 认证的 Provider 列表。

#### Scenario: 获取 OAuth Provider 列表
- **WHEN** 客户端发送 `GET /v0/management/oauth-providers`
- **THEN** 返回 JSON 格式的 Provider 列表，包含 key、display_name、flow_type、auth_url_endpoint 等信息

#### Scenario: 响应格式
```json
{
  "providers": [
    {
      "key": "claude",
      "display_name": "Claude (Anthropic)",
      "flow_type": "authorization_code_pkce",
      "auth_url_endpoint": "/anthropic-auth-url",
      "aliases": ["anthropic"]
    }
  ]
}
```

### Requirement: Web UI Embedding
系统 SHALL 通过 `//go:embed` 将 Next.js 静态导出产物嵌入 Go 二进制，并在磁盘文件不可用时作为 fallback。

#### Scenario: 首次启动无磁盘文件
- **WHEN** 服务器首次启动且磁盘上没有 management.html
- **THEN** 使用嵌入的静态资源响应请求

#### Scenario: 磁盘文件存在
- **WHEN** 磁盘上存在通过自动更新下载的 management.html
- **THEN** 优先使用磁盘版本（支持后续自动更新）

#### Scenario: 离线环境
- **WHEN** 服务器在无网络环境下启动
- **THEN** 嵌入的静态资源确保管理面板可用

## MODIFIED Requirements

### Requirement: Static Asset Serving
原有逻辑：仅从磁盘文件或 GitHub Releases 下载提供 management.html。
修改为：优先使用磁盘文件 → 下载到磁盘 → fallback 到嵌入资源。支持 Next.js 静态导出的多文件结构（index.html + _next/ 资源目录）。

## REMOVED Requirements

无移除的需求。现有 Management Center 的自动更新机制保持不变，嵌入资源仅作为 fallback。
