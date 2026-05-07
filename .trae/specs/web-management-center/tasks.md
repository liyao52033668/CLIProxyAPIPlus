# Tasks

- [x] Task 1: 初始化 Next.js + shadcn/ui 项目
  - [x] SubTask 1.1: 在 `./web` 下用 `npx shadcn@latest init` 初始化 Next.js 项目（App Router、TypeScript、Tailwind CSS v4）
  - [x] SubTask 1.2: 安装核心 shadcn 组件：button, card, tabs, table, dialog, sheet, badge, input, select, switch, textarea, separator, skeleton, alert, dropdown-menu, tooltip, avatar, form, sonner, sidebar, scroll-area, empty, spinner, field, field-group
  - [x] SubTask 1.3: 配置项目结构：`src/app/`（页面路由）、`src/components/`（组件）、`src/lib/`（工具函数和 API client）、`src/hooks/`（自定义 hooks）
  - [x] SubTask 1.4: 创建 API client 层（`src/lib/api.ts`），封装所有 `/v0/management/*` 端点调用，包含认证 header 管理
  - [x] SubTask 1.5: 创建 Management Key 认证上下文和登录页面

- [x] Task 2: 实现 Dashboard 页面
  - [x] SubTask 2.1: 创建 Sidebar 导航布局（Dashboard、Config、Auth Files、OAuth、API Keys、Usage、Logs）
  - [x] SubTask 2.2: 实现 Dashboard 页面：服务器版本、运行状态、Provider 概览、配置摘要卡片

- [x] Task 3: 实现 Config 页面
  - [x] SubTask 3.1: 创建 Config 编辑器页面，支持查看和编辑 config.yaml（代码编辑器 + 表单混合模式）
  - [x] SubTask 3.2: 实现各项设置的开关/输入控件：debug、logging-to-file、usage-statistics、request-log、ws-auth、force-model-prefix、proxy-url、request-retry、max-retry-interval、routing-strategy

- [x] Task 4: 实现 Auth Files 页面
  - [x] SubTask 4.1: 创建 Auth Files 列表页，展示所有认证文件（Table 组件），显示 provider、label、status、email、priority 等字段
  - [x] SubTask 4.2: 实现上传认证文件功能（Dialog + 文件选择）
  - [x] SubTask 4.3: 实现删除认证文件功能（AlertDialog 确认）
  - [x] SubTask 4.4: 实现编辑认证文件字段（prefix、proxy_url、headers、priority、note）
  - [x] SubTask 4.5: 实现启用/禁用认证文件（Switch 组件）

- [x] Task 5: 实现 OAuth 页面
  - [x] SubTask 5.1: 创建 OAuth Providers 列表页，展示所有支持 OAuth 的 Provider（调用新的 `/oauth-providers` 端点）
  - [x] SubTask 5.2: 实现各 Provider 的 OAuth 登录流程：发起认证 → 打开 URL → 轮询状态 → 显示结果
  - [x] SubTask 5.3: 实现 OAuth Model Alias 管理和 OAuth Excluded Models 管理

- [x] Task 6: 实现 API Keys 页面
  - [x] SubTask 6.1: 创建 API Keys 管理页，使用 Tabs 切换不同类型（api-keys、gemini、claude、codex、vertex、openai-compatibility）
  - [x] SubTask 6.2: 实现每种 Key 类型的 CRUD 操作（Table + Dialog 表单）
  - [x] SubTask 6.3: 实现 AmpCode 配置管理（upstream-url、upstream-api-key、model-mappings）

- [x] Task 7: 实现 Usage 页面
  - [x] SubTask 7.1: 创建 Usage 统计页面，展示使用量数据（Table + Card 汇总）
  - [x] SubTask 7.2: 实现导出和导入功能（Button + Dialog）

- [x] Task 8: 实现 Logs 页面
  - [x] SubTask 8.1: 创建 Logs 查看页面，实时流式显示日志（ScrollArea + 自动滚动）
  - [x] SubTask 8.2: 实现错误日志列表和下载
  - [x] SubTask 8.3: 实现请求日志查看和清理

- [x] Task 9: Go 后端 — 新增 OAuth Providers API
  - [x] SubTask 9.1: 在 `sdk/auth/manager.go` 中添加 `ListProviders()` 方法，返回已注册的 Provider 信息
  - [x] SubTask 9.2: 在 `internal/api/handlers/management/` 中新增 `oauth_providers.go`，实现 `GetOAuthProviders` handler
  - [x] SubTask 9.3: 在 `internal/api/server.go` 的 management 路由组中注册 `GET /oauth-providers`
  - [x] SubTask 9.4: 编写单元测试

- [x] Task 10: Go 后端 — Web UI 嵌入
  - [x] SubTask 10.1: 在 `internal/managementasset/` 中新增 `embed.go`，使用 `//go:embed` 嵌入 Next.js 静态导出产物
  - [x] SubTask 10.2: 修改 `serveManagementControlPanel`，支持多文件静态资源服务（index.html + _next/ 目录）
  - [x] SubTask 10.3: 实现 fallback 逻辑：磁盘文件优先 → 嵌入资源 fallback
  - [x] SubTask 10.4: 添加构建脚本/Makefile target，将 `cd web && npm run build` 的产物复制到嵌入目录
  - [x] SubTask 10.5: 验证编译通过（`go build -o test-output ./cmd/server && rm test-output`）

# Task Dependencies
- [Task 2-8] 依赖 [Task 1]（项目初始化和 API client）
- [Task 5] 依赖 [Task 9]（OAuth Providers API）
- [Task 10] 依赖 [Task 1-8]（前端构建产物需要存在才能嵌入）
- [Task 2-8] 之间可并行开发
