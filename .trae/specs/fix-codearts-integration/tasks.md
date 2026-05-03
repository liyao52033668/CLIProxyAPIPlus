# Tasks

- [x] Task 1: 修复 `signer.go` body hash bug — POST 请求签名必须包含实际 body 的 SHA256 hash
  - [x] SubTask 1.1: 修改 `SignRequest` 函数签名，接受 body 参数
  - [x] SubTask 1.2: 计算 body SHA256 hash 替代空 hash
  - [x] SubTask 1.3: 更新所有 `SignRequest` 调用点（executor、auth refresh）
  - [x] SubTask 1.4: 验证签名结果与 Python 参考实现一致

- [x] Task 2: 修复 `buildCodeArtsPayload` 对齐 Python 参考实现
  - [x] SubTask 2.1: 将 messages 格式从 `[{role, content}]` 改为 `[{type: "text", content}]`
  - [x] SubTask 2.2: 添加 chat_id (UUID)、client、task、task_parameters 完整结构
  - [x] SubTask 2.3: 添加 is_delta_response、user_id、attempt、parent_message_id 字段
  - [x] SubTask 2.4: 修复 tool_calls/tool result 消息格式对齐 Python 实现
  - [x] SubTask 2.5: 添加 tools 和 temperature 到 task_parameters 中

- [x] Task 3: 修复 `PrepareRequest` 请求头对齐 Python 参考
  - [x] SubTask 3.1: 添加 Accept: text/event-stream、Heartbeat-Enable: true 等缺失头
  - [x] SubTask 3.2: 添加 Ide-Name、Ide-Version、Is-Confidential、X-Language、X-Snap-Traceid
  - [x] SubTask 3.3: 更新 SignRequest 调用传入 body

- [x] Task 4: 新增 `sdk/auth/codearts.go` SDK Authenticator
  - [x] SubTask 4.1: 实现 `CodeArtsAuthenticator` struct，Provider() 返回 "codearts"
  - [x] SubTask 4.2: 实现 `RefreshLead()` 返回 4 小时
  - [x] SubTask 4.3: 实现 `Login()` 方法：启动回调服务器 → 生成 ticket → 打开浏览器 → 轮询 → 保存凭证

- [x] Task 5: 注册 CodeArts 到 SDK auth 系统
  - [x] SubTask 5.1: 在 `sdk/auth/refresh_registry.go` 注册 codearts refresh lead
  - [x] SubTask 5.2: 在 `internal/cmd/auth_manager.go` 注册 `NewCodeArtsAuthenticator()`

- [x] Task 6: 新增 `--codearts-login` CLI flag 和登录命令
  - [x] SubTask 6.1: 在 `cmd/server/main.go` 添加 `codeartsLogin` flag
  - [x] SubTask 6.2: 在 flag 处理分支添加 `else if codeartsLogin` 调用
  - [x] SubTask 6.3: 创建 `internal/cmd/codearts_login.go` 实现 `DoCodeArtsLogin`

- [x] Task 7: 新增 CodeArts thinking provider
  - [x] SubTask 7.1: 创建 `internal/thinking/provider/codearts/apply.go`
  - [x] SubTask 7.2: 实现 Apply 方法，将 thinking level 映射到请求参数
  - [x] SubTask 7.3: 在 `internal/runtime/executor/helps/thinking_providers.go` 注册

- [x] Task 8: 修复文档注释
  - [x] SubTask 8.1: 更新 `config.example.yaml` 中 oauth-model-alias Supported channels 添加 codearts
  - [x] SubTask 8.2: 更新 `config.example.yaml` 中 oauth-excluded-models Supported channels 添加 codearts 移除 qwen
  - [x] SubTask 8.3: 更新 `internal/config/config.go` 中 OAuthExcludedModels 注释添加 codearts

- [x] Task 9: 翻译中文注释为英文
  - [x] SubTask 9.1: 翻译 `internal/api/server.go` 中的中文注释
  - [x] SubTask 9.2: 翻译 `internal/watcher/watcher.go` 中的中文注释

- [x] Task 10: 编译验证
  - [x] SubTask 10.1: `go build -o test-output ./cmd/server && rm test-output`
  - [x] SubTask 10.2: `gofmt -w .`

# Task Dependencies
- Task 1 (signer fix) → Task 3 (PrepareRequest uses signer)
- Task 4 (SDK authenticator) → Task 5 (registration) → Task 6 (CLI flag)
- Task 2 (payload fix) and Task 3 (headers) are independent of Task 4-6
- Task 7 (thinking provider) is independent
- Task 8 (docs) and Task 9 (comments) are independent
- Task 10 depends on all others
