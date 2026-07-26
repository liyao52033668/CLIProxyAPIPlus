# Dependency Audit Report

Generated: 2026-07-26  
Auditor: Dependency Agent (Overnight Repo Auditor)  
Repository: `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus`  
Module: `github.com/router-for-me/CLIProxyAPI/v7`  
Go toolchain observed: `go1.26.5 linux/amd64` (`go 1.26` in go.mod)

---

## Executive Summary

| Metric | Count / Status |
|---|---|
| Direct dependencies (no `// indirect`) | **35** |
| Production modules (`go list -deps ./...`, non-stdlib) | **96** |
| go.sum unique module paths | **~131** (282 hash lines) |
| `go mod graph` edges (raw) | **797** (environment-inflated; see Methodology) |
| go.sum-filtered graph edges | **~533** |
| `go mod verify` | **PASS** — all modules verified |
| `govulncheck` | **NOT RUN** — binary not installed; no network install permitted |
| `replace` / `exclude` / `retract` | **None** |
| Declared-but-unimported direct deps | **0** (all 35 direct deps have import reachability) |

**Vulnerabilities (manual, knowledge-limited):** 0 CRITICAL confirmed with known exploit on used path; 0 HIGH CVE IDs pinned with confidence (govulncheck absent). Several **HIGH supply-chain / stability** issues (alpha `go-git`, TLS-fingerprint stack). **~11** direct/security-relevant packages have newer versions on proxy. **License:** project ships **AGPL-3.0** (primary, per README) plus a separate **LICENSE-MIT** file — dual-license messaging is inconsistent and material for downstream distributors.

**Assessment:** Dependency hygiene is generally solid (`go.sum` committed, verify clean, no replace hijacks, direct deps all used, versions relatively current for mid-2026). The highest practical risks are (1) **production use of `go-git` v6 alpha**, (2) **deliberate uTLS Chrome fingerprinting** on auth/executor paths, (3) **AGPL network-copyleft + ambiguous MIT file**, and (4) **Gin-pulled quic-go + mongo bson** expanding attack surface and binary weight. Re-run with `govulncheck` when install is authorized.

---

## Critical Findings

_None confirmed._ No CVE with a known public exploit was verified against a reachable code path. `govulncheck` was unavailable; absence of CRITICAL here is **not** a clean bill of health.

---

## High Findings

### [HIGH] go-git v6 alpha used in production store path
- **File**: `go.mod` (line 14); consumer `internal/store/gitstore.go`
- **Category**: Supply Chain / Stability
- **Description**: Direct dependency `github.com/go-git/go-git/v6 v6.0.0-alpha.4` is an **alpha** pre-release. Transitive `github.com/go-git/go-billy/v6 v6.0.0-alpha.1` is also alpha. Used for the optional Git-backed config/auth storage backend.
- **Evidence**:
  - `go.mod`: `github.com/go-git/go-git/v6 v6.0.0-alpha.4`
  - Imports in `internal/store/gitstore.go`: plumbing, object, transport/http, client
  - `go list -m -u` shows **no non-alpha newer tag** advertised at audit time (still on alpha line)
- **Impact**: API/behavior instability; higher defect rate typical of alpha VCS libraries (path traversal, symlink, transport auth edge cases historically appear in git implementations). If `GITSTORE_*` is enabled in production, this sits on a credential/config persistence path.
- **Recommendation**:
  1. Prefer stable `go-git/v5` **or** wait for v6 stable before recommending GITSTORE in production docs.
  2. If alpha must stay: pin closely, add integration tests for auth, LFS-less clone, and malicious ref/file names; document “experimental” clearly.
  3. Consider subprocess `git` CLI with allowlisted args as an alternative for lower library risk (trade-off: host git dependency).
- **References**: go-git v6 release channel (alpha); package path `/v6` + version `v6.0.0-alpha.4`

### [HIGH] refraction-networking/utls used to spoof Chrome TLS fingerprint on live auth/API paths
- **File**: `go.mod` (line 22); `internal/auth/claude/utls_transport.go`; `internal/runtime/executor/helps/utls_client.go`
- **Category**: Supply Chain / Dual-use / Security-relevant networking
- **Description**: `github.com/refraction-networking/utls v1.8.2` is wired to `tls.UClient(..., tls.HelloChrome_Auto)` to bypass Cloudflare TLS fingerprinting on Anthropic domains. This is intentional product behavior, not accidental.
- **Evidence**:
  - Comment in source: “bypass TLS fingerprinting” / “bypass Cloudflare's TLS fingerprinting on Anthropic domains”
  - `HelloChrome_Auto` in both auth and executor helper clients
  - Imported from auth (`internal/auth/claude`) and runtime executor helps — **reachable on default Claude/Anthropic traffic**, not dead code
- **Impact**:
  - Niche, research-oriented TLS stack (smaller reviewer base than `crypto/tls`)
  - Fingerprint profiles can break or mis-handshake when Chrome/CF changes; failure modes affect availability of upstream AI providers
  - Policy/ToS risk: deliberate client impersonation may violate upstream terms; security reviewers and some hosts flag uTLS as evasion tooling
  - Any future uTLS CVE would land directly on credential and prompt traffic
- **Recommendation**:
  1. Isolate uTLS behind explicit config flag; default to `crypto/tls` where possible.
  2. Track uTLS releases aggressively; add handshake/regression tests against Anthropic endpoints in CI (opt-in secrets).
  3. Document ToS/compliance posture for operators.
  4. Prefer minimal fork surface — do not vendor-modify ClientHello beyond library APIs.
- **References**: module `github.com/refraction-networking/utls@v1.8.2`; call sites above

### [HIGH] Project license posture: AGPL-3.0 primary + co-shipped LICENSE-MIT (messaging conflict)
- **File**: `LICENSE` (AGPLv3, ~34KB); `LICENSE-MIT`; `README.md` / `README_CN.md` License sections
- **Category**: License
- **Description**: README states the project is licensed under **AGPL-3.0** only. Repository also contains **LICENSE-MIT** (Copyright “2025-2005.9 Luis Pater” / “2025.9-present Router-For.ME”). No clear dual-license exception grant (e.g. “or MIT at your option”) is documented in README.
- **Evidence**:
  - `LICENSE` header: `GNU AFFERO GENERAL PUBLIC LICENSE Version 3`
  - README: “This project is licensed under the AGPL-3.0 License - see the LICENSE file”
  - `LICENSE-MIT` present at repo root with MIT grant text
- **Impact**:
  - **AGPL-3.0** is network copyleft: modified versions offered as a network service must offer corresponding source to users of that service. Material for SaaS/embedders and for proprietary downstream products.
  - Ambiguous MIT file can create false confidence that the work is permissively licensed, or conversely create compliance confusion in SBOM/legal review.
  - Dependencies are mostly MIT/BSD/Apache-2.0 and are **compatible as inbound** deps under AGPL, but **outbound** distribution of *this* codebase remains AGPL-constrained unless a real dual-license is intended and documented.
- **Recommendation**:
  1. Legal owner should publish one clear policy: AGPL-only **or** explicit dual-license (“AGPL-3.0 OR MIT”) with scope (whole repo vs. specific paths/sdk).
  2. If AGPL-only: remove or relocate `LICENSE-MIT` to avoid misrepresentation; if MIT applies only to upstream-derived files, say so per-file.
  3. Downstream commercial users need AGPL source-offer process for network deployment.
- **References**: AGPLv3 §13 (remote network interaction); README License section

---

## Medium Findings

### [MEDIUM] Security-relevant golang.org/x/* packages behind latest proxy versions
- **File**: `go.mod` lines 27–31
- **Category**: Outdated
- **Description**: Several `golang.org/x` modules used for crypto, HTTP/HTTP2, OAuth, and terminals have newer minor releases available via `go list -m -u`.
- **Evidence** (`go list -m -u`, 2026-07-26, GOPROXY reachable):

  | Package | Current | Latest on proxy |
  |---|---|---|
  | `golang.org/x/crypto` | v0.52.0 | **v0.54.0** |
  | `golang.org/x/net` | v0.55.0 | **v0.57.0** |
  | `golang.org/x/sync` | v0.20.0 | **v0.22.0** |
  | `golang.org/x/term` | v0.43.0 | **v0.45.0** |
  | `golang.org/x/sys` (indirect) | v0.45.0 | **v0.47.0** |
  | `golang.org/x/text` (indirect) | v0.37.0 | **v0.40.0** |
  | `golang.org/x/oauth2` | v0.36.0 | (no update flagged) |

- **Impact**: x/net and x/crypto historically carry HTTP/2, certificate, and parsing fixes. Being 2 minor versions behind is moderate risk without a specific CVE ID from govulncheck.
- **Recommendation**: Batch-bump `golang.org/x/crypto`, `x/net`, `x/sys`, `x/text`, `x/sync`, `x/term` together; run full test suite. Schedule monthly x/* refresh.
- **References**: `go list -m -u golang.org/x/crypto golang.org/x/net ...`

### [MEDIUM] klauspost/compress and quic-go minor/patch behind
- **File**: `go.mod` line 19; indirect `github.com/quic-go/quic-go v0.59.1`
- **Category**: Outdated
- **Evidence**:
  - `github.com/klauspost/compress v1.18.6` → **v1.19.1** (used for zstd in logging/executor)
  - `github.com/quic-go/quic-go v0.59.1` → **v0.61.0** (pulled via Gin HTTP/3 support)
- **Impact**: Compression libs and QUIC stacks are common CVE targets; lag increases window of exposure.
- **Recommendation**: Update compress directly; re-resolve quic-go via Gin/`go get` as compatible. Confirm HTTP/3 not required — if unused, explore build tags to drop quic (see weight finding).
- **References**: proxy latest versions as of audit date

### [MEDIUM] Gin v1.12.0 pulls quic-go HTTP/3 and mongo-driver BSON into the build
- **File**: transitive via `github.com/gin-gonic/gin v1.12.0`
- **Category**: Weight / Attack surface / Transitive risk
- **Description**: Gin’s `gin.go` imports `quic-go/http3`; `binding/bson.go` and `render/bson.go` import `go.mongodb.org/mongo-driver/v2/bson`. `go list -deps ./...` shows these packages **are linked** into this module’s dependency set (not merely listed in Gin’s go.mod).
- **Evidence**:
  - `go list -deps ./...` includes `github.com/quic-go/quic-go/http3`, `go.mongodb.org/mongo-driver/v2/bson`, …
  - Project code does not appear to call HTTP/3 or BSON APIs directly; surface is framework-inherited
- **Impact**: Larger binary, more native code paths to audit, extra CVE blast radius (QUIC + BSON parsers) even if app only uses JSON HTTP/1.1/2.
- **Recommendation**:
  1. Track Gin issues/build tags for optional HTTP3/BSON.
  2. Periodically `go list -deps` diff to detect new heavy transitives.
  3. If binary size matters, evaluate `net/http` + lightweight router for non-management planes (large change — quarterly evaluate only).
- **References**: Gin module go.mod requires `quic-go` and `mongo-driver/v2`

### [MEDIUM] mattn/go-sqlite3 CGO dependency in server/usage paths
- **File**: `go.mod` line 57 (`github.com/mattn/go-sqlite3 v1.14.45`); `recover.go`; `internal/usage/keeper/backup/sqlite_backup_cgo.go` (`//go:build cgo`); blank-import in root/`cmd` usage paths
- **Category**: Supply Chain / Portability
- **Description**: CGO SQLite driver enables backup/recover features and is required for pure `sqlite3` driver registration. Non-CGO build has `sqlite_backup_nocgo.go` counterpart for backup package, but root `recover.go` and gorm sqlite driver stack still imply CGO for full functionality.
- **Impact**: Cross-compile friction; CGO attack surface (cgo + libsqlite); deployment images need gcc/toolchain or prebuilt.
- **Recommendation**: Prefer `modernc.org/sqlite` (pure Go) for default embeds if CGO is painful; keep CGO path opt-in for performance backups. Document `CGO_ENABLED` requirements in ops docs.
- **References**: `go list -m -u` shows v1.14.45 → v1.14.48 available

### [MEDIUM] pkg/browser pinned to 2024 pseudo-version; limited maintenance signal
- **File**: `go.mod` line 21; `internal/browser/browser.go`
- **Category**: Outdated / Supply Chain
- **Description**: `github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c` — pseudo-version, no semver tag update flagged by `go list -m -u`. Used to open OAuth/browser flows.
- **Impact**: Unmaintained-feeling dependency for OS-specific browser launch; historically fine but stagnant. Low direct RCE likelihood if URL inputs are controlled; still a supply-chain weak link.
- **Recommendation**: Vendor a 20-line OS switch (`xdg-open` / `open` / `cmd /c start`) and drop the module, or confirm upstream activity quarterly.
- **References**: pseudo-version date 2024-01-02

### [MEDIUM] TUI stack (bubbletea/bubbles/lipgloss/clipboard) always imported by server binary
- **File**: `go.mod` lines 7–10, 8–9; `cmd/server/main.go` imports `internal/tui`
- **Category**: Weight
- **Description**: Charmbracelet TUI stack + `atotto/clipboard` ship inside the same server entrypoint as the API proxy.
- **Impact**: Larger attack/analysis surface and binary size for headless container deploys that never use `--tui`.
- **Recommendation**: Build tags or separate `cmd/cli-tui` vs `cmd/server` entrypoints; compile TUI only with `//go:build tui`.
- **References**: `cmd/server/main.go` import of `internal/tui`; clipboard used from `internal/tui/keys_tab.go`

### [MEDIUM] Niche tokenizers and single-purpose crypto-adjacent libs
- **File**: `github.com/tiktoken-go/tokenizer v0.8.0` (→ v0.8.1 available); `github.com/pierrec/xxHash v0.1.5`
- **Category**: Supply Chain
- **Description**: `tiktoken-go/tokenizer` is a community BPE tokenizer (MIT) used for local token counting in executor/helps. Smaller maintainer base than official OpenAI stacks. `xxHash` used in executor.
- **Impact**: Supply-chain concentration risk; tokenizer correctness bugs affect billing/limits, not typically RCE.
- **Recommendation**: Keep current (patch to v0.8.1); add golden-file tests for token counts; watch upstream commit activity.
- **References**: import from `internal/runtime/executor` and `helps`

### [MEDIUM] minio-go / gorm / redis — heavy optional backends compiled in by default
- **File**: `go.mod` minio-go v7.2.0, gorm v1.31.1 + drivers, redis v9.20.0
- **Category**: Weight
- **Description**: Object store, ORM+Postgres+SQLite, and Redis clients are direct deps used under `internal/store`, `internal/usage/keeper`, `internal/home`. Feature-flagged at runtime but **always compiled**.
- **Evidence**: All show import reachability via `go list`; updates available: minio v7.2.1, gorm v1.31.2, redis v9.21.0
- **Impact**: Binary weight and dependency CVE surface even for file-only deployments.
- **Recommendation**: Optional build tags per storage backend (`objectstore`, `pgstore`, `redis`) for slim releases; otherwise accept as product cost of multi-backend proxy.
- **References**: `internal/store/{objectstore,postgresstore,gitstore}.go`, `internal/home` redis usage

### [MEDIUM] Fork / multi-remote provenance complexity
- **File**: git remotes (read-only inspection)
- **Category**: Supply Chain
- **Description**: Working tree identifies as Plus fork lineage of `router-for-me/CLIProxyAPI` with remotes:
  - `cpa` → `https://github.com/router-for-me/CLIProxyAPI.git`
  - `github` → `https://github.com/liyao52033668/CLIProxyAPIPlus`
  - `origin` → `https://cnb.cool/liyao52033/CLIProxyAPIPlus.git`  
  Module path remains upstream `github.com/router-for-me/CLIProxyAPI/v7`.
- **Impact**: Consumers may confuse upstream vs Plus release artifacts; SBOM module path ≠ GitHub Plus repo path; supply-chain attestation harder.
- **Recommendation**: Document release provenance in SBOM/release notes; ensure CI builds from a single canonical remote; consider Go module major path strategy if Plus diverges long-term.
- **References**: `git remote -v`; README Plus positioning

---

## Low Findings

### [LOW] Minor/patch updates available on direct deps
- **File**: `go.mod`
- **Category**: Outdated
- **Evidence** (`go list -m -u` on direct require set):

  | Package | Current | Latest | Notes |
  |---|---|---|---|
  | `github.com/andybalholm/brotli` | v1.2.1 | v1.2.2 | compression |
  | `github.com/klauspost/compress` | v1.18.6 | v1.19.1 | also MEDIUM security-relevant |
  | `github.com/minio/minio-go/v7` | v7.2.0 | v7.2.1 | patch |
  | `github.com/mattn/go-sqlite3` | v1.14.45 | v1.14.48 | patch |
  | `github.com/redis/go-redis/v9` | v9.20.0 | v9.21.0 | minor |
  | `github.com/tiktoken-go/tokenizer` | v0.8.0 | v0.8.1 | patch |
  | `gorm.io/gorm` | v1.31.1 | v1.31.2 | patch |
  | `github.com/cloudflare/circl` (indirect via go-git) | v1.6.3 | v1.6.4 | crypto primitive lib |

- **Impact**: Low unless release notes cite security fixes.
- **Recommendation**: Include in next routine dependency bump PR.
- **References**: `go list -m -u` output 2026-07-26

### [LOW] logrus in maintenance-mode ecosystem vs slog
- **File**: `github.com/sirupsen/logrus v1.9.4` (no update flagged)
- **Category**: Outdated / Weight
- **Description**: Project standard is logrus (per AGENTS.md). Ecosystem momentum is `log/slog`; logrus still receives fixes but is not a greenfield choice.
- **Impact**: Long-term maintenance only; not an active vuln finding.
- **Recommendation**: No rush; evaluate slog migration quarterly if contribution cost allows.
- **References**: go.mod line 23

### [LOW] atotto/clipboard v0.1.4 — mature but stagnant
- **File**: `go.mod` line 7; TUI only
- **Category**: Outdated
- **Description**: No newer version on proxy; BSD-3-Clause; TUI key-tab clipboard integration only.
- **Impact**: Minimal if TUI optionalized (see MEDIUM TUI finding).
- **Recommendation**: Accept or replace with platform calls when splitting TUI binary.
- **References**: `internal/tui/keys_tab.go`

### [LOW] go list -m all environment inflation (tooling noise)
- **File**: N/A (tooling)
- **Category**: Config / Methodology
- **Description**: `go list -m all` returned **332** modules including `golangci-lint` and many linters **not present in go.sum**. Production `go list -deps ./...` is **96** modules; go.sum ~**131** paths. Suggests local tool/env module graph pollution when listing “all”, not a committed dependency bloat in this repo.
- **Impact**: Can mislead automated SBOM jobs if they use `go list -m all` in this environment without `-mod=readonly` discipline or go.sum intersection.
- **Recommendation**: SBOM from `go list -deps -json ./...` or `govulncheck -json ./...`; intersect with go.sum.
- **References**: compare `wc -l go.sum` vs `go list -m all | wc -l`

---

## License Summary

### Project license structure

| Artifact | Spelled license | Role |
|---|---|---|
| `LICENSE` | **GNU AGPL v3** | Primary per README |
| `LICENSE-MIT` | **MIT** | Co-shipped; relationship **undocumented** in README |
| README / README_CN | AGPL-3.0 only | User-facing claim |

**Copyleft implications:** Offering modified CLIProxyAPIPlus as a network service triggers AGPL source-offer obligations to service users. Inbound dependencies below are permissive/weak-copyleft-friendly and do not block AGPL distribution, but they also **do not** relicense this project to MIT.

**Knowledge note:** gorm driver license files are named `License` (MIT text confirmed in module cache). Some licenses below are from module-cache LICENSE files (read-only); where marked “knowledge”, cache file was missing or ambiguous.

### Direct dependency license table

| Package | Version | License | Risk |
|---|---|---|---|
| `github.com/andybalholm/brotli` | v1.2.1 | MIT (Brotli Authors) | Low |
| `github.com/atotto/clipboard` | v0.1.4 | BSD-3-Clause | Low |
| `github.com/charmbracelet/bubbles` | v1.0.0 | MIT | Low |
| `github.com/charmbracelet/bubbletea` | v1.3.10 | MIT | Low |
| `github.com/charmbracelet/lipgloss` | v1.1.0 | MIT | Low |
| `github.com/fsnotify/fsnotify` | v1.10.1 | BSD-3-Clause | Low |
| `github.com/fxamacker/cbor/v2` | v2.9.2 | MIT | Low |
| `github.com/gin-gonic/gin` | v1.12.0 | MIT | Low |
| `github.com/go-git/go-git/v6` | v6.0.0-alpha.4 | Apache-2.0 | Low license / **High stability** |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | Low |
| `github.com/gorilla/websocket` | v1.5.3 | BSD-2-Clause | Low |
| `github.com/jackc/pgx/v5` | v5.10.0 | MIT | Low |
| `github.com/joho/godotenv` | v1.5.1 | MIT | Low |
| `github.com/klauspost/compress` | v1.18.6 | BSD-3-Clause / Apache-2.0 (multi) | Low |
| `github.com/minio/minio-go/v7` | v7.2.0 | Apache-2.0 | Low |
| `github.com/pkg/browser` | v0.0.0-20240102092130-5ac0b6a4141c | BSD-2-Clause | Low |
| `github.com/refraction-networking/utls` | v1.8.2 | BSD-3-Clause | Low license / **High dual-use** |
| `github.com/sirupsen/logrus` | v1.9.4 | MIT | Low |
| `github.com/tidwall/gjson` | v1.19.0 | MIT | Low |
| `github.com/tidwall/sjson` | v1.2.5 | MIT | Low |
| `github.com/tiktoken-go/tokenizer` | v0.8.0 | MIT | Low |
| `golang.org/x/crypto` | v0.52.0 | BSD-3-Clause | Low |
| `golang.org/x/net` | v0.55.0 | BSD-3-Clause | Low |
| `golang.org/x/oauth2` | v0.36.0 | BSD-3-Clause | Low |
| `golang.org/x/sync` | v0.20.0 | BSD-3-Clause | Low |
| `golang.org/x/term` | v0.43.0 | BSD-3-Clause | Low |
| `gopkg.in/natefinch/lumberjack.v2` | v2.2.1 | MIT | Low |
| `gopkg.in/yaml.v3` | v3.0.1 | MIT **and** Apache-2.0 (dual, upstream notice) | Low |
| `gorm.io/driver/postgres` | v1.6.0 | MIT (License file in cache) | Low |
| `gorm.io/driver/sqlite` | v1.6.0 | MIT (License file in cache) | Low |
| `gorm.io/gorm` | v1.31.1 | MIT | Low |
| `github.com/mattn/go-sqlite3` | v1.14.45 | MIT | Low |
| `github.com/redis/go-redis/v9` | v9.20.0 | BSD-2-Clause | Low |
| `github.com/pierrec/xxHash` | v0.1.5 | BSD-3-Clause | Low |
| `google.golang.org/protobuf` | v1.36.11 | BSD-3-Clause | Low |

**Inbound license conflicts with AGPL project:** None identified among direct deps (no GPL-2-only, no proprietary).  
**Outbound risk:** AGPL (and ambiguous MIT file) — see HIGH finding.

---

## Outdated Dependencies

| Package | Current | Latest (proxy) | Behind By | Breaking Changes | Priority |
|---|---|---|---|---|---|
| `golang.org/x/crypto` | v0.52.0 | v0.54.0 | 2 minor | Unlikely (v0.x) | **High** (security-relevant) |
| `golang.org/x/net` | v0.55.0 | v0.57.0 | 2 minor | Unlikely | **High** |
| `golang.org/x/sys` | v0.45.0 | v0.47.0 | 2 minor | Unlikely | Medium |
| `golang.org/x/text` | v0.37.0 | v0.40.0 | 3 minor | Unlikely | Medium |
| `golang.org/x/sync` | v0.20.0 | v0.22.0 | 2 minor | Unlikely | Medium |
| `golang.org/x/term` | v0.43.0 | v0.45.0 | 2 minor | Unlikely | Low–Med |
| `github.com/klauspost/compress` | v1.18.6 | v1.19.1 | 1 minor | Check release notes | **High** |
| `github.com/quic-go/quic-go` | v0.59.1 | v0.61.0 | 2 minor | Possible API | Medium |
| `github.com/redis/go-redis/v9` | v9.20.0 | v9.21.0 | 1 minor | Unlikely | Medium |
| `github.com/cloudflare/circl` | v1.6.3 | v1.6.4 | patch | Unlikely | Medium |
| `github.com/minio/minio-go/v7` | v7.2.0 | v7.2.1 | patch | Unlikely | Low |
| `github.com/mattn/go-sqlite3` | v1.14.45 | v1.14.48 | patch | Unlikely | Low |
| `github.com/andybalholm/brotli` | v1.2.1 | v1.2.2 | patch | Unlikely | Low |
| `github.com/tiktoken-go/tokenizer` | v0.8.0 | v0.8.1 | patch | Unlikely | Low |
| `gorm.io/gorm` | v1.31.1 | v1.31.2 | patch | Unlikely | Low |
| `github.com/go-git/go-git/v6` | v6.0.0-alpha.4 | (still alpha line) | n/a | **Alpha API** | **High** (stability) |
| `github.com/pkg/browser` | pseudo 20240102 | none flagged | stagnant | n/a | Medium |
| `github.com/gin-gonic/gin` | v1.12.0 | none flagged | current | — | OK |
| `golang.org/x/oauth2` | v0.36.0 | none flagged | current | — | OK |
| `github.com/refraction-networking/utls` | v1.8.2 | none flagged | current tag | — | Monitor |
| `gopkg.in/yaml.v3` | v3.0.1 | none flagged | current | — | OK |

**Outdated count (direct or security-relevant with newer version):** 15 rows with updates or stagnant flags above (excluding “OK”).

---

## Unused Dependencies

**Result: Clean — 0 unused direct dependencies.**

Method: `go list` import closure over `./...` matched against each of the 35 direct module paths (including subpackages). Every direct module had ≥1 import hit.

| Package | Import hits (packages) | Primary consumers |
|---|---|---|
| All 35 direct modules | ≥1 each | store, executor, auth, tui, usage, logging, browser, home, cmd/server, … |

**Note:** “Used” ≠ “needed in every deploy”. Optional backends (minio, go-git, redis, gorm, TUI) are compile-time always-on, runtime optional — see Weight findings. That is **not** the same as go.mod cruft.

**go.sum-only / test-ish modules** (present in sum, not all in production package import set): testify, go-spew, go-cmp, go-git fixtures, bsm/ginkgo (redis tests), etc. Normal for Go modules; not “unused direct deps.”

---

## Upgrade Roadmap

### 1. Immediate (this week)
1. Install/run **`govulncheck ./...`** in CI and locally; triaging any HIGH/CRITICAL CVEs overrides this roadmap.
2. Bump **`golang.org/x/crypto`**, **`golang.org/x/net`**, **`golang.org/x/sys`**, **`golang.org/x/text`**, **`golang.org/x/sync`**, **`golang.org/x/term`** to latest patch/minor; `gofmt` + full `go test ./...`.
3. Bump **`klauspost/compress`** to v1.19.x.
4. Clarify **LICENSE** policy (AGPL-only vs dual) — legal, not just engineering.

### 2. This sprint
1. Patch bumps: brotli, minio-go, redis, gorm, go-sqlite3, tiktoken-go, circl (transitive via tidy).
2. Risk-assess **go-git v6 alpha** for any production GITSTORE users; document experimental or pin migration plan to v5/stable.
3. Config-flag or document **uTLS** usage; ensure operators understand fingerprint evasion behavior and ToS risk.
4. Add CI step: `go mod verify` + `govulncheck` + `go list -m -u` report.

### 3. This quarter
1. Evaluate **build tags** to optionalize TUI, objectstore, gitstore, redis for slim server builds.
2. Evaluate **pure-Go SQLite** (`modernc.org/sqlite`) vs CGO `mattn/go-sqlite3`.
3. Track Gin HTTP/3 + BSON transitive weight; revisit if quic CVEs appear.
4. SBOM generation aligned to production deps (not inflated `go list -m all`).

### 4. Evaluate (backlog)
1. logrus → slog migration cost.
2. Replace `pkg/browser` with tiny internal helper.
3. Long-term module path / fork provenance if Plus diverges from upstream.
4. Whether Anthropic access can drop uTLS as upstream anti-bot posture changes.

---

## Checklist Coverage

| # | Category | Status |
|---|---|---|
| 1 | Security vulnerabilities | **CHECKED — govulncheck unavailable; manual review: 0 pinned CVE IDs; residual risk noted** |
| 2 | Outdated dependencies | **CHECKED — 15 update/stale rows; prioritize x/crypto, x/net, compress, go-git alpha** |
| 3 | License compliance | **CHECKED — full direct-dep table; AGPL+MIT ambiguity = HIGH** |
| 4 | Supply chain | **CHECKED — verify PASS; no replace; alpha go-git; uTLS dual-use; pseudo browser; Plus fork remotes** |
| 5 | Unused deps | **CHECKED — Clean (0)** |
| 6 | Weight & alternatives | **CHECKED — Gin quic/mongo, TUI-in-server, gorm/minio/redis always linked, CGO sqlite** |
| 7 | Config (go.mod hygiene) | **CHECKED — go 1.26 matches toolchain 1.26.5; go.sum committed; no replace/exclude/retract** |
| 8 | Transitive risks | **CHECKED — ~96 prod modules; quic-go + mongo bson via Gin; circl via go-git; graph noise documented** |

---

## Files Reviewed

- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/go.mod`
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/go.sum` (integrity via `go mod verify`; line/module counts)
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/LICENSE`
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/LICENSE-MIT`
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/README.md` (License section)
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/README_CN.md` (License line)
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/auth/claude/utls_transport.go`
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/runtime/executor/helps/utls_client.go` (via import listing / line samples)
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/store/gitstore.go` (import list)
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/usage/keeper/backup/sqlite_backup_cgo.go`
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/recover.go`
- `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/cmd/server/main.go` (TUI import coupling)
- Module-cache LICENSE files for direct deps under `GOMODCACHE` (read-only)
- Gin `@v1.12.0` go.mod and import sites for quic/mongo (module cache)

**Explicitly not read (FORBIDDEN):** `.env`, `config.yaml`, `auths/**`, `CLIProxyAPI/**`

---

## Methodology Notes

### Tools used (read-only)
- `go version` → go1.26.5
- `which govulncheck` → **not installed** (no install/`go run` attempted)
- `go mod verify` → success (“all modules verified”)
- `go list -m all` / `go list -m -u` / `go list -m -u <direct…>` (network via configured GOPROXY — succeeded)
- `go mod graph` + Python filtering against go.sum
- `go list -deps ./...` and `go list` import closure for reachability / unused detection
- Read of LICENSE files and selected source call sites
- `git remote -v`, `git ls-files go.mod go.sum` (go.sum is tracked)

### Not used / constraints
- No `go get`, `go mod tidy`, `go install`, or any go.mod/go.sum mutation
- No recursive grep over secrets-bearing trees; import analysis via `go list`
- No reading of `.env`, `config.yaml`, `auths/`, or nested `CLIProxyAPI/`

### Knowledge-cutoff & CVE caveats
- Audit calendar date: **2026-07-26**
- Without **govulncheck** / OSV bulk query, CVE IDs are **not authoritatively enumerated**. Manual assessment used version recency, package role (crypto/HTTP/TLS), and known historical problem classes (YAML parsers, WebSocket, QUIC, compression).
- “No CRITICAL CVE confirmed” ≠ “no vulnerabilities exist.”
- `go list -m all` = 332 in this environment (includes golangci and linter modules **absent from go.sum**). Treat **96 production modules** + **go.sum (~131 paths)** as ground truth for this repo’s shipped dependency set.
- License determinations mixed **module-cache file reads** and well-known upstream licenses; not a law-firm license scan.

### Positive controls observed
- `go.sum` present and committed
- `go mod verify` clean
- No `replace` / `exclude` directives (no path hijack / local replace surprises)
- Direct requires are actually imported (no obvious go.mod rot)
- Security-relevant stacks (oauth2, gin, websocket, pgx) are on recent major lines for 2026

---

## Appendix A — Direct require inventory (go.mod)

```
github.com/andybalholm/brotli v1.2.1
github.com/atotto/clipboard v0.1.4
github.com/charmbracelet/bubbles v1.0.0
github.com/charmbracelet/bubbletea v1.3.10
github.com/charmbracelet/lipgloss v1.1.0
github.com/fsnotify/fsnotify v1.10.1
github.com/fxamacker/cbor/v2 v2.9.2
github.com/gin-gonic/gin v1.12.0
github.com/go-git/go-git/v6 v6.0.0-alpha.4          ## ALPHA
github.com/google/uuid v1.6.0
github.com/gorilla/websocket v1.5.3
github.com/jackc/pgx/v5 v5.10.0
github.com/joho/godotenv v1.5.1
github.com/klauspost/compress v1.18.6
github.com/minio/minio-go/v7 v7.2.0
github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c  ## pseudo
github.com/refraction-networking/utls v1.8.2         ## TLS fingerprint
github.com/sirupsen/logrus v1.9.4
github.com/tidwall/gjson v1.19.0
github.com/tidwall/sjson v1.2.5
github.com/tiktoken-go/tokenizer v0.8.0
golang.org/x/crypto v0.52.0
golang.org/x/net v0.55.0
golang.org/x/oauth2 v0.36.0
golang.org/x/sync v0.20.0
golang.org/x/term v0.43.0
gopkg.in/natefinch/lumberjack.v2 v2.2.1
gopkg.in/yaml.v3 v3.0.1
gorm.io/driver/postgres v1.6.0
gorm.io/driver/sqlite v1.6.0
gorm.io/gorm v1.31.1
github.com/mattn/go-sqlite3 v1.14.45                 ## CGO
github.com/redis/go-redis/v9 v9.20.0
github.com/pierrec/xxHash v0.1.5
google.golang.org/protobuf v1.36.11
```

## Appendix B — Finding count by severity

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 3 |
| MEDIUM | 9 |
| LOW | 4 |
| **Total structured findings** | **16** |

---

*End of Dependency Audit Report.*
