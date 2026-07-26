# Overnight Codebase Audit Report

**Repository**: CLIProxyAPIPlus (`github.com/router-for-me/CLIProxyAPI/v7`)  
**Generated**: 2026-07-26  
**Audit Duration**: Exact elapsed time was not recorded; all five domain reports completed on 2026-07-26  
**Auditor**: Overnight Repo Auditor (Claude Code Skill)  
**Method**: Parallel specialist static audits followed by sequential deduplication and compilation

---

## Executive Summary

### Overall Health: D — Significant remediation is required before broad internet exposure or high-volume production use

The audit identified **104 unique findings** after consolidating 106 raw findings and removing two cross-domain duplicates. Six findings are rated Critical. The most urgent risks are concentrated in public authentication/OAuth routes, SSRF-capable URL construction, process-wide panic paths, repeated TLS handshakes on Claude traffic, and the accessibility failure mode of the management UI when JavaScript does not execute.

The codebase also has strong foundations: secret-bearing runtime files are ignored, management authentication uses bcrypt and constant-time comparison, several hot caches are already bounded, dependency integrity verification passes, formatting is clean, and many management UI components already implement useful ARIA patterns. The primary problem is not uniformly poor engineering; it is a set of high-impact gaps concentrated in complex, security-sensitive paths.

### Finding Counts After Deduplication

| Category | Critical | High | Medium | Low | Total |
|----------|---------:|-----:|-------:|----:|------:|
| Security | 3 | 7 | 8 | 5 | 23 |
| Performance | 1 | 6 | 8 | 2 | 17 |
| Accessibility | 1 | 8 | 10 | 5 | 24 |
| Dependencies | 0 | 3 | 9 | 4 | 16 |
| Code Quality | 1 | 6 | 11 | 6 | 24 |
| **Total** | **6** | **30** | **46** | **22** | **104** |

### Deduplication Summary

| Canonical finding | Duplicate source | Resolution |
|---|---|---|
| `go-git v6.0.0-alpha.4` in the production Git store path | Security MEDIUM + Dependency HIGH | Kept the more detailed Dependency finding at HIGH; removed the Security duplicate from aggregate counts. |
| CGO/SQLite increases build, portability, and runtime surface | Performance LOW + Dependency MEDIUM | Kept the broader Dependency finding at MEDIUM; removed the Performance duplicate from aggregate counts. |

The following superficially related findings were **not** merged because they describe different failure mechanisms or remediation:

- Claude uTLS client connection reuse (Performance) vs. uTLS supply-chain/ToS posture (Dependencies).
- O(n²) response aggregation (Performance) vs. sensitive request-body persistence (Security).
- Signature-cache capacity and lock contention (Performance) vs. unchecked cache type assertions (Code Quality).
- Management key storage in browser storage (Security) vs. management-panel WCAG failures (Accessibility).

### Top 10 Priority Items

1. **[CRITICAL][Security] Protect Kiro OAuth import/refresh/start routes** — unauthenticated callers can mutate the credential store and trigger outbound OAuth traffic.
2. **[CRITICAL][Security] Default-deny when no API keys are configured** — the current legacy behavior exposes the provider-backed proxy without authentication.
3. **[CRITICAL][Security] Validate Kiro OIDC region values** — untrusted region interpolation permits attacker-controlled host construction and SSRF.
4. **[CRITICAL][Code Quality] Remove Cursor proto panic paths** — marshal or descriptor failures can crash the entire proxy rather than fail one request.
5. **[CRITICAL][Performance] Reuse Claude uTLS transports and HTTP/2 connections** — every request currently pays a new TCP/TLS/HTTP2 setup cost.
6. **[CRITICAL][Accessibility] Add a non-JavaScript fallback to the management UI** — the page is otherwise completely blank when the bundle cannot execute.
7. **[HIGH][Security] Stop `MANAGEMENT_PASSWORD` from enabling remote management** — authentication configuration must not silently override network exposure policy.
8. **[HIGH][Security] Restrict management `APICall` destinations and token substitution** — the endpoint is an authenticated open HTTP proxy with credential-injection capability.
9. **[HIGH][Security] Reduce management secret-export blast radius** — config YAML, API keys, and auth files are returned in full to a single management credential.
10. **[HIGH][Security] Enforce WebSocket authentication and origin validation by default** — permissive origins combine dangerously with optional authentication.

### Highest-Risk Areas

1. **Authentication and management API** — `internal/auth/kiro`, `internal/api/server.go`, and `internal/api/handlers/management` contain the most consequential access-control, SSRF, credential, and lifecycle findings.
2. **Provider execution and translation hot paths** — `internal/runtime/executor`, `internal/translator`, and `sdk/api/handlers` combine very large functions with streaming backpressure, repeated allocation, and cache-lifecycle risks.
3. **Management and OAuth user interfaces** — the generated management SPA and embedded OAuth HTML have contrast, focus, language, status-announcement, timing, and fallback deficiencies.
4. **Dependency/build surface** — alpha `go-git`, uTLS, CGO SQLite, optional backends compiled by default, and ambiguous AGPL/MIT messaging require operational decisions.
5. **Architecture and testability** — multi-thousand-line files, the `sdk`↔`internal` coupling, global mutable state, and weak auth/thinking/TUI coverage increase regression risk.

### Positive Observations

- `.env`, `config.yaml`, auth files, runtime data, and generated management assets are ignored and not tracked; reviewed CI files did not contain hardcoded PATs.
- Management authentication uses bcrypt for stored secrets, constant-time comparison for the environment secret, `RemoteAddr` for loopback checks, and failed-attempt lockout.
- Management CORS defaults to deny, and management auth-file/log download paths include separator and path checks.
- The Amp proxy strips client credentials before injecting upstream credentials; logging helpers mask sensitive headers and query parameters.
- Several providers use cryptographically random OAuth state and PKCE correctly.
- Hot-path engineering is not uniformly weak: Kiro transport pooling, tokenizer caching, model-list caching, bounded reasoning replay caches, and watcher-driven auth updates are good controls.
- `go.sum` is committed, `go mod verify` passes, there are no `replace`/`exclude` surprises, and all 35 direct dependencies are imported.
- `gofmt` adherence is excellent, TODO/FIXME debt is very low, and executor/SDK areas contain substantial companion test code.
- The management SPA already uses semantic regions, labels, `aria-invalid`, `aria-describedby`, dialog roles, pagination labels, and some live regions.

### Recommended Action Plan

#### Immediate — This Week

1. Put management authentication or strict localhost-only enforcement around all credential-mutating Kiro OAuth routes; review equivalent CodeArts/JoyCode start routes.
2. Enforce default-deny public API authentication when no providers/API keys are configured; require an explicit opt-in for unauthenticated mode.
3. Validate Kiro AWS regions with a strict allowlist or `^[a-z0-9-]+$` before constructing any URL.
4. Convert Cursor proto marshal/descriptor helpers from panic-based APIs to error returns and propagate errors to request boundaries.
5. Cache/reuse Claude uTLS clients and HTTP/2 connections by normalized proxy/fingerprint key.
6. Add a visible `<noscript>` explanation and CLI/API fallback instructions to the management panel source.
7. Decouple `MANAGEMENT_PASSWORD` from `allow-remote`; preserve the explicit network policy.
8. Add an HTTPS/provider-host allowlist and private/link-local/metadata IP denial to management `APICall`; prohibit `$TOKEN$` substitution for untrusted hosts.
9. Install `govulncheck` in CI and run `govulncheck ./...`; the dependency audit could not authoritatively enumerate CVEs.
10. Clarify whether the repository is AGPL-only or explicitly dual-licensed.

#### This Sprint

1. Enforce same-origin/allowlisted WebSocket upgrades and make WebSocket authentication default-on in code.
2. Redact management config responses and require elevated capability for raw YAML/auth-file export.
3. Replace timestamp OAuth state values with `misc.GenerateRandomState()` everywhere.
4. Stop rebuilding complete stream responses per chunk; use one bounded buffer and finalize once.
5. Add small bounded buffers to stream channels and reduce 50 MiB scanner token limits to realistic caps.
6. Bound signature, session-affinity, WebSocket tool-output, and proxy-client caches by entries and/or bytes.
7. Add `lang`, `role="status"`, `aria-live`, visible focus indicators, and adjustable/no auto-close behavior to OAuth pages.
8. Correct management/OAuth foreground-background color pairs to WCAG 2.1 AA contrast.
9. Upgrade the `golang.org/x/*` family and `klauspost/compress`; complete routine patch bumps with tests in the implementation change.
10. Mark the Git-backed store experimental or define a migration path away from `go-git/v6` alpha.

#### This Quarter

1. Split the largest executor, conductor, server, service, and config files; convert giant stream functions to explicit state machines/event handlers.
2. Define a stable public DTO/interface boundary so `sdk` no longer grows direct dependencies on `internal` concrete types.
3. Expand the canonical representation approach used by `internal/thinking` to shared message/tool normalization across translators.
4. Add focused tests for auth refresh/OAuth flows, `internal/thinking`, and pure TUI model transitions.
5. Separate optional TUI/storage backends with commands or build tags where operationally worthwhile.
6. Pin and accessibility-test the management-panel release in its source repository; provide chart tables/text summaries.
7. Run load/heap profiles for long SSE/WebSocket streams after allocation and channel fixes.
8. Add non-root container execution and reduce default published callback ports.

#### Backlog

- Standardize security headers and CSP, route-specific page titles, reduced-motion behavior, and decorative icon semantics.
- Hoist per-call regex compilation and evaluate buffer pools only after larger allocation issues are fixed.
- Move the root `recover.go` utility under `cmd/` and remove library-like `log.Fatal` usage.
- Translate non-English code comments in dedicated cleanup work while preserving localized user-visible strings.
- Evaluate `logrus`→`slog`, replacing `pkg/browser`, and slim dependency variants only if maintenance value justifies migration cost.

---

## Detailed Findings by Category

The tables below are the canonical, deduplicated finding register. Full evidence, code excerpts, checklists, and methodology remain in the five source reports linked in [Audit Files](#audit-files).

### Security Audit — 23 Unique Findings

| ID | Severity | Finding | Primary location | Recommended action |
|---|---|---|---|---|
| SEC-01 | CRITICAL | Unauthenticated Kiro OAuth routes can import tokens and mutate the auth store | `internal/auth/kiro/oauth_web.go`; `internal/api/server.go` | Require management auth or localhost-only access for import, refresh, and credential-writing starts. |
| SEC-02 | CRITICAL | Empty API-key/provider list disables authentication | `sdk/access/manager.go`; `internal/api/server.go`; `internal/access/config_access/provider.go` | Default-deny; require explicit `allow-unauthenticated` opt-in. |
| SEC-03 | CRITICAL | Kiro OIDC `region` permits SSRF host breakout | `internal/auth/kiro/sso_oidc.go`; `internal/auth/kiro/oauth_web.go` | Strictly validate/allowlist AWS regions before URL construction. |
| SEC-04 | HIGH | `MANAGEMENT_PASSWORD` forces remote management enabled | `internal/api/handlers/management/handler.go` | Decouple secret configuration from `allow-remote`. |
| SEC-05 | HIGH | Management `APICall` is an open HTTP proxy with token injection | `internal/api/handlers/management/api_tools.go` | Restrict scheme/hosts/IP ranges and token-substitution destinations. |
| SEC-06 | HIGH | Third-party OAuth client secrets are hardcoded and duplicated | Gemini, Antigravity, and iFlow auth/executor files | Centralize, externalize where possible, and rotate provider-side credentials. |
| SEC-07 | HIGH | WebSocket origins are unrestricted and authentication is optional | `internal/wsrelay/manager.go`; OpenAI responses WS handler; `internal/api/server.go` | Enforce origin policy and default authentication on. |
| SEC-08 | HIGH | Management endpoints export raw config, API keys, and auth files | Management config/list/auth-file handlers | Redact by default and require elevated export scope. |
| SEC-09 | HIGH | CodeArts/JoyCode OAuth start routes and root callback are public | CodeArts/JoyCode OAuth handlers; `internal/api/server.go` | Gate start routes; use dedicated callback listeners/routes. |
| SEC-10 | HIGH | API keys are accepted in URL query parameters | Config access provider; conductor selection | Deprecate query auth and prefer headers; warn or reject on non-TLS. |
| SEC-11 | MEDIUM | Several OAuth states use predictable UnixNano values | Management OAuth handlers | Use CSPRNG state generation consistently. |
| SEC-12 | MEDIUM | Container runs as root and compose publishes many callback ports | `Dockerfile`; `docker-compose.yml` | Use a non-root user and bind/publish only required ports. |
| SEC-13 | MEDIUM | `CPA_TOKEN` is embedded through Docker build args and ldflags | `Dockerfile`; `cmd/server/main.go` | Supply at runtime; never embed secrets in image history or binaries. |
| SEC-14 | MEDIUM | Optional TLS certificate verification bypass | Usage keeper/home clients and config | Keep false by default, warn loudly, and reject unsafe production combinations. |
| SEC-15 | MEDIUM | Public API CORS is fully permissive | `internal/api/server.go` | Make origins configurable and default-deny for browser access. |
| SEC-16 | MEDIUM | Request logging can persist sensitive JSON bodies | Request logging middleware/logger | Redact sensitive JSON fields, cap capture, and minimize retention. |
| SEC-17 | MEDIUM | Postgres store delete-path resolver accepts absolute/separator paths | `internal/store/postgresstore.go` | Jail all resolved paths under `authDir`. |
| SEC-18 | MEDIUM | Management key is stored in browser storage | `static/management.html` | Prefer memory-only or HttpOnly short-lived management sessions and strong CSP. |
| SEC-19 | LOW | Global browser security headers are missing | API middleware stack | Add CSP, frame protections, Referrer-Policy, and HSTS where TLS applies. |
| SEC-20 | LOW | MD5 is used for Qoder protocol signing | `internal/runtime/executor/qoder_executor.go` | Isolate and document as vendor compatibility; never reuse locally. |
| SEC-21 | LOW | Empty default host binds all interfaces | `config.example.yaml`; listen path | Use loopback-safe development defaults. |
| SEC-22 | LOW | pprof may be exposed beyond loopback by configuration | `sdk/cliproxy/pprof_server.go` | Enforce loopback unless explicit remote opt-in is present. |
| SEC-23 | LOW | Public API/OAuth starts have no application-level rate limiting | Public `/v1` and `/v0/oauth/*` routes | Add per-IP/per-key controls or documented reverse-proxy limits. |

**Deduplication note:** The Security report's MEDIUM `go-git v6 alpha` item is represented once as DEP-01 at HIGH severity.

### Performance Audit — 17 Unique Findings

| ID | Severity | Finding | Primary location | Recommended action |
|---|---|---|---|---|
| PERF-01 | CRITICAL | Claude uTLS clients recreate TCP/TLS/HTTP2 connections per request | `internal/runtime/executor/helps/utls_client.go`; Claude executor | Cache clients/transports by proxy and fingerprint; prune dead connections. |
| PERF-02 | HIGH | Unbuffered `StreamChunk` channels synchronize every producer/consumer chunk | Multiple executors, conductor, and wsrelay | Use a consistent small bounded buffer while retaining hard backpressure. |
| PERF-03 | HIGH | Request-log aggregation rebuilds the full response on every SSE chunk | `internal/runtime/executor/helps/logging_helpers.go` | Aggregate lazily once and cap logged body size. |
| PERF-04 | HIGH | Client-facing `API_RESPONSE` capture repeatedly copies the full body | `sdk/api/handlers/handlers.go` | Store one buffer/builder and finalize once with a size cap. |
| PERF-05 | HIGH | Stream translators repeatedly rewrite JSON with `sjson.SetBytes` | `internal/translator/**` response converters | Use pre-sized fixed event builders for high-frequency delta shapes. |
| PERF-06 | HIGH | SSE scanner maximum token size is 50 MiB | Multiple runtime executors | Lower the hard maximum and handle exceptional large events explicitly. |
| PERF-07 | HIGH | Signature cache is unbounded and takes write locks on hits | `internal/cache/signature_cache.go` | Add entry/byte limits and reduce sliding-TTL write contention. |
| PERF-08 | MEDIUM | Session-affinity cache has TTL but no entry cap | `sdk/cliproxy/auth/session_cache.go` | Add bounded eviction and avoid full writes on every hit. |
| PERF-09 | MEDIUM | WebSocket tool-output cache lacks a global session cap | OpenAI responses WebSocket repair cache | Bound sessions and add lifecycle cleanup/sharding. |
| PERF-10 | MEDIUM | Kiro stream usage re-tokenizes the full accumulated output | `internal/runtime/executor/kiro_eventstream.go` | Count deltas incrementally or estimate until final exact count. |
| PERF-11 | MEDIUM | Amp reverse proxy fully buffers gzip responses | `internal/api/modules/amp/proxy.go` | Stream decompression or apply strict read limits. |
| PERF-12 | MEDIUM | Postgres/usage pools are fixed at very small sizes | Store and usage repositories | Make pool sizing configurable and separate request/background workloads. |
| PERF-13 | MEDIUM | Proxy HTTP client cache has no eviction | `internal/runtime/executor/helps/proxy_helpers.go` | Cap entries and close idle transports on eviction. |
| PERF-14 | MEDIUM | Regexes are compiled per call on error/cloak paths | Gemini CLI, JSON retry, logging, cloak helpers | Hoist regexes to package-level variables. |
| PERF-15 | MEDIUM | `GetModelInfo` deep-clones metadata on every lookup | `internal/registry/model_registry.go` | Use immutable/copy-on-write views or invalidated clone caches. |
| PERF-16 | LOW | wsrelay buffers may stall bursty streams | `internal/wsrelay/session.go`; `internal/wsrelay/http.go` | Modestly tune/configure buffers without dropping messages. |
| PERF-17 | LOW | Hot stream paths rarely reuse scratch byte buffers | Executors, translators, SDK handlers | Consider carefully scoped pools after larger allocation fixes. |

**Deduplication note:** The Performance report's LOW Docker/CGO item is represented once as DEP-07 at MEDIUM severity.

### Accessibility Audit — 24 Unique Findings

| ID | Severity | Finding | Primary location | Recommended action |
|---|---|---|---|---|
| A11Y-01 | CRITICAL | Management UI is blank without JavaScript | `static/management.html` | Add visible `<noscript>` guidance and critical fallback text outside the SPA root. |
| A11Y-02 | HIGH | Tertiary/quaternary text tokens fail AA contrast | Management SPA theme CSS | Darken normal-text tokens to at least 4.5:1. |
| A11Y-03 | HIGH | Primary button/link color pairs fail text contrast | Management SPA and OAuth templates | Adjust fill/foreground pairs and verify both themes. |
| A11Y-04 | HIGH | OAuth polling status updates lack live-region semantics | CodeArts/JoyCode/Kiro waiting pages | Add `role="status"`, `aria-live`, and assertive error handling. |
| A11Y-05 | HIGH | Most embedded OAuth/callback pages omit document language | Shared callbacks and provider templates | Add the appropriate `lang` attribute to every HTML document. |
| A11Y-06 | HIGH | OAuth windows auto-close without adjustable timing | Shared/provider success pages | Disable auto-close or provide visible cancel/extend controls. |
| A11Y-07 | HIGH | Management SPA lacks a skip-to-main link | Management SPA chrome | Add a first-focus skip link and route-change focus management. |
| A11Y-08 | HIGH | `outline:none` is widespread with incomplete replacement focus styles | Management SPA and Kiro templates | Provide ≥3:1 `:focus-visible` indicators on all controls. |
| A11Y-09 | HIGH | Canvas charts lack discoverable text alternatives | Management usage analytics | Add data tables/summaries and accessible names. |
| A11Y-10 | MEDIUM | Global translation suppression blocks user-agent translation | `static/management.html` root/meta | Remove global `notranslate` or scope it to code snippets. |
| A11Y-11 | MEDIUM | Initial `lang` conflicts with English title/mixed default UI | Management shell | Align initial locale, title, and SPA i18n state. |
| A11Y-12 | MEDIUM | Status feedback relies heavily on emoji and color | Embedded OAuth templates | Put plain text first and hide decorative symbols from assistive tech. |
| A11Y-13 | MEDIUM | New-tab links lack disclosure and `rel` protection | OAuth waiting/success templates | Add “opens in a new tab” and `rel="noopener noreferrer"`. |
| A11Y-14 | MEDIUM | Reduced-motion support is incomplete | Management SPA and OAuth CSS | Add comprehensive reduced-motion overrides. |
| A11Y-15 | MEDIUM | Auto-updated remote panel may differ from the audited asset | `internal/managementasset/updater.go` | Pin versions/digests and test the panel source in CI. |
| A11Y-16 | MEDIUM | Usage Keeper UI assets were unavailable for audit | Usage Keeper injected `staticFS` | Include or independently audit the deployed frontend artifact. |
| A11Y-17 | MEDIUM | Placeholder-heavy forms may lack persistent labels | Management SPA forms | Enforce a shared labeled `FormField` component. |
| A11Y-18 | MEDIUM | Dialog focus trapping/restoration cannot be verified | Minified management SPA | Use a tested accessible dialog primitive and runtime tests. |
| A11Y-19 | MEDIUM | OAuth countdown/helper text has insufficient contrast | Claude/Codex/Kiro templates | Darken meaningful muted text to AA-compliant values. |
| A11Y-20 | LOW | Minimal callback pages lack landmarks and viewport metadata | Shared/Kiro callback HTML | Standardize an accessible callback page skeleton. |
| A11Y-21 | LOW | Decorative success icons are announced | Claude/Codex/Kiro templates | Add `aria-hidden="true"` to decorative glyphs. |
| A11Y-22 | LOW | Empty image alt text requires adjacent visible labels to remain safe | Management provider cards | Preserve empty alt only for truly decorative images. |
| A11Y-23 | LOW | Close buttons omit explicit `type="button"` | Claude/Codex templates | Set explicit non-submit button type. |
| A11Y-24 | LOW | SPA page title likely remains static across routes | Management SPA | Update `document.title` per route and locale. |

### Dependency Audit — 16 Unique Findings

| ID | Severity | Finding | Primary location | Recommended action |
|---|---|---|---|---|
| DEP-01 | HIGH | `go-git/v6` alpha is used in the production Git-store path | `go.mod`; `internal/store/gitstore.go` | Prefer stable v5/v6, or mark and test the backend as experimental. |
| DEP-02 | HIGH | uTLS Chrome fingerprinting is on live auth/API paths | `go.mod`; Claude auth and executor transports | Isolate behind configuration, track releases, test handshakes, document ToS posture. |
| DEP-03 | HIGH | AGPL-3.0 primary license conflicts with unexplained `LICENSE-MIT` | `LICENSE`; `LICENSE-MIT`; READMEs | Publish a clear AGPL-only or explicit dual-license policy. |
| DEP-04 | MEDIUM | Security-relevant `golang.org/x/*` modules lag current proxy versions | `go.mod` | Batch-upgrade and test on a regular cadence. |
| DEP-05 | MEDIUM | `klauspost/compress` and `quic-go` lag releases | Direct/transitive module graph | Upgrade compatible versions and monitor release security notes. |
| DEP-06 | MEDIUM | Gin pulls HTTP/3 `quic-go` and Mongo BSON into the build | Gin transitive graph | Track optionalization/build tags and dependency-surface changes. |
| DEP-07 | MEDIUM | CGO SQLite expands portability and native-code surface | `go.mod`; usage backup/recovery paths; Docker build | Document/optionalize CGO or assess a pure-Go SQLite backend. |
| DEP-08 | MEDIUM | `pkg/browser` is pinned to a stagnant pseudo-version | `go.mod`; `internal/browser/browser.go` | Confirm maintenance or replace with a small controlled helper. |
| DEP-09 | MEDIUM | TUI dependencies are always linked into the server binary | `cmd/server/main.go`; TUI modules | Split commands or use a build tag for headless builds. |
| DEP-10 | MEDIUM | Niche tokenizer/hash libraries need focused monitoring | Tokenizer and xxHash modules | Patch tokenizer and maintain golden correctness tests. |
| DEP-11 | MEDIUM | Optional MinIO/GORM/Redis backends are compiled by default | Store/home/usage dependencies | Offer slim build variants where deployment value warrants it. |
| DEP-12 | MEDIUM | Fork/module/multi-remote provenance is ambiguous | Git remotes; module path; release metadata | Document canonical build provenance and SBOM identity. |
| DEP-13 | LOW | Routine direct/transitive patch updates are available | `go.mod`/`go.sum` | Include in a tested maintenance bump. |
| DEP-14 | LOW | logrus has long-term ecosystem maintenance cost | `go.mod`; logging packages | Evaluate `slog` only as a planned migration. |
| DEP-15 | LOW | `atotto/clipboard` is mature but stagnant | TUI dependency | Accept or replace if TUI is split. |
| DEP-16 | LOW | `go list -m all` is inflated by environment/tooling modules | Audit/SBOM procedure | Build SBOMs from production package reachability and `go.sum`. |

**Coverage caveat:** `govulncheck` was not installed, so no authoritative Go vulnerability database scan was completed. `go mod verify` passed, no replace/exclude/retract directives were present, and all direct dependencies were reachable from imports.

### Code Quality Audit — 24 Unique Findings

| ID | Severity | Finding | Primary location | Recommended action |
|---|---|---|---|---|
| CQ-01 | CRITICAL | Cursor proto encode/descriptor failures panic | `internal/auth/cursor/proto/encode.go`; `descriptor.go` | Return errors and handle startup/request failures without process panic. |
| CQ-02 | HIGH | Nine hot-path source files exceed 2,000 lines | Executors, service, config, API server | Split by responsibility while preserving package boundaries. |
| CQ-03 | HIGH | Auth conductor remains a ~5K LOC god package | `sdk/cliproxy/auth/conductor_*.go` | Extract selection, retry/cooldown, refresh, and result policies. |
| CQ-04 | HIGH | Hundreds of functions are extremely long | Kiro stream parser, `main`, translators, executors | Decompose into state machines and testable event/stage handlers. |
| CQ-05 | HIGH | Public SDK extensively imports internal concrete packages | `sdk/cliproxy/service.go`; SDK aliases | Move stable DTOs/interfaces to the public side. |
| CQ-06 | HIGH | Translator matrix duplicates N×M protocol logic | `internal/translator/**` | Expand canonical intermediate representations and golden fixtures. |
| CQ-07 | HIGH | Hundreds of non-English comments violate project convention | Kiro/auth/usage and related files | Translate comments in a dedicated cleanup; preserve localized UI strings. |
| CQ-08 | MEDIUM | Tests are uneven: TUI has none; auth/thinking are thin | `internal/tui`, `internal/auth`, `internal/thinking` | Prioritize state, refresh, rate-limit, and conversion tests. |
| CQ-09 | MEDIUM | Process-wide mutable singletons complicate lifecycle/isolation | Registry, caches, OAuth sessions, browser, Kiro limiters | Prefer service-owned injected instances. |
| CQ-10 | MEDIUM | Signature cache uses unchecked type assertions and APIs use loose maps | `internal/cache/signature_cache.go`; model listings | Add typed wrappers and DTOs with checked assertions. |
| CQ-11 | MEDIUM | Deferred close errors are handled inconsistently | Repo-wide HTTP/auth paths | Standardize wrapped close handling in new and touched code. |
| CQ-12 | MEDIUM | Management OAuth handlers detach work with background contexts/goroutines | `oauth_handlers_*.go` | Use bounded request-derived contexts, recovery, and structured logs. |
| CQ-13 | MEDIUM | AMP recovery re-panics non-error values | `internal/api/modules/amp/routes.go` | Convert every recovered value to a controlled response. |
| CQ-14 | MEDIUM | Optional `http.Client.Timeout` can terminate full streams | `internal/runtime/executor/helps/proxy_helpers.go` | Remove/limit the option to credential acquisition or dial/handshake phases. |
| CQ-15 | MEDIUM | Management handlers contain dense domain/business logic | Management config/API/auth files handlers | Extract domain services and keep HTTP binding thin. |
| CQ-16 | MEDIUM | README language coverage and SDK Godoc are uneven | READMEs; exported SDK symbols | Port operational docs and enforce exported-symbol documentation. |
| CQ-17 | MEDIUM | Executors duplicate stream, retry, and error-mapping frameworks | `internal/runtime/executor/*_executor.go` | Introduce shared stream/retry interfaces with provider hooks. |
| CQ-18 | MEDIUM | Root utility uses `log.Fatal` outside a command package | `recover.go` | Move under `cmd/` and return errors from reusable logic. |
| CQ-19 | LOW | One actionable TODO remains | CodeBuddy auth | Resolve when upstream denial codes are documented. |
| CQ-20 | LOW | Formatting is clean and should remain automated | Entire Go tree | Keep `gofmt`/goimports checks in CI. |
| CQ-21 | LOW | Fire-and-forget goroutines often lack local panic recovery | Background work across packages | Use a standard recovered/logged launcher for detached tasks. |
| CQ-22 | LOW | Examples use panic for routine error flow | `examples/**/main.go` | Prefer checked errors or main-only fatal logging. |
| CQ-23 | LOW | Heavy `init()` registration obscures dependencies | Translators, thinking providers, updaters | Document side effects or provide explicit test registration. |
| CQ-24 | LOW | Root `recover.go` is an orphaned operational script | `/recover.go` | Move to `cmd/recover-usage-db` or `scripts/`. |

---

## Repository Overview

| Attribute | Value |
|---|---|
| Primary language | Go 1.26 |
| Estimated Go LOC | ~299,600 |
| Estimated repository files | ~8,144 |
| Indexed source scale | ~1,042 files in the CodeGraph snapshot |
| HTTP framework | Gin v1.12.0 |
| WebSockets | gorilla/websocket plus provider-specific executors and wsrelay |
| Storage | File default; optional PostgreSQL, Git, object storage, Redis/usage storage |
| Frontend | Generated `static/management.html` React/Vite SPA plus embedded OAuth HTML |
| Dependency manager | Go modules (`go.mod`, `go.sum`) |
| Main security assets | Provider OAuth tokens, API keys, management credentials, auth files |

### Major Source Areas

- `cmd/server/` — process entry point and CLI/TUI startup.
- `internal/api/` — Gin server, route wiring, middleware, management API, Amp module.
- `internal/auth/` — provider OAuth/token acquisition and callback HTML.
- `internal/runtime/executor/` — provider request, SSE, and WebSocket execution paths.
- `internal/translator/` — protocol conversion matrix.
- `internal/thinking/` — canonical thinking/reasoning normalization and provider application.
- `internal/store/` — file/Postgres/Git/object-store implementations and secret resolution.
- `internal/registry/`, `internal/cache/`, `internal/watcher/`, `internal/wsrelay/`, `internal/usage/` — model, cache, lifecycle, relay, and accounting infrastructure.
- `sdk/` — embeddable service, public handlers/config aliases, auth conductor, and SDK-facing APIs.
- `static/management.html` — generated management panel artifact; production may update it remotely.

### Audit Scope

All five modules were active:

- Security
- Performance
- Accessibility (management SPA and embedded OAuth HTML)
- Dependencies (`go.mod`, `go.sum`, licenses, reachable module graph)
- Code Quality

Live `.env`, `config.yaml`, auth files, and runtime credential data were intentionally not read or quoted.

---

## Audit Metadata

### Configuration

- Specialist reports compiled: **5**
- Raw structured findings: **106**
- Cross-report duplicates removed: **2**
- Canonical unique findings: **104**
- WCAG target: **2.1 Level AA**
- Severity rubric: **Critical / High / Medium / Low**
- Analysis mode: static source and dependency analysis

### Methodology

Specialized audits independently reviewed the codebase for security, performance, accessibility, dependency, and maintainability concerns. The compilation pass then compared titles, code locations, root causes, impact, and remediation. A candidate was merged only when both the underlying defect and intended remediation substantially matched. The more detailed source finding and the higher severity were retained.

### Limitations

- This is primarily static analysis; no authenticated penetration test, exploit execution, load test, browser automation, screen-reader test, or production profiling was performed.
- `govulncheck` was unavailable, so the dependency report does not authoritatively enumerate current Go vulnerability database entries.
- The management SPA is minified and may be replaced at runtime by the remote asset updater; findings apply to the audited snapshot unless separately verified against the deployed version.
- The Usage Keeper frontend artifact was not present in this repository and could not be assessed.
- Runtime behavior that depends on specific credentials, environment variables, backend configuration, or network topology may differ from static conclusions.
- Severity reflects the shared audit rubric; operational exposure can increase or decrease practical risk.
- This report does not replace professional penetration testing, production load testing, legal license advice, or accessibility testing with assistive-technology users.

## Audit Files

- Reconnaissance: [`00-reconnaissance.md`](00-reconnaissance.md)
- Security evidence: [`01-security-audit.md`](01-security-audit.md)
- Performance evidence: [`02-performance-audit.md`](02-performance-audit.md)
- Accessibility evidence: [`03-accessibility-audit.md`](03-accessibility-audit.md)
- Dependency evidence: [`04-dependency-audit.md`](04-dependency-audit.md)
- Code quality evidence: [`05-code-quality-audit.md`](05-code-quality-audit.md)
- Compiled report: [`overnight-audit-report.md`](overnight-audit-report.md)

---

*End of compiled overnight audit report.*
