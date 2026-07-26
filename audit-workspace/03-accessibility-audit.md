# Accessibility Audit Report
Generated: 2026-07-26  
Auditor: Accessibility Agent (Overnight Repo Auditor)  
Standard: WCAG 2.1 Level AA  

## Executive Summary
- **Total findings: 24** — CRITICAL: 1 · HIGH: 8 · MEDIUM: 10 · LOW: 5
- **WCAG Conformance Level: DOES NOT CONFORM (Level AA)**
- **Scope note:** This repository is primarily a headless Go proxy. User-facing HTML is limited to:
  1. `static/management.html` (167 physical lines, ~2.7 MB single-file Vite/React SPA with inlined JS/CSS; body is only `<div id="root">`);
  2. Multiple embedded OAuth success/login/waiting pages under `internal/auth/**`, `internal/api/server.go`, and `sdk/auth/**`;
  3. Optional usage-keeper SPA served from an injected `staticFS` (`internal/usage/keeper/api/router.go`) — **no HTML asset is vendored in this repo**, so it is out of static reach.
- **Assessment:** The management control panel is a real client-rendered SPA (not a 167-line static shell in functional terms) and already implements several ARIA patterns (dialog `aria-modal`, login field labels, some `aria-live`, `focus-visible` on selected controls). It still fails AA on color contrast for tertiary/primary text and key button pairings, lacks skip navigation, and depends entirely on JavaScript with no progressive fallback. Embedded OAuth HTML is weaker: widespread missing `lang`, status polling without live regions, low-contrast accents, and auto-closing windows. Overall **does not conform** to WCAG 2.1 AA.

## Critical Findings

### [CRITICAL] Management UI is empty without JavaScript (no progressive text alternative)
- **File**: `static/management.html` (lines 2–9 shell; 166 body; scripts lines ~9–161)
- **Category**: Robust / Perceivable (non-text content / status messages)
- **Description**: Document body contains only `<div id="root"></div>`. There is no `<noscript>` guidance, no server-rendered landmarks, and no textual alternative if the module bundle fails to load or execute.
- **Evidence**:
  ```html
  <html lang="zh-CN" translate="no" class="notranslate">
  ...
  <body>
    <div id="root"></div>
  </body>
  ```
- **Impact**: Users of locked-down browsers, broken CDN/local asset paths, or failed script parse get a blank page with no operable interface (management key login, config, auth files). Assistive technology that can run JS is less affected; environments that cannot are fully blocked from the control panel.
- **Recommendation**: Add a visible `<noscript>` block explaining that JavaScript is required and how to use CLI/API alternatives; optionally server-render a minimal login/status skeleton. Keep critical error text outside the SPA root.
- **References**: WCAG 2.1 1.1.1 (A), 1.3.1 (A), 4.1.2 (A); best practice progressive enhancement

## High Findings

### [HIGH] Tertiary and quaternary text tokens fail AA contrast
- **File**: `static/management.html` (CSS variables in line 162 style block)
- **Category**: Contrast
- **Description**: Theme tokens `--text-tertiary:#a29c95` and `--text-quaternary:#c0bab3` are used widely for secondary UI copy. Measured contrast against light surfaces (`#fff` / `#faf9f5` / `#f0eee8`) is **~1.8–2.7:1**, below the 4.5:1 AA requirement for normal text (and below 3:1 large text).
- **Evidence** (computed):
  - `#a29c95` on `#ffffff` ≈ **2.72:1** (FAIL)
  - `#a29c95` on `#faf9f5` ≈ **2.58:1** (FAIL)
  - `#c0bab3` on `#ffffff` ≈ **1.92:1** (FAIL)
  - Token usage density: `text-tertiary` appears ~73 times in the bundle
- **Impact**: Hints, metadata, timestamps, and muted labels become unreadable for low-vision users.
- **Recommendation**: Darken tertiary to ≥ `#6d6760` (existing secondary, ~5.3:1 on `#faf9f5`) or darker; reserve quaternary for non-text dividers only, or ensure 3:1 against adjacent colors for UI components (1.4.11).
- **References**: WCAG 2.1 1.4.3 Contrast (Minimum) (AA), 1.4.11 Non-text Contrast (AA)

### [HIGH] Primary control colors fail text contrast on buttons/links
- **File**: `static/management.html` (line 162: `--primary-color:#8b8680`); also OAuth templates using white-on-blue/red buttons
- **Category**: Contrast
- **Description**: White label text on primary surfaces does not meet 4.5:1 for normal-sized button text.
- **Evidence** (computed):
  - `#ffffff` on `#8b8680` ≈ **3.61:1** (FAIL AA text)
  - `#ffffff` on `#3b82f6` (Claude/Codex primary button) ≈ **3.68:1** (FAIL)
  - `#ffffff` on `#667eea` (Kiro auth buttons) ≈ **3.66:1** (FAIL)
  - `#ffffff` on `#e53935` (CodeArts/JoyCode) ≈ **4.23:1** (FAIL AA text, borderline)
- **Impact**: Primary actions (login submit, OAuth continue, IDC submit) are harder to read; fails AA for essential controls.
- **Recommendation**: Darken primary fills (e.g. ≥ `#59554f` / `#1d4ed8` / `#b71c1c`) or use dark text on light primary backgrounds; verify with a contrast checker in both light and dark themes.
- **References**: WCAG 2.1 1.4.3 (AA)

### [HIGH] OAuth polling status regions lack `aria-live` / `role="status"`
- **File**:  
  - `internal/auth/codearts/oauth_web.go` (`codeArtsWaitingPage`, ~L380–346, status `#status` L397)  
  - `internal/auth/joycode/oauth_web.go` (`joyCodeWaitingPage`, ~L377+, status L394)  
  - `internal/auth/kiro/oauth_web_templates.go` (`#statusBox`, ~L190, JS updates ~L259–271)
- **Category**: Dynamic content / Status messages
- **Description**: Login waiting pages poll every 3–5s and rewrite status text (`Waiting…` / success / error). The status nodes are plain `<div>`s without `aria-live`, `role="status"`, or `role="alert"`.
- **Evidence**:
  ```html
  <div id="status">&#x23f3; Waiting for login callback...</div>
  ```
  ```javascript
  el.textContent = "✅ " + data.message; // no live region
  ```
- **Impact**: Screen-reader users are not notified when authentication succeeds or fails unless they manually re-navigate the page; core OAuth completion feedback is effectively silent.
- **Recommendation**: Use `<div id="status" role="status" aria-live="polite" aria-atomic="true">` (errors: `role="alert"` or `aria-live="assertive"`). Prefer text status words over emoji-only cues.
- **References**: WCAG 2.1 4.1.3 Status Messages (AA), 1.3.1 (A)

### [HIGH] Most OAuth/callback HTML documents omit `lang` on `<html>`
- **File**:  
  - `internal/api/server.go` L62 (`oauthCallbackSuccessHTML`, reused by many `/…/callback` routes L545–670)  
  - `internal/auth/codearts/oauth_web.go` login/waiting/success snippets  
  - `internal/auth/joycode/oauth_web.go`  
  - `internal/auth/kiro/oauth_web_templates.go`, `protocol_handler.go`, `sso_oidc.go`, `social_auth.go`  
  - `sdk/auth/qoder.go`, `codearts.go`, `joycode.go`  
  - Positive counterexample: `internal/auth/claude/html_templates.go` / `codex/html_templates.go` use `lang="en"`
- **Category**: Language of page
- **Description**: `<html>` has no `lang` attribute on the majority of embedded pages.
- **Evidence**:
  ```html
  <!DOCTYPE html><html><head><meta charset="utf-8"><title>Authentication successful</title>...
  ```
- **Impact**: Screen readers may use the wrong voice/pronunciation rules for English copy.
- **Recommendation**: Add `lang="en"` (or appropriate locale) to every HTML document string; keep consistent with page content language.
- **References**: WCAG 2.1 3.1.1 Language of Page (A)

### [HIGH] Auto-closing OAuth windows without adjustable time limit
- **File**:  
  - `internal/api/server.go` L62 (`setTimeout(...,3000); window.close()`)  
  - `internal/auth/claude/html_templates.go` / `codex/html_templates.go` (10s countdown then `window.close()`)  
  - `internal/auth/joycode/oauth_web.go` success (2s)  
  - Kiro/protocol success pages also call `window.close()`
- **Category**: Timing / Operable
- **Description**: Success pages automatically close the tab/window after 2–10 seconds. There is generally no control to extend, pause, or disable the timeout (Claude/Codex at least focus a Close button and allow Escape).
- **Evidence**:
  ```html
  <script>setTimeout(function(){window.close();},3000);</script>
  ```
- **Impact**: Users who need more time to read success/error text (cognitive disabilities, screen magnification, motor impairment) may lose the message. Also disruptive if focus was not expected to leave the page.
- **Recommendation**: Default to **no auto-close**, or provide ≥20s with visible “Stay open” / “Close now” controls that cancel the timer (WCAG 2.2.1). Announce countdown via `aria-live` if retained.
- **References**: WCAG 2.1 2.2.1 Timing Adjustable (A), 2.2.4 Interruptions (AAA optional)

### [HIGH] Management SPA has no skip link to main content
- **File**: `static/management.html` (SPA chrome: `main-header`, `sidebar`, `main-content` classes; one `nav` / `main` / `aside` via React create)
- **Category**: Keyboard / Navigation
- **Description**: Bundle includes header actions, sidebar navigation, and main content regions, but no “Skip to main content” link (search for `skip-link` / “skip to” patterns: none). Keyboard users must tab through chrome on every route change.
- **Evidence**: Semantic counts show `nav`/`main`/`aside` exist in JS, but no skip-link CSS/class or first-focus link in the shell.
- **Impact**: Material keyboard overhead for power users and motor-impaired users on a multi-page admin shell.
- **Recommendation**: Add a visually hidden, focus-visible skip link as the first focusable element targeting `<main id="main">` (or equivalent). Ensure route changes move focus to the new `h1`/main.
- **References**: WCAG 2.1 2.4.1 Bypass Blocks (A)

### [HIGH] Widespread `outline: none` with incomplete focus-visible coverage
- **File**: `static/management.html` (line 162 CSS: ~26 `outline:none`; ~10 `focus-visible` rules); `internal/auth/kiro/oauth_web_templates.go` L480, L569
- **Category**: Focus visibility / Keyboard
- **Description**: Many interactive styles remove the default outline. Management partially restores focus via box-shadow/`focus-visible` on selected components (header buttons, checkboxes, some tags), but not uniformly. Kiro form inputs/buttons set `outline: none` with **no** `:focus` / `:focus-visible` replacement.
- **Evidence**:
  ```css
  /* management: common pattern */
  :focus{border-color:var(--primary-color);outline:none;box-shadow:0 0 0 3px #8b86802e}
  /* kiro templates */
  outline: none;
  ```
  Kiro file has zero `:focus-visible` rules.
- **Impact**: Keyboard users may lose track of focus on OAuth forms and on management controls that only define `:hover` or incomplete focus styles. Low-contrast focus rings (`#8b86802e`) may also fail 1.4.11 (3:1).
- **Recommendation**: Never remove outline without a ≥3:1 visible focus indicator on **all** interactive elements; prefer `:focus-visible`. Audit Kiro CSS immediately.
- **References**: WCAG 2.1 2.4.7 Focus Visible (AA), 1.4.11 Non-text Contrast (AA)

### [HIGH] Canvas/chart visualizations lack discoverable text alternatives
- **File**: `static/management.html` (Usage page chart styles; `(0,H.jsx)(\`canvas\`)` present; chart scroller CSS references `canvas`)
- **Category**: Non-text content / Charts
- **Description**: Usage analytics appear to render via `<canvas>` (not Recharts SVG). Static analysis found no accompanying long descriptions, data tables toggles, or `aria-label` patterns bound to chart canvases.
- **Evidence**: `canvas` element creation present; CSS `.UsagePage-module__chartScroller… canvas{touch-action:…}`; no chart-specific `aria-label`/`role="img"` pairing found adjacent to chart construction.
- **Impact**: Screen-reader users cannot perceive trends that sighted users get from charts (quota/usage core admin tasks).
- **Recommendation**: Provide a data table or downloadable summary next to each chart; set `role="img"` + concise `aria-label` summarizing the series; ensure keyboard access to any chart tooltips.
- **References**: WCAG 2.1 1.1.1 (A), 1.4.5 Images of Text (AA if labels are painted)

## Medium Findings

### [MEDIUM] `translate="no"` / `notranslate` blocks assistive translation
- **File**: `static/management.html` L2
- **Category**: Understandable / Language
- **Description**: Root HTML forces `translate="no"` and `class="notranslate"`, plus `<meta name="google" content="notranslate" />`.
- **Evidence**:
  ```html
  <html lang="zh-CN" translate="no" class="notranslate">
  ```
- **Impact**: Users who rely on browser/page translation (including some cognitive and second-language users) cannot auto-translate the management UI. The SPA does include its own i18n (`document.documentElement.lang` is updated in JS), which mitigates but does not replace user-agent translation for uncovered strings.
- **Recommendation**: Remove global translate suppression, or limit it to code/config samples only.
- **References**: WCAG 2.1 3.1.2 Language of Parts (AA) spirit; usability / 3.1.1 related

### [MEDIUM] Initial document language vs. English title / mixed UI language
- **File**: `static/management.html` L2–8
- **Category**: Language
- **Description**: `lang="zh-CN"` while `<title>CLI Proxy API Management Center</title>` is English. JS later sets `document.documentElement.lang` from i18n state, but the first paint and non-JS title remain mismatched.
- **Impact**: Minor AT mis-voiceing of the title; SEO/AT inconsistency.
- **Recommendation**: Align default `lang` with default UI locale, or localize `<title>` with the same i18n catalog.
- **References**: WCAG 2.1 3.1.1 (A)

### [MEDIUM] Status and success/error feedback rely on emoji + color
- **File**: OAuth templates across CodeArts/JoyCode/Kiro/server callback HTML
- **Category**: Use of color / Non-text content
- **Description**: Status often prefixed with ✅/❌/⏳ and color classes; Kiro status uses left border color (`.status-pending|success|failed`) as a strong cue. Text is usually present, so not a pure color-only failure, but emoji may be announced verbosely or inconsistently.
- **Evidence**: `#status` / `#statusBox` updates with emoji; Claude/Codex decorative `✓` in `.success-icon` without `aria-hidden="true"`.
- **Impact**: Noisy or redundant SR announcements; decorative glyphs read as content.
- **Recommendation**: Mark decorative emoji/icons `aria-hidden="true"`; keep plain-language status text first; do not rely on border color alone.
- **References**: WCAG 2.1 1.4.1 Use of Color (A), 1.1.1 (A)

### [MEDIUM] Waiting pages open provider login via `target="_blank"` without disclosure or `rel`
- **File**: `internal/auth/codearts/oauth_web.go` L396; JoyCode waiting page analogous; Claude/Codex success “Open Platform” links L169+
- **Category**: Link purpose / Window behavior
- **Description**: “Open … Login” links use `target="_blank"` without telling users a new window/tab will open, and without `rel="noopener noreferrer"` in several templates.
- **Impact**: Disorientation for AT users; minor security tab-napping risk (`window.opener`).
- **Recommendation**: Add visible text “(opens in a new tab)” and `rel="noopener noreferrer"`.
- **References**: WCAG 2.1 3.2.2 On Input (A) / 3.2.5 (AAA); G201 technique

### [MEDIUM] Management reduced-motion support is partial
- **File**: `static/management.html` (line 162: only 2 `prefers-reduced-motion` blocks)
- **Category**: Animation from interactions
- **Description**: Reduced motion only adjusts glass backdrop / a provider nav indicator transition. Multiple UI animations (dialogs, progress width transitions, transforms on OAuth pages) remain.
- **Impact**: Vestibular users may still experience motion in the SPA and especially on OAuth gradient/slideIn pages (Claude/Codex `slideIn` animation has no reduced-motion guard).
- **Recommendation**: Global `@media (prefers-reduced-motion: reduce) { *, *::before, *::after { animation: none !important; transition: none !important; } }` with intentional exceptions.
- **References**: WCAG 2.1 2.3.3 Animation from Interactions (AAA) — recommended; related 2.2.2 Pause/Stop/Hide (A) for continuous motion

### [MEDIUM] Remote auto-updated management asset drifts from audited build
- **File**: `internal/managementasset/updater.go` (L27–34, L67–109, L194+); served by `internal/api/server.go` `serveManagementControlPanel`
- **Category**: Process / Robust
- **Description**: Production may replace local `static/management.html` from GitHub release `liyao52033668/Cli-Proxy-API-Management-Center` (fallback `https://cpamc.xiaoying.org.cn`). Accessibility fixes applied only in-repo can be overwritten; conversely, remote UI improvements are not guaranteed by this audit snapshot.
- **Impact**: Conformance cannot be claimed for deployed panels without pinning/version-auditing the asset.
- **Recommendation**: Pin release digests (already partially hashed), run axe/Lighthouse in the panel repo CI, and document the panel version in the management footer.
- **References**: WCAG conformance claims require a defined set of web pages; EN 301 549 process note

### [MEDIUM] Usage Keeper UI not present for static audit
- **File**: `internal/usage/keeper/api/router.go` L115–196 (`staticFS`, `index.html`)
- **Category**: Scope limitation
- **Description**: Router serves `index.html` and `/assets/*` from an injected filesystem. No HTML/JS/CSS for that UI is checked into this repository.
- **Impact**: Any accessibility defects in Usage Keeper front-end are unknown; operators enabling that module must audit the external asset separately.
- **Recommendation**: Vendor or submodule the UI, or add CI that fetches the built asset and runs automated a11y tests.
- **References**: WCAG conformance scope

### [MEDIUM] Placeholder-heavy forms risk missing persistent labels (partial)
- **File**: `static/management.html` (minified: ~204 `placeholder:` vs ~10 `htmlFor` / ~64 `label` elements)
- **Category**: Form labels
- **Description**: Login path shows a good pattern (`label` + password toggle `aria-label`). Overall bundle has far more placeholders than `htmlFor` associations. Some fields may rely on placeholder-only or wrapper-label patterns that are hard to verify when minified.
- **Evidence**: Login snippet uses `label:e(\`login.management_key_label\`)` with show/hide `aria-label` — good. Global ratio placeholders ≫ htmlFor suggests residual risk on secondary forms.
- **Impact**: If any field is placeholder-only, SR/browser autofill and cognitive users lose the label after typing (3.3.2).
- **Recommendation**: Enforce a shared `FormField` that always renders a visible `<label>` / `aria-labelledby`; ban placeholder-only inputs in component lint rules.
- **References**: WCAG 2.1 3.3.2 Labels or Instructions (A), 1.3.1 (A), 4.1.2 (A)

### [MEDIUM] Dialogs exist but full focus-trap quality not verifiable in minified SPA
- **File**: `static/management.html` (role:`dialog`, `aria-modal:true`, `aria-labelledby` present; `Escape` referenced; no clear `focus-trap` library string)
- **Category**: Component pattern (modal)
- **Description**: At least one dialog implementation sets `role="dialog"` + `aria-modal="true"`. Without source maps, focus cycle, initial focus, and restore-focus on close cannot be fully confirmed. `autoFocus` is used on some fields (login/rename).
- **Impact**: If focus escapes behind the modal, keyboard/AT users can interact with inert background (serious when present).
- **Recommendation**: Use a tested dialog primitive (Radix/Headless UI) with focus trap, Esc, return focus, and `aria-labelledby` pointing at visible title.
- **References**: WCAG 2.1 2.1.2 No Keyboard Trap (A), 2.4.3 Focus Order (A), WAI-ARIA APG Dialog

### [MEDIUM] Countdown and muted OAuth helper text fail or barely pass contrast
- **File**: Claude/Codex templates (`.countdown` / `.footer` `#9ca3af`); Kiro `.expires { color:#999 }`
- **Category**: Contrast
- **Description**: `#9ca3af` on white ≈ **2.54:1**; `#999` on white ≈ **2.85:1** — both FAIL AA for normal text (countdown is meaningful content).
- **Impact**: Users may miss auto-close timing text.
- **Recommendation**: Use ≥ `#595959` (~7:1) or `#667085` carefully verified.
- **References**: WCAG 2.1 1.4.3 (AA)

## Low Findings

### [LOW] Simple OAuth callback pages lack landmarks and viewport meta
- **File**: `internal/api/server.go` L62; Kiro minimal success/fail HTML in `protocol_handler.go` / `sso_oidc.go` / `social_auth.go`
- **Category**: Semantic structure / Mobile
- **Description**: Minimal pages omit `viewport` meta, `main`, and sometimes even charset consistency (`text/html` without charset in older Kiro paths).
- **Impact**: Minor; pages are short single-purpose messages.
- **Recommendation**: Standardize a tiny accessible layout partial: `lang`, charset, viewport, one `main`, one `h1`.
- **References**: WCAG 2.1 1.3.1 (A); mobile best practice

### [LOW] Decorative success icons not hidden from assistive tech
- **File**: `internal/auth/claude/html_templates.go` L159; `codex/html_templates.go` equivalent; Kiro emoji icons
- **Category**: Name, Role, Value
- **Description**: `✓` / emoji icons sit beside textual headings without `aria-hidden="true"`.
- **Impact**: Redundant announcements (“check mark Authentication Successful”).
- **Recommendation**: `aria-hidden="true"` on decorative glyphs.
- **References**: WCAG 2.1 1.1.1 (A)

### [LOW] Empty `alt=""` on management card icons (acceptable if decorative)
- **File**: `static/management.html` (multiple `alt:``` on card title icons)
- **Category**: Images
- **Description**: Provider card icons use empty alt next to visible text titles — correct for decorative images **if** adjacent text always present.
- **Impact**: Low risk; becomes a defect if an icon-only control reuses the same pattern without a name (some toggles pair empty alt with adjacent text — verify icon-only buttons always have `aria-label`, which several do).
- **Recommendation**: Keep empty alt only when adjacent text exists; otherwise accessible name required.
- **References**: WCAG 2.1 1.1.1 (A)

### [LOW] Claude/Codex primary close control is `<button>` without explicit `type="button"`
- **File**: `internal/auth/claude/html_templates.go` (~actions block)
- **Category**: Robust
- **Description**: Outside a `<form>` this defaults safely, but is fragile if markup is later wrapped in a form.
- **Recommendation**: Always set `type="button"` for non-submit buttons.
- **References**: HTML parsing robustness; related 4.1.2

### [LOW] Management SPA title/language not updated in `<title>` on route change (likely)
- **File**: `static/management.html` (static `<title>` only in shell)
- **Category**: Page titles
- **Description**: Shell title is constant “CLI Proxy API Management Center”. SPA route-level title updates were not positively identified in minified code.
- **Impact**: Browser history and SR page-title announcements may not reflect current section (Auth Files vs Usage vs Quota).
- **Recommendation**: Update `document.title` per route with i18n section names (2.4.2).
- **References**: WCAG 2.1 2.4.2 Page Titled (A)

## Positive Observations (not findings)
- Management shell declares `lang="zh-CN"`, charset UTF-8, responsive viewport.
- Login field pattern includes visible label prop, password reveal control with `aria-label`, and `autoFocus` on the key field.
- Dialog wiring includes `role="dialog"`, `aria-modal`, `aria-labelledby`.
- Some status/progress UI uses `role="status"` + `aria-live="polite"`.
- Pagination controls expose dedicated aria labels (`pagination_prev/next/nav_aria`).
- `aria-invalid` + `aria-describedby` appear on validated inputs.
- Claude/Codex success pages set `lang="en"`, focus the close button, and support Escape — better than minimal callbacks.
- Kiro select page associates labels via `for`/`id` for Start URL, Region, and Refresh Token textarea.
- Icon-only management actions frequently ship `aria-label` (close, clear search, batch select).

## WCAG Criterion Coverage

| Criterion | Level | Result |
|---|---|---|
| 1.1.1 Non-text Content | A | **FAIL** (charts; decorative emoji noise; JS-empty root) |
| 1.3.1 Info and Relationships | A | **FAIL** (status structure; some form/label risk; landmarks incomplete on OAuth) |
| 1.3.2 Meaningful Sequence | A | **PASS** (no evidence of harmful CSS order traps in static review) |
| 1.3.3 Sensory Characteristics | A | **PASS** (instructions not solely shape-based) |
| 1.3.4 Orientation | AA | **PASS** (no orientation lock found) |
| 1.3.5 Identify Input Purpose | AA | **FAIL / partial** (management key as password-ish; OAuth URL fields lack `autocomplete`) |
| 1.4.1 Use of Color | A | **FAIL (n=1 group)** (status borders + color-heavy badges; mitigated by text in many places) |
| 1.4.3 Contrast (Minimum) | AA | **FAIL (n≥5)** (tertiary text, primary buttons, countdown greys) |
| 1.4.4 Resize Text | AA | **PASS** (viewport meta; rem/px mix not fully proven but no max-scale lock) |
| 1.4.5 Images of Text | AA | **NOT APPLICABLE** (no essential text-in-image logos required) |
| 1.4.10 Reflow | AA | **PASS / limited** (responsive CSS present; not runtime-tested at 320px) |
| 1.4.11 Non-text Contrast | AA | **FAIL** (focus rings / primary UI greys likely &lt;3:1 in places) |
| 1.4.12 Text Spacing | AA | **PASS** (no !important fixed clipping found for spacing overrides) |
| 1.4.13 Content on Hover or Focus | AA | **NOT FULLY TESTED** (tooltips in minified SPA) |
| 2.1.1 Keyboard | A | **FAIL / partial** (custom widgets mostly button-based; focus gaps on OAuth CSS; charts uncertain) |
| 2.1.2 No Keyboard Trap | A | **NOT FULLY TESTED** (dialogs) |
| 2.1.4 Character Key Shortcuts | A | **NOT APPLICABLE** (no single-key shortcuts identified) |
| 2.2.1 Timing Adjustable | A | **FAIL** (auto-close OAuth windows) |
| 2.2.2 Pause, Stop, Hide | A | **PASS / limited** (polling not decorative motion; animations short) |
| 2.3.1 Three Flashes | A | **PASS** (no flashing content found) |
| 2.4.1 Bypass Blocks | A | **FAIL** (no skip link on management SPA) |
| 2.4.2 Page Titled | A | **PASS / weak** (pages have titles; SPA may not update per view) |
| 2.4.3 Focus Order | A | **NOT FULLY TESTED** |
| 2.4.4 Link Purpose (In Context) | A | **PASS / weak** (`target=_blank` links lack “new window” note) |
| 2.4.5 Multiple Ways | AA | **NOT APPLICABLE** (small set of OAuth pages; SPA has nav) |
| 2.4.6 Headings and Labels | AA | **PASS / partial** (h1 present on OAuth; management uses h1–h4 in JS) |
| 2.4.7 Focus Visible | AA | **FAIL** (outline removed; incomplete replacements; Kiro none) |
| 2.5.1 Pointer Gestures | A | **PASS** (no multipoint-only gestures found) |
| 2.5.2 Pointer Cancellation | A | **PASS** (standard click handlers) |
| 2.5.3 Label in Name | A | **PASS / limited** (visible labels generally match) |
| 2.5.4 Motion Actuation | A | **NOT APPLICABLE** |
| 3.1.1 Language of Page | A | **FAIL** (many OAuth pages missing `lang`) |
| 3.1.2 Language of Parts | AA | **PASS / limited** (i18n system present) |
| 3.2.1 On Focus | A | **PASS** (no evidence of focus-triggered context change) |
| 3.2.2 On Input | A | **PASS / limited** |
| 3.2.3 Consistent Navigation | AA | **PASS** (SPA chrome) |
| 3.2.4 Consistent Identification | AA | **PASS** |
| 3.3.1 Error Identification | A | **PASS / partial** (`aria-invalid` present on some forms) |
| 3.3.2 Labels or Instructions | A | **PASS / partial** (login good; placeholders heavy elsewhere) |
| 3.3.3 Error Suggestion | AA | **NOT FULLY TESTED** |
| 3.3.4 Error Prevention (Legal/Financial) | AA | **NOT APPLICABLE** (admin config; destructive actions should confirm — not fully verified) |
| 4.1.1 Parsing | A | **PASS** (obsolete as WCAG 2.2 criterion; HTML generally well-formed) |
| 4.1.2 Name, Role, Value | A | **FAIL / partial** (good ARIA in places; charts/status gaps) |
| 4.1.3 Status Messages | AA | **FAIL** (OAuth polling status without live regions; SPA partial pass) |

## Accessibility Quick Wins
1. **Add `lang` + `role="status" aria-live="polite"`** to all OAuth waiting/success HTML templates (highest impact, tiny diffs in Go strings).
2. **Stop auto-closing** callback windows by default, or add “Stay open” that clears the timer.
3. **Darken** `--text-tertiary`, `--primary-color`, and button foreground/background pairs until ≥4.5:1 (management theme tokens).
4. **Restore focus indicators** on Kiro inputs/buttons; ban bare `outline:none`.
5. **Skip link** + route focus to `main`/`h1` in the management panel source repo.
6. **Pin and a11y-test** the remote `management.html` release in CI (axe-core / Lighthouse CI) — panel lives at `Cli-Proxy-API-Management-Center`.
7. Provide **chart data tables** or summaries beside canvas charts.
8. Mark decorative emoji `aria-hidden="true"`; put plain language first in status text.

## Files Reviewed
| Path | Role |
|---|---|
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/static/management.html` | Management SPA (primary UI) |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/managementasset/updater.go` | Remote asset sync / serve path resolution |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/api/server.go` | `/management.html` route; shared OAuth success HTML; callbacks |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/auth/claude/html_templates.go` | Claude OAuth success page |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/auth/codex/html_templates.go` | Codex OAuth success page |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/auth/codearts/oauth_web.go` | CodeArts login/waiting/success HTML |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/auth/joycode/oauth_web.go` | JoyCode login/waiting/success HTML |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/auth/kiro/oauth_web_templates.go` | Kiro start/select/error/success templates |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/auth/kiro/protocol_handler.go` | Minimal Kiro success/fail HTML |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/auth/kiro/sso_oidc.go` | Minimal SSO HTML |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/auth/kiro/social_auth.go` | Minimal social auth HTML |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/api/handlers/management/auth_oauth_infra.go` | Qoder login HTML snippet |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/sdk/auth/qoder.go` | SDK Qoder HTML snippets |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/sdk/auth/codearts.go` | SDK CodeArts HTML snippet |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/sdk/auth/joycode.go` | SDK JoyCode HTML snippet |
| `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus/internal/usage/keeper/api/router.go` | Usage Keeper HTML serving (asset missing in-repo) |

**HTML inventory note:** The only standalone `.html` file outside `.git`/`CLIProxyAPI` is `static/management.html`. All other user-facing HTML is embedded in Go string constants.

## Methodology Notes
- **Read-only static audit** of source and the checked-in management bundle. No browser, screen reader, or axe runtime was executed in this pass.
- **Contrast** values were computed from declared hex tokens using relative luminance (sRGB) formulas aligned with WCAG 2.1.
- **`management.html` is not a 167-line page in functional terms**: line count is low because JS/CSS are minified onto very long lines; effective UI complexity is a full React admin console.
- **Minification limits**: ARIA coverage counts and dialog focus-trap behavior are inferred from string patterns (`role:\`dialog\``, `aria-live`, `htmlFor`, component class names). False negatives/positives are possible without source maps.
- **Remote UI**: Auto-updater may serve a different `management.html` than the audited file; treat findings as applicable to the vendored snapshot and to the panel project generally.
- **Usage Keeper**: Frontend not in repository — explicitly out of static-audit reach.
- **Core product APIs** are headless JSON/WebSocket interfaces and are **not** subject to WCAG UI criteria except where they emit HTML (OAuth callbacks, management panel).
- AAA items (e.g., 1.4.6 Enhanced Contrast, 2.3.3 Animation, 3.2.5 Change on Request) are noted only where low-cost and practical.
