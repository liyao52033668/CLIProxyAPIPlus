# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go run ./cmd/sync_freebuff_catalog -write   # Regenerate the freebuff model from the upstream TS constants.
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Development Server Process Policy (Mandatory)
- Do not use `nohup`, `disown`, or similar daemonization to hide development services.
- Run development services in a visible foreground terminal so the user can stop them with `Ctrl+C`.
- If background execution is explicitly authorized, report the PID, log path, and exact stop command immediately.

## Architecture
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates

## Provider Modification Scope (Mandatory)
- `codebuddy` and `codebuddy_ai` are two **separate providers**, even though their names and implementations look similar.
  - `codebuddy`: `internal/auth/codebuddy/`, `internal/runtime/executor/codebuddy_executor.go` (upstream: copilot.tencent.com)
  - `codebuddy_ai`: `internal/auth/codebuddy_ai/`, `internal/runtime/executor/codebuddy_ai_executor.go` (upstream: www.codebuddy.ai)
- When modifying one provider, **never touch the other**. Apply changes only to the provider named in the request; if the same change seems applicable to both, propose it but do not apply it without an explicit ask.

## Code Conventions
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, the `cmd/fetch_antigravity_models` utility timeouts, and the Cursor upstream liveness deadlines in `internal/runtime/executor/cursor_transport.go` (`CURSOR_FIRST_TIMEOUT` / `CURSOR_FIRST_OUTPUT_TIMEOUT` / `CURSOR_IDLE_STOP`, all env-tunable and disabled at 0)


## Review Policy (Mandatory)
- Automatic execution of reviews, code reviews, project-wide checks, or similar operations upon task completion is **prohibited** by default.
- Reviews may only be performed when explicitly requested by the user (e.g., "Review," "Check the entire project," "Comprehensive check").
- Upon completing a single task, partial modification, or minor change, the task must be concluded immediately; do not initiate any reviews, tests, or expanded checks on your own initiative.
- Adhere strictly to the task scope defined by the user; do not add extra steps simply because the task has been completed.
