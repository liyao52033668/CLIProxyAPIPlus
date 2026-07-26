# Code Quality Audit Report
Generated: 2026-07-26
Auditor: Code Quality Agent (Overnight Repo Auditor)

## Executive Summary
- **Total findings**: 24 (CRITICAL: 1, HIGH: 6, MEDIUM: 11, LOW: 6)
- **Overall code health**: **FAIR**
- **Tech debt estimate**: **HIGH**
- Assessment: Core concurrency primitives (registry, selector, oauth sessions) are generally mutex-aware and gofmt is clean, but the codebase is dominated by multi-thousand-line executors/translators, a bidirectional `internal`↔`sdk` coupling, and uneven test coverage on auth/thinking/TUI. Maintainability risk is concentrated in hot-path god files rather than widespread style issues.

## Critical Findings

### [CRITICAL] Cursor proto encode path panics on marshal / descriptor failures
- **File**: `internal/auth/cursor/proto/encode.go` (lines 90–95, ~603); `internal/auth/cursor/proto/descriptor.go` (lines ~1222–1241)
- **Category**: Correctness / Panic in library path
- **Description**: `marshal()` panics on `proto.Marshal` failure; descriptor bootstrap panics if decode/unmarshal/create fails or message name is missing. These sit on the Cursor runtime encoding path used by the executor, not only in `main` or tests.
- **Evidence**:
  ```go
  func marshal(msg *dynamicpb.Message) []byte {
      b, err := proto.Marshal(msg)
      if err != nil {
          panic("cursor proto marshal: " + err.Error())
      }
      return b
  }
  ```
- **Impact**: A malformed dynamic message or descriptor drift can crash the whole proxy process instead of failing a single request.
- **Recommendation**: Return `([]byte, error)` from encode helpers; recover only at HTTP/WS handler edges if needed. Treat descriptor load failure as startup error, not per-call panic.
- **References**: AGENTS.md — “Avoid panics in HTTP handlers”; checklist item 4 & 10.

## High Findings

### [HIGH] Multiple hot-path god files (>2000 lines) in executor / service / config / API
- **File**: `internal/runtime/executor/antigravity_executor.go` (2908), `xai_executor.go` (2904), `claude_executor.go` (2387), `codex_executor.go` (2055), `codex_websockets_executor.go` (2014), `github_copilot_executor.go` (2007); `sdk/cliproxy/service.go` (2362); `internal/config/config.go` (2299); `internal/api/server.go` (2224)
- **Category**: Complexity
- **Description**: Nine non-test source files exceed 2000 lines; 35 exceed 1000. These sit on request execution, config, and server wiring — highest change frequency areas.
- **Evidence**: Line counts via `wc -l` on main tree (excluding `.claude/worktrees`, `CLIProxyAPI/`, secrets).
- **Impact**: Reviews become shallow, merge conflicts spike, and provider-specific bugs are hard to isolate. New providers copy the same mega-file pattern.
- **Recommendation**: Split each executor into: client/http, stream, non-stream, error mapping, model/tool helpers (package already allows `helps/`). Split `service.go` into lifecycle / model registration / home-runtime. Keep `config.go` types vs load/save vs defaults in separate files.
- **References**: Checklist item 3.

### [HIGH] Auth conductor remains a multi-file god package (~5K non-test LOC)
- **File**: `sdk/cliproxy/auth/conductor_execute.go` (1244), `conductor_select.go` (966), `conductor_result.go` (941), `conductor_models.go` (748), `conductor_refresh_http.go` (633), `conductor.go` (427) — **4959 lines** non-test total
- **Category**: Complexity / Architecture
- **Description**: Conductor was partially split by filename but still concentrates selection, execution, refresh, result mapping, and model alias logic in one package with very large units (`conductor_execute.go` alone is 1244 lines).
- **Impact**: Auth routing changes require reading thousands of lines; regression surface is enormous (many companion tests exist, which is good, but cognitive load remains high).
- **Recommendation**: Extract pure selection policy, retry/cooldown policy, and HTTP refresh into interfaces with narrow files; keep Manager as orchestration only.
- **References**: Checklist item 3, 6.

### [HIGH] Extreme function-level complexity on streaming and protocol conversion paths
- **File**: Multiple; worst offenders:
  - `internal/runtime/executor/kiro_eventstream.go:723` `streamToChannel` (**1113 lines**)
  - `cmd/server/main.go:262` `main` (**750 lines**)
  - `internal/translator/antigravity/claude/antigravity_claude_request.go:310` `ConvertClaudeRequestToAntigravity` (**502 lines**)
  - Several response converters 400–500 lines; `cursor_executor.ExecuteStream` 410 lines; `xai_websockets_executor.ExecuteStream` 353 lines
- **Category**: Complexity
- **Description**: Heuristic scan found **237** functions ≥100 lines, **60** ≥200, **26** ≥300 in non-test code.
- **Impact**: Nearly impossible to unit-test edge branches inside 500–1000 line functions; streaming bugs and off-by-one chunk errors hide easily.
- **Recommendation**: Decompose stream parsers into state machines with per-event handlers; extract translator stages (messages, tools, thinking, media) into helpers with golden tests.
- **References**: Checklist item 3, 10.

### [HIGH] Bidirectional coupling: `sdk` imports `internal` extensively (embeddable SDK is not a clean boundary)
- **File**: `sdk/cliproxy/service.go` (imports `internal/api`, `internal/auth/kiro`, `internal/config`, `internal/registry`, `internal/runtime/executor`, `internal/watcher`, `internal/usage/keeper/*`, …); `sdk/config/config.go` type-aliases to `internal/config`; ~**221** sdk→internal import sites vs ~**330** internal→sdk
- **Category**: Architecture
- **Description**: Public SDK packages re-export or directly depend on `internal/*`. Meanwhile `internal` heavily depends on `sdk/cliproxy` (auth manager, executor interfaces). This is intentional “facade + core” but creates circular-ish design pressure and means external embedders transitively couple to internal packages via type aliases that still point at internal concrete types.
- **Evidence**:
  ```go
  // sdk/config/config.go
  type Config = internalconfig.Config
  // sdk/cliproxy/service.go imports many internal packages
  ```
- **Impact**: Cannot evolve `internal` without SDK breakage; testing and module boundaries blur; “internal” naming no longer means private.
- **Recommendation**: Define stable DTOs/interfaces in `sdk/` (or a `pkg/` API layer); have `internal` implement them. Keep re-exports as thin aliases only after types live on the public side, or document that `sdk` is an embedding shell not a stable API.
- **References**: Checklist item 6; mission note on sdk mirror pattern.

### [HIGH] Translator matrix: structural duplication + high coupling, restricted change policy
- **File**: `internal/translator/**` (~27.6K non-test LOC, 116 source files); many `Convert*Request` 400–800+ lines using gjson/sjson
- **Category**: Duplication / Architecture
- **Description**: N×M format converters (OpenAI/Claude/Gemini/Codex/Antigravity/Kiro/…) repeat message/tool/thinking mapping patterns with copy-paste variants. Repo policy restricts standalone translator edits, increasing coordination cost.
- **Evidence**: Top request converters 578–1010 lines; shared patterns of `gjson.GetBytes` / `sjson.SetBytes` without shared intermediate IR in many paths (thinking package is a partial IR for one concern only).
- **Impact**: Bugfixes must be replicated across pairs; inconsistent edge-case handling (tool choice, image parts, signatures) across providers.
- **Recommendation**: Expand the “canonical representation → per-provider translation” model already used by `internal/thinking` to messages/tools; extract shared content-part normalizers. Prefer golden JSON fixtures per pair.
- **References**: AGENTS.md translator policy; checklist 2, 3.

### [HIGH] Systematic non-English comments violate project convention (Kiro/auth/usage)
- **File**: Concentrated in `internal/auth/kiro/*` (token_repository, rate_limiter, background_refresh), `internal/runtime/executor/kiro_executor.go`, `internal/usage/keeper/**`, root `recover.go`; also `internal/tui/i18n.go` (UI strings — different concern)
- **Category**: Naming & conventions
- **Description**: **~380** non-test lines match CJK characters in `.go` files (402 including tests). AGENTS.md requires English-only comments.
- **Evidence**: Sample from `internal/auth/kiro/token_repository.go` — package/type/method comments entirely in Chinese.
- **Impact**: Inconsistent onboarding for international contributors; automated style enforcement fails; conflicts with stated repo policy.
- **Recommendation**: Translate code comments to English in a dedicated cleanup PR (keep user-visible TUI i18n strings as-is). Add CI grep for CJK in `//` comments excluding `i18n.go` and string literals if feasible.
- **References**: AGENTS.md Code Conventions; checklist 9.

## Medium Findings

### [MEDIUM] Test coverage highly uneven: TUI zero tests; auth/thinking thin; executor/translator better
- **File**: Directory metrics (source files / companion test ratio)
- **Category**: Testing gaps
- **Description**:
  | Area | src files | test files | src LOC | test LOC | Notes |
  |---|---:|---:|---:|---:|---|
  | `internal/runtime/executor` | 61 | 54 | 41.7K | 22.8K | Good file ratio; still 33 sources without companion test |
  | `internal/translator` | 116 | 42 | 27.6K | 14.4K | 78 sources without companion test |
  | `internal/auth` | 93 | 17 | 22.6K | 3.9K | Weak |
  | `internal/thinking` | 19 | 6 | 3.3K | 0.4K | Weak for core pipeline |
  | `internal/tui` | 13 | **0** | 4.2K | **0** | No tests |
  | `sdk` | 91 | 67 | 28.2K | 15.1K | Solid |
  | Overall (main tree) | 696 | 356 | 198K | 101K | ~51% test/src line ratio |
- **Impact**: Auth refresh/OAuth and thinking conversion regressions more likely; TUI breaks silently.
- **Recommendation**: Prioritize unit tests for `internal/thinking` appliers and auth refresh/rate-limit; smoke-test TUI models with bubbletea test harness for pure state transitions.
- **References**: Checklist 7.

### [MEDIUM] Global mutable singletons proliferate (registry, caches, oauth sessions, kiro limiters, browser)
- **File**: `internal/registry/model_registry.go` (`globalRegistry`); `internal/cache/signature_cache.go` (`sync.Map`); `internal/api/handlers/management/oauth_sessions.go` (`oauthSessions`); `internal/auth/kiro/rate_limiter_singleton.go`; `internal/browser/browser.go` (`incognitoMode`, `lastBrowserProcess`); many `init()` (~55)
- **Category**: Architecture / Correctness risks
- **Description**: Process-wide mutable state is common. Most critical maps use locks (`ModelRegistry.mutex`, `oauthSessionStore.mu`, `RoundRobinSelector.mu`), which is good, but globals complicate multi-tenant embedding, tests, and lifecycle (e.g. kiro cooldown goroutine needs explicit `ShutdownRateLimiters`).
- **Impact**: Harder isolation in tests; subtle lifecycle leaks if shutdown not called; embedders share global registry via `GlobalModelRegistry()`.
- **Recommendation**: Prefer Manager/Service-owned instances; inject registries. Keep globals only for true process-wide catalogs with documented lifecycle.
- **References**: Checklist 6, 10.

### [MEDIUM] Unchecked type assertions in signature cache and map plumbing
- **File**: `internal/cache/signature_cache.go` (lines 71–76, 95, 201, 264); widespread `map[string]any` (~911 occurrences of map[string]any/interface{})
- **Category**: Type safety
- **Description**:
  ```go
  if val, ok := signatureCache.Load(groupKey); ok {
      return val.(*groupCache) // panics if wrong type stored
  }
  actual, _ := signatureCache.LoadOrStore(groupKey, sc)
  return actual.(*groupCache)
  ```
  Model listing APIs return `[]map[string]any` instead of structured DTOs (`GetAvailableModels`).
- **Impact**: Wrong-type store → panic; loose maps hide schema drift between handlers and registry.
- **Recommendation**: Use typed `sync.Map` wrappers with ok-form asserts; introduce stable model DTO structs for API responses.
- **References**: Checklist 5, 10.

### [MEDIUM] Deferred Close error handling inconsistently applied
- **File**: Repo-wide; ~**61** bare `defer x.Close()` vs ~**109** wrapped `defer func(){ _ = x.Close() }` patterns
- **Category**: Error handling / Conventions
- **Description**: AGENTS.md asks to wrap defer Close errors with logging. Many auth/HTTP paths still use bare `defer resp.Body.Close()` (e.g. codearts, joycode, kilo).
- **Impact**: Silent close failures; inconsistent style; occasional fd leaks harder to diagnose.
- **Recommendation**: Standardize on `defer func(){ if err := f.Close(); err != nil { log.… } }()` or a small `closer.Close(err, log)` helper; apply in new code first.
- **References**: AGENTS.md; checklist 4, 9.

### [MEDIUM] Management OAuth handlers use `context.Background()` and fire-and-forget goroutines
- **File**: Many `internal/api/handlers/management/oauth_handlers_*.go` (e.g. `oauth_handlers_claude.go:21`, `:62`); ~**218** non-cmd `context.Background()` uses
- **Category**: Correctness risks / Architecture
- **Description**: OAuth start handlers ignore request context, spawn `go func()` waiters without recover, and use `fmt.Println` for operator messaging mixed with logrus.
- **Impact**: Cannot cancel in-flight OAuth wait on client disconnect; panic in goroutine kills process; harder structured logging.
- **Recommendation**: Derive context from request with timeout; add recover in OAuth waiter goroutines; replace `fmt.Println` with logrus.
- **References**: Checklist 4, 10.

### [MEDIUM] AMP proxy handler re-panics non-error values after recovery
- **File**: `internal/api/modules/amp/routes.go` (lines 185–200)
- **Category**: Error handling / Panic
- **Description**: Recover logs and returns JSON for `error` panics, but `panic(rec)` rethrows for non-error values, relying on a global recovery middleware.
- **Impact**: Depends on outer middleware always being present; string panics may still abort connection ungracefully if middleware order changes.
- **Recommendation**: Always convert recovered value to 502 response; never re-panic in HTTP handlers.
- **References**: Checklist 4.

### [MEDIUM] `http.Client.Timeout` optional path in executor proxy helper can bound full request lifecycle
- **File**: `internal/runtime/executor/helps/proxy_helpers.go` (lines 60–87); callers may pass non-zero timeout
- **Category**: Correctness / Conventions
- **Description**: AGENTS.md forbids timeouts after upstream connection is established (with four named exceptions). Helper documents `timeout: 0 means no timeout` but still supports client-level Timeout which covers headers+body for the entire request.
- **Impact**: Long-running streams can be killed if any caller passes a positive timeout; policy drift between providers.
- **Recommendation**: Audit all call sites; prefer dial/handshake timeouts only (as `kiro_http.go` comments already state). Consider removing Timeout parameter or renaming to dial-only.
- **References**: AGENTS.md Timeouts rule; checklist 10.

### [MEDIUM] Business logic density in HTTP management handlers
- **File**: `internal/api/handlers/management/config_lists.go` (1566), `api_tools.go` (1505), `auth_files_*`, large OAuth handlers
- **Category**: Architecture
- **Description**: Management handlers embed validation, persistence, proxy HTTP calls, and token refresh. Partial split of `auth_files.go` into satellites is good but logic remains presentation-coupled.
- **Impact**: Hard to reuse from CLI/TUI/SDK; tests need gin context.
- **Recommendation**: Move domain operations to services (auth file store, OAuth session service, config list service); handlers only bind HTTP.
- **References**: Checklist 6.

### [MEDIUM] Docs asymmetry: English README thin; Japanese README rich; SDK godoc incomplete
- **File**: `README.md` (~1.5KB), `README_CN.md` (~1.3KB), `README_JA.md` (~18KB); SDK exported symbols ~293 with ~49 missing preceding comments
- **Category**: Docs & DX
- **Description**: Primary README is contribution/CI focused for Plus fork, not operational docs. `config.example.yaml` is comparatively strong (~28KB, well commented). Public SDK surface missing docs on several authenticators and helpers.
- **Impact**: New operators and embedders under-documented in English/Chinese; Japanese docs may drift from Plus-specific behavior.
- **Recommendation**: Port operational sections from README_JA / upstream into README.md; require godoc on all exported `sdk/` symbols in CI (`gonew`/`golint` style check).
- **References**: Checklist 8.

### [MEDIUM] Executor free-function / stream helper duplication across providers
- **File**: `internal/runtime/executor/*_executor.go` (pattern: Execute / ExecuteStream / error classify / body build)
- **Category**: Duplication
- **Description**: Each provider reimplements similar retry, stream pump, SSE parse, and error classification. Some shared helpers exist under `helps/` but adoption is uneven (kiro has private http pool; others use proxy helpers differently).
- **Impact**: Fixing a streaming race or retry bug requires N provider PRs.
- **Recommendation**: Shared stream pump + retry policy interfaces; provider only supplies request build and event parse.
- **References**: Checklist 2.

### [MEDIUM] `log.Fatal` present in root utility `recover.go` (outside cmd main)
- **File**: `recover.go` (lines 22, 28, 41, 143)
- **Category**: Error handling / Conventions
- **Description**: Utility uses `log.Fatal` which terminates the process. File also contains Chinese user-facing strings. Not on server hot path, but violates “no log.Fatal outside main” spirit if imported.
- **Impact**: Low production risk if unused by server; policy inconsistency.
- **Recommendation**: Move under `cmd/` or return errors; avoid Fatal in library-like files.
- **References**: AGENTS.md; checklist 4.

## Low Findings

### [LOW] TODO/FIXME inventory nearly clean
- **File**: `internal/auth/codebuddy/codebuddy_auth.go:204` (only real `TODO`)
- **Category**: Dead code
- **Description**: Grep for TODO/FIXME/HACK found essentially **1** actionable TODO (other XXX hits are string/doc false positives). No large commented-out code blocks detected.
- **Impact**: Positive signal — little abandoned marker debt.
- **Recommendation**: Resolve CodeBuddy denial error-code TODO when API docs available.
- **References**: Checklist 1 — **mostly Clean**.

### [LOW] gofmt adherence excellent
- **File**: entire tree (excluding secrets/worktrees)
- **Category**: Naming & conventions
- **Description**: `gofmt -l` returned **0** files.
- **Impact**: None — healthy.
- **Recommendation**: Keep gofmt/goimports in CI.
- **References**: Checklist 9 — **Clean** for formatting.

### [LOW] Floating goroutines often lack local recover
- **File**: ~169 `go` starts without nearby `recover` vs ~4 with (heuristic)
- **Category**: Correctness risks
- **Description**: Background work (OAuth waiters, keepalives, mux routing, cache cleanup) rarely installs recover. Process-level risk depends on runtime panic behavior (unrecovered panic in any goroutine crashes the program).
- **Impact**: Rare panics become full outages.
- **Recommendation**: Standard `go safeGo(fn)` helper with recover+log for fire-and-forget tasks.
- **References**: Checklist 10.

### [LOW] Examples use panic for control flow
- **File**: `examples/custom-provider/main.go`, `examples/http-request/main.go`
- **Category**: Error handling
- **Description**: Demo code panics on errors — acceptable for examples but may be copy-pasted.
- **Impact**: Low.
- **Recommendation**: Prefer `log.Fatal` in `main` only or checked errors in examples README.
- **References**: Checklist 4.

### [LOW] Package `init()` registration used heavily for translators/thinking
- **File**: ~20 translator `init.go` files; thinking provider `apply.go` inits; registry updaters
- **Category**: Architecture
- **Description**: Side-effect registration via `init()` is intentional plugin style but obscures dependency graphs and test setup order.
- **Impact**: Import side effects; harder dead-code elimination.
- **Recommendation**: Document required blank imports; optional explicit `RegisterAll()` for tests.
- **References**: Checklist 6.

### [LOW] Root `recover.go` appears orphaned operational script
- **File**: `/recover.go` (161 lines)
- **Category**: Dead code / DX
- **Description**: SQLite usage DB recovery utility at repo root with Chinese UI, `log.Fatal`, not under `cmd/`.
- **Impact**: Clutters module root; may confuse newcomers.
- **Recommendation**: Move to `cmd/recover-usage-db` or `scripts/`.
- **References**: Checklist 1.

## Code Health Metrics

| Metric | Value |
|---|---|
| Non-test files >1000 lines | **35** |
| Non-test files >2000 lines | **9** |
| Functions ≥100 / ≥200 / ≥300 lines | **237 / 60 / 26** |
| Worst function | `streamToChannel` **1113** lines (`kiro_eventstream.go`) |
| Real TODO/FIXME | **1** |
| gofmt dirty files | **0** |
| CJK hits in non-test `.go` | **~380** |
| `init()` funcs (non-test) | **55** |
| Test/src file ratio (overall) | **51%** (356/696) |
| Test/src line ratio (overall) | **~51%** |
| `internal/tui` tests | **0** |
| `map[string]any` / `interface{}` density | **~911** map any; **~1465** any/interface hits |
| sdk→internal import sites | **~221** |
| internal→sdk import sites | **~330** |
| Conductor non-test LOC | **~4959** |
| Bare `defer x.Close()` | **~61** |
| `context.Background()` outside cmd/examples | **~218** |

### Largest non-test source files (top 15)
1. `internal/runtime/executor/antigravity_executor.go` — 2908
2. `internal/runtime/executor/xai_executor.go` — 2904
3. `internal/runtime/executor/claude_executor.go` — 2387
4. `sdk/cliproxy/service.go` — 2362
5. `internal/config/config.go` — 2299
6. `internal/api/server.go` — 2224
7. `internal/runtime/executor/codex_executor.go` — 2055
8. `internal/runtime/executor/codex_websockets_executor.go` — 2014
9. `internal/runtime/executor/github_copilot_executor.go` — 2007
10. `internal/logging/request_logger.go` — 1912
11. `internal/runtime/executor/kiro_executor.go` — 1887
12. `internal/registry/model_registry.go` — 1883
13. `internal/runtime/executor/kiro_eventstream.go` — 1835
14. `internal/runtime/executor/qoder_executor.go` — 1782
15. `sdk/api/handlers/openai/openai_images_handlers.go` — 1773

### internal vs sdk duplication estimate
- **Not line-for-line duplication** of large modules. Pattern is **re-export / type alias facade** (`sdk/config`, `sdk/logging`, `sdk/cliproxy/model_registry`) plus **heavy composition** (`sdk/cliproxy.Service` owns internal server/executors).
- True logic duplication is higher **within** `internal/translator` and `internal/runtime/executor` provider matrices than between internal and sdk.
- Estimated “mirror debt”: **low-moderate for sdk/internal**, **high for provider N×M converters/executors**.

## Top Refactoring Priorities
1. **Eliminate panics on Cursor proto encode/descriptor path** — Effort: S (1–2 days)
2. **Split top executor god files** (antigravity, xai, claude, copilot, codex) into stream/non-stream/helpers — Effort: L (2–3 weeks)
3. **Decompose `kiro_eventstream.streamToChannel` (1113 lines)** into event handlers — Effort: M (3–5 days)
4. **Clarify SDK boundary** (stop growing sdk→internal imports; public interfaces first) — Effort: L (ongoing)
5. **Shared stream/retry framework for executors** — Effort: L
6. **Translator intermediate representation expansion** (beyond thinking) — Effort: XL
7. **English-comment cleanup for kiro/usage packages** — Effort: S–M
8. **Auth + thinking unit test boost; TUI pure-model tests** — Effort: M
9. **OAuth waiter goroutine safety** (context + recover + logrus) — Effort: S
10. **Replace `[]map[string]any` model listings with typed DTOs** — Effort: M

## Checklist Coverage
1. **Dead code & unused exports** — CHECKED - 2 findings (LOW orphan recover.go; LOW single TODO). No large commented-out blocks. Unused export scan not exhaustively proven without staticcheck (build-based tools skipped by charter).
2. **Duplication** — CHECKED - 3 findings (translator matrix, executor patterns, sdk facade vs true mirror).
3. **Complexity** — CHECKED - 3 HIGH findings (god files, conductor package, mega-functions). Metrics recorded.
4. **Error handling** — CHECKED - CRITICAL panic path; MEDIUM defer Close / AMP re-panic / log.Fatal utility; empty `if err != nil {}` count **0**.
5. **Type safety** — CHECKED - MEDIUM signature cache asserts + map[string]any plumbing.
6. **Architecture** — CHECKED - HIGH sdk/internal coupling; MEDIUM globals, management handler density, init plugins.
7. **Testing gaps** — CHECKED - MEDIUM uneven coverage; TUI 0; auth/thinking weak.
8. **Docs & DX** — CHECKED - MEDIUM README asymmetry; partial SDK godoc; config.example strong.
9. **Naming & conventions** — CHECKED - HIGH CJK comments; gofmt Clean; English comment policy violations concentrated.
10. **Correctness risks** — CHECKED - CRITICAL panics; MEDIUM client Timeout / Background ctx / goroutines; registry locking appears sound (`RWMutex` on ModelRegistry; selector mutex; oauth session mutex).

## Files Reviewed
- **Metrics sweep**: all main-tree `*.go` excluding `.git`, `.claude/worktrees`, `CLIProxyAPI/`, `auths/`, `secrets/`, `audit-workspace/`
- **Deep reads / samples**:
  - `internal/registry/model_registry.go`
  - `internal/cache/signature_cache.go`
  - `internal/api/handlers/management/oauth_sessions.go`, `oauth_handlers_claude.go`
  - `internal/api/modules/amp/routes.go`
  - `internal/auth/cursor/proto/encode.go`
  - `internal/runtime/executor/helps/proxy_helpers.go`, `kiro_http.go` (timeout comments)
  - `sdk/cliproxy/service.go`, `sdk/cliproxy/auth/conductor.go`, `sdk/access/registry.go`
  - `sdk/config/config.go`, `sdk/logging/request_logger.go`, `sdk/translator/registry.go`
  - `internal/browser/browser.go`, `internal/auth/kiro/rate_limiter_singleton.go`
  - `README.md`, `README_JA.md`, `config.example.yaml` (headers), `AGENTS.md` conventions
- **Directory summary coverage**: `internal/api`, `internal/runtime/executor`, `internal/translator`, `internal/auth`, `internal/store`, `internal/config`, `internal/watcher`, `internal/registry`, `internal/cache`, `internal/thinking`, `internal/wsrelay`, `internal/usage`, `internal/tui`, `sdk/**`, `cmd/**`, `test/**`

## Methodology Notes
- **Read-only**: no builds, no `go test`, no `go vet` (would compile). Metrics from filesystem scans + CodeGraph status (1042 files indexed).
- **Function length heuristic**: lines from `^func` to next top-level decl; may slightly over-count due to trailing comments/whitespace — still directionally accurate for ranking.
- **Goroutine recover heuristic**: looks only ~25 lines ahead; may misclassify multi-line `go package.Func()` without recover.
- **Ignored `_ =` count** (~3346) is noisy (includes non-error blanks); not treated as a standalone finding.
- **Secrets/auths/config.yaml/.env** not read per charter.
- **Worktrees under `.claude/`** excluded from metrics to avoid multi-counting.
- Assumptions: AGENTS.md conventions are the authoritative project standard; sdk-as-embed-shell is intentional but still a maintainability concern for external consumers.
