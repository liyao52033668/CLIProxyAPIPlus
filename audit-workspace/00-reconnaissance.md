# Reconnaissance Report
Generated: 2026-07-26 20:10 CST

## Repository Overview
- Repository: CLIProxyAPIPlus (github.com/router-for-me/CLIProxyAPI, module `github.com/router-for-me/CLIProxyAPI/v7`)
- Total files (excluding .git/.codegraph/CLIProxyAPI-data): ~8,144
- Total directories: ~1,528
- Estimated lines of Go code: ~299,600 (internal: 249,425 across 881 files; sdk: 43,294 across 158 files; cmd: 1,827 across 7 files; test: 4,673 across 5 files; examples: 407 across 3 files)
- Primary language: Go 1.26 (essentially 100% of source)
- Frontend: single file `static/management.html` (167 lines)
- Package manager: Go modules (go.mod + go.sum, module v7)

## Technology Stack
- **Purpose**: Proxy server providing OpenAI/Gemini/Claude/Codex-compatible APIs with OAuth flows and round-robin load balancing across provider credentials.
- **HTTP framework**: gin-gonic/gin v1.12.0
- **WebSockets**: gorilla/websocket v1.5.3 (Codex/xAI websocket executors, wsrelay)
- **Database/ORM**: gorm v1.31.1 with postgres (pgx v5) and sqlite drivers; optional Postgres/git/object-store storage backends (PGSTORE_*, GITSTORE_*, OBJECTSTORE_*)
- **Object storage**: minio-go v7
- **Git storage**: go-git v6.0.0-alpha.4 (note: alpha dependency)
- **TUI**: charmbracelet bubbletea/bubbles/lipgloss (`--tui`, `--standalone`)
- **Auth**: OAuth flows (golang.org/x/oauth2), token/auth files under `auths/`
- **TLS fingerprinting**: refraction-networking/utls v1.8.2
- **Logging**: logrus + lumberjack rotation
- **JSON manipulation**: tidwall/gjson + sjson (heavy use in translators)
- **Tokenization**: tiktoken-go/tokenizer
- **Config**: yaml.v3; `config.yaml` (39KB, live config committed in working dir), `config.example.yaml`; `.env` auto-loaded (a `.env` FILE EXISTS in repo root — check gitignore status)
- **CI/CD**: GitHub Actions (`agents-md-guard.yml`, `auto-retarget-main-pr-to-dev.yml`, `pr-path-guard.yml`, `pr-test-build.yml`, `release-stable.yaml`); goreleaser; CNB pipeline (.cnb.yml)
- **Docker**: Dockerfile, docker-compose.yml, docker-compose.cluster.yml
- **Testing**: standard `go test`; unit tests co-located; cross-module integration tests in `test/`

## Directory Map (source roots)
- `cmd/server/` — server entrypoint; other cmd utilities (e.g. fetch_antigravity_models)
- `internal/api/` — Gin HTTP API: server.go, protocol multiplexer, buffered conn, redis queue protocol, `handlers/`, `middleware/`, `modules/` (incl. `modules/amp/` reverse proxy)
- `internal/runtime/executor/` — per-provider executors (claude, codex websocket, xai websocket, antigravity, openai-compat...) + `helps/`
- `internal/translator/` — provider protocol translators (antigravity, codex, claude, gemini, openai + shared common)
- `internal/thinking/` — thinking/reasoning pipeline (apply.go, suffix.go, types.go, validate.go, convert.go, provider appliers)
- `internal/auth/` — OAuth/token acquisition
- `internal/store/` — storage implementations & secret resolution (file, postgres, git, object store)
- `internal/registry/` — model registry + remote updater
- `internal/access/`, `internal/api/middleware/` — request auth/access control
- `internal/cache/` — request signature caching; `internal/signature/`
- `internal/watcher/` — config hot-reload
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — usage/token accounting
- `internal/tui/` — terminal UI
- `internal/managementasset/` — config snapshots, management assets (serves `static/management.html`)
- `internal/mailfetcher/`, `internal/browser/`, `internal/codexinspection/`, `internal/redisqueue/`, `internal/logging/`, `internal/util/`, `internal/misc/`, `internal/config/`, `internal/constant/`, `internal/home/`, `internal/interfaces/`, `internal/authfiles/`, `internal/buildinfo/`, `internal/cmd/`
- `sdk/` — embeddable SDK (cliproxy service/builder/pipeline, api handlers, auth, translator, access, config, logging, proxyutil)
- `test/` — integration tests
- `examples/` — SDK examples
- `static/management.html` — management UI page (167 lines)
- NOTE: top-level `CLIProxyAPI/` directory contains only runtime data (`auths/`, `data/`) — NOT source; treat contents as potentially sensitive credential material, do not print secrets.
- NOTE: repo root contains live `config.yaml` (39KB) and `.env` — flag for secret exposure review, but NEVER quote actual secret values in reports; redact to first/last 4 chars max.

## Audit Plan
- Security: ACTIVE
- Performance: ACTIVE
- Accessibility: ACTIVE (scoped — only `static/management.html` exists; small scope)
- Dependency: ACTIVE (go.mod/go.sum; govulncheck may be run read-only if available)
- Code Quality: ACTIVE

## Scale Guidance
~300K lines → large-repo tier. Security and Code Quality agents should parallelize internally with sub-agents (e.g. by directory: internal/api + internal/auth + internal/store; internal/runtime + internal/translator; sdk + cmd + rest). Prioritize: auth, access control, store/secret resolution, API handlers/middleware, websocket executors, management API.

## Constraints (from repo policy)
- READ-ONLY audit: never modify, build, or execute project code; only `audit-workspace/` and the final report may be written. Dependency agent may run read-only audit commands (e.g. `govulncheck ./...` is allowed only if it does not modify the repo; go.sum download cache writes outside repo are acceptable).
- Do not print secret values from `.env`, `config.yaml`, `auths/`, or `CLIProxyAPI/` — reference file+line and redact.
