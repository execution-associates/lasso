# LassoDesktop — plan

A native macOS client for a running lasso: a rail on the screen edge for the fleet, a card per agent, and a chat
with a lasso bot (the assistant), with speech-to-text input. This document is the plan; the brief it answers is
the LassoDesktop brief (plan mode, 2026-10-11). The spec the harness asserts against is `mac/Spec/tokens.json`;
`mac/Spec/fixtures/rail-expanded.json` sketches how a named state is produced from the server's own JSON shapes.

## Facts this plan rests on (checked 2026-10-11)

- **Build Mac is minime**: macOS 27.0.1, arm64, Command Line Tools with Swift 6.4, Aqua session logged in
  (uid 501). The CLT ship `Testing.framework`, so `swift test` works through swift-testing.
- **minime's `127.0.0.1:8090` is already bound** by an ssh ControlMaster mux that `jessica-triage.service` on
  titan owns. It reaches titan's prod lasso, but it is not ours and lives and dies with that service.
- **`/api/*` accepts only UI_AUTH basic auth** (`src/main.go:3285`); no route there reads a bearer token.
- **Signing uses a self-signed identity on minime** so TCC grants (microphone, screen recording,
  accessibility) survive rebuilds; ad-hoc signing changes the cdhash every build.
- **No speech-to-text exists anywhere in lasso today**, server or web.


## 0. Decisions that differ from the brief

| Brief says | Plan does | Why |
|---|---|---|
| Optional bearer token for a remote lasso | UI_AUTH basic in the Keychain, plus optional Cloudflare Access service-token headers (`CF-Access-Client-Id`, `CF-Access-Client-Secret`) | `withAuth` reads only `r.BasicAuth()` (`src/main.go:3285-3299`). Making `/api/*` accept MCP bearers would hand a host-scoped MCP credential the whole UI API, which has no `requireHost` checks. |
| Ad-hoc `codesign` | Sign with a self-signed identity in a dedicated keychain on minime. Ad-hoc is only the fallback for a fresh Mac and prints a warning. | TCC keys grants on the designated requirement. An ad-hoc cdhash changes every build. |
| `dist/LassoDesktop.app`, `dist/parity/` | `mac/dist/…` | The repo-root `dist/` is the release workflow's output (`dist/lasso-*`), and it already has a stray 26 MB `dist/lasso-linux-amd64` committed in 53cf9c24. |
| Mac's `127.0.0.1:8090` | The app defaults to `http://127.0.0.1:8090` as the brief says. On minime, the dev script points it at our own tunnel on `127.0.0.1:18090`. | `jessica-triage`'s mux holds 8090 on minime, and binding over it would fail `ExitOnForwardFailure`. |
| Frameworks: SwiftUI, AppKit, Foundation, AVFoundation, CoreGraphics only | The rule is **no third-party code**. Other Apple frameworks are allowed: Combine (`@Published`), Security (Keychain, required by constraint 4), ScreenCaptureKit (`S` attach), ApplicationServices (`AXIsProcessTrusted`), Carbon (`RegisterEventHotKey`, optional summon key). CoreML and Accelerate come later, for on-device ASR. | Constraints 3 and 4 can't be met with the brief's list. |
| One executable target | One executable `LassoDesktop` plus a library `LassoDesktopKit` and a test target | The harness is `LassoDesktop --parity`, the same shipped binary, so parity measures the real code. |

## 1. Scope

| # | Surface | Status | Reason |
|---|---|---|---|
| 1 | Rail, collapsed (`Show lasso controls`, badge) | **now**, M1 | Fixtures only. Live data arrives in M2. |
| 2 | Rail, expanded (inbox, agents capsule with `+N`, tools capsule, ellipsis) | **now**, M1 and M2 | The record tool's glyph renders in M1 but stays disabled until M7. |
| 3 | Card | **now**, M4 | **Discard (`E`) is blocked on S1** (shared dismiss state). **Next steps are partly blocked**: M4 takes them only from open questions (`chatTool.ask`, `pending_ui` options). The API has no source of generated suggestions, so those are *later*. |
| 4 | List panel (machine, repo, agent; pins; context menu) | **now**, M5 | Pins already exist server-side (`agent_pins`). |
| 5 | Chat with the bot | **blocked on S1** | The configured bot must be shared state (`assistant_bot` in ui_state). omp bots are *now* (M6); claude-runtime bots are *later* because they need the pane chat routes. |
| 6 | Onboarding | **now**, M6 | Permissions, host, bot. |
| 7 | Voice | Groq/OpenAI **now** (M7). **lasso engine blocked on S2** (`/api/transcribe`). **Metadata blocked on S3** (`meta` on bot send). Parakeet *later*. | See §4. |

Proposed server additions:
- **S1**: ui_state `assistant_bot` plus card dismissals
- **S2**: `/api/transcribe`
- **S3**: bot-send `meta`

## 2. Token table (spec = `mac/Spec/tokens.json`, code = `Theme.swift`)

Every Theme constant carries one of these provenances:
- `measured` (method): for the brief's values, the method is "upstream reference, method not supplied"
- `unmeasured`
- `inferred` (derivation)

Proposed additions are tagged `proposed`.

### 2.1 Restated from Appendix A

- **Rail (A.1)**
  - group fill width 40 pt; rim 1 pt `#5D5F5F`; radius = width / 2; group gap 8 pt
  - capsule fill `#1E2020`, sampled on the lower half
  - glyphs 11 pt, pitch 18 pt:
    - at-rest/done dot `#57C459`
    - working ring 2 pt `#3877F4`
    - needs-you dot `#AB362F`
    - idle dot `#828186`
  - padding above the first glyph 8.5 pt
  - tools capsule: 3 glyphs, no spacing
- **Card (A.2)**
  - 500×500 pt, radius 14 pt, surface `#110F0D`, footer `#0F0F0C`
  - keycap `#333130`, legend 10 pt semibold
  - text `#F1EEED` / `#949290`, accent `#3877F4`, danger `#EB6E62` on `#23211F`
  - row hover 6 % white, selection 12 % white (both **unmeasured**)
  - row radius 8 pt, padding 6 pt vertical × 8 pt horizontal
  - fonts: name 13 pt medium, second line 11 pt, header 12 pt, tree 12 pt, terminal 12 pt monospaced
  - unread segment 4×16 pt `#3877F4`
- **Motion (A.4)**
  - rail spring: response 0.2, damping 0.9
  - card open/close: easeOut 0.07 s
  - toast: about 183 ms, decelerating
  - paging: easeInOut 0.2 s, **unmeasured**

### 2.2 Proposed tokens (the brief lacks these)

- **Rail placement**: trailing screen edge, inset 8 pt from `visibleFrame`, vertically centred (inferred).
- **Group outer size**: 42 pt (40 + 2 × 1 rim, inferred; see §2.3-1).
- **Ellipsis group**: a 42 pt circle like the inbox.
- **Tools capsule cells**: 40×40 pt each, so the fill is 120 pt tall (inferred from "filling the width").
- **Bottom padding**: 8.5 pt, mirroring the top (inferred).
- **Agents capsule size**: at most 5 slots. When there are more agents, the last slot shows `+N`.
  - Derivation (inferred): 42 + 8 + (8.5 + 11 + 4·18 + 8.5 + 2) + 8 + 122 + 8 + 42 = **332 pt**, which matches the brief's "~330 pt of travel".
- **`+N` text**: 9 pt semibold `#949290`, in an 11 pt-tall cell.
- **Badge**: 16 pt circle, `#AB362F` fill, 10 pt semibold `#F1EEED` count, offset to the top-leading corner of the inbox.
- **Card placement**: leading side of the rail, 8 pt gap, top-aligned to the picked glyph and clamped to `visibleFrame`.
- **Card internals**:
  - header 36 pt
  - footer 44 pt
  - composer field 32 pt, fill white 6 %, radius 8
  - keycap minimum 18×18 pt, radius 4, padding 4 pt horizontal
  - tool row 24 pt with radius 6, fill white 4 %
  - machine chip: radius 4, fill `#333130`, 11 pt
- **Unread bar**: 4 pt gap between segments; read segments are white 12 %.
- **List panel**: 360×560 pt, radius 14, same surface; group headers 11 pt semibold `#949290`, uppercase off.
- **Focus ring**: 2 pt `#3877F4`, outset 2 pt (accessibility).
- **Recording**: ring `#EB6E62`, level meter fill `#F1EEED` at 60 %, meter refresh 30 Hz. **Open question Q4.**
- **Toast**: timing curve (0, 0, 0.58, 1), 0.183 s (inferred from "decelerating, no overshoot").
- **Voice VAD**:
  - noise-floor window 300 ms
  - speech threshold floor + 12 dB
  - end of turn after 900 ms of silence, following at least 400 ms of speech
  - maximum turn 120 s
  - push-to-talk tap/hold split at 250 ms
- **Live data**:
  - poll floor 5 s for `/api/all-panes`, matching the web client (`src/web/src/lib/agents.ts:337`)
  - bots 3 s; chat 2 s while open
  - SSE watchdog 60 s (the server pings every 25 s, `main.go:2847`)
  - reconnect backoff 1 s → 30 s with ±20 % jitter

### 2.3 Tokens believed wrong or ambiguous

1. **"40 pt plus a 1 pt rim"** could mean outer 42 or outer 40. The plan takes 40 as the fill and 42 as the outer size; the 332 pt sum above supports this. The harness measures the fill diameter.
2. **"Rail collapse: same spring (~133 ms)"** cannot be true. With response 0.2 and damping 0.9, settle time depends only on travel and threshold. Settling to 0.5 pt on 330 pt takes about 230 ms whichever direction it moves. The spring parameters are treated as normative, and duration is defined as "time to settle within 1 px". 133 ms is recorded as unmeasured and inconsistent.
   - The overshoot does check out: e^(−ζπ/√(1−ζ²)) × 330 = 0.00152 × 330 = **0.50 pt**.
3. **The state red conflicts.** A.1 needs-you is `#AB362F`, A.2 danger is `#EB6E62`, and A.5 says one hue per state. The plan uses `#AB362F` for the blocked/failed *state* everywhere, and `#EB6E62` only for *destructive actions* (Kill).
4. **`failed` does not exist in the fleet payload.** `agent_status` is working, blocked, done, idle or unknown (`src/hostpanes.go:263`). `failed` is used only for a bot `error`. `unknown` maps to idle grey.
5. **`↳` / `→` keycaps are ambiguous.** The proposal: `→` takes the highlighted step (sends it), `↳` (bound to Tab) puts it into the composer to edit, `1`–`9` highlight a step. **Open question Q1.**
6. **The tolerances are coarse in places.**
   - ±1 px overshoot equals the whole spec value (0.5 pt = 1 px), so it passes anything up to 2 px.
   - ±10 ms on a 70 ms ease is finer than one 60 Hz frame (16.7 ms), so it can't be screenshot-measured. §5.4 measures it another way.

## 3. API integration table

All routes are behind the UI_AUTH tier: open when `UI_AUTH` is unset, basic auth otherwise, and optionally the Cloudflare Access header gate in front (`src/accessgate.go:80`). Every host-scoped call passes `?host=<pane's host>`. Errors are plain-text bodies (`http.Error`).

| Use | Method, path | Request | Response | Source |
|---|---|---|---|---|
| Host check, onboarding | GET `/api/version` | – | `{lasso_protocol, lasso_version, herdr_protocol, herdr_version, compatible, …}` | `src/main.go:1863` |
| Machines | GET `/api/hosts[?refresh=1]` | – | `{active, local{version,protocol,hostname,user}, hosts:[{alias,hostname,reachable,running,compatible,…}], probing}` | `src/hosts.go:704`, struct `:43`/`:72` |
| Initial frame | GET `/api/active` | `?host=` | `Active` | `src/main.go:298`, struct `:1069` |
| Live stream | GET `/api/events?host=local&client=<uuid>` | – | SSE. `active` frames carry `panes_rev`, `ui_state_rev`, `plugins_rev`, …; one-shot `bot-chat {bot}` and `notice`; `ping` every 25 s | `src/main.go:2802`, ping `:2847`, `src/botomp.go:953` |
| Fleet | GET `/api/all-panes` | – | `{panes:[hostPane], errors:{host:msg}}`. hostPane tags: `host, host_label, pane_id, workspace_id, workspace_label, tab_id, tab_label, pane_label, terminal_title, cwd, agent, agent_status, has_agent, focused, transcript_at, repo, repo_key, repo_label, prompt, agent_id, closed` | `src/hostpanes.go:923`, struct `:263` |
| Pins, shared state | GET/POST `/api/ui-state` | POST patch, e.g. `{client_id, agent_pins:{"<host>\u0000<pane_id>":true}}` | `uiState` incl. `pinned_agents` (≤64) | `src/hostpanes.go:430`, writer `:383`, merge `:655`, struct `src/db.go:276` |
| Focus in herdr (list menu only) | POST `/api/focus?host=` | `{pane_id}` | `{ok, reattach}` | `src/main.go:1352` |
| Kill agent (`X`) | POST `/api/agent/close` | `{pane_id, agent_id, host, remove_worktree:false}` | `{agent_killed, pane_closed, removed_worktree}` | `src/closeme.go:34` |
| End a pane without an agent record | POST `/api/close?host=` | `{pane_ids:[…]}` | `{closed, errors}` | `src/main.go:1555` |
| Card conversation | GET `/api/chat?pane=&host=&before=` | – | `chatPayload{pane_id, agent, host, title, model, path, start_offset, items[chatItem], tokens, running, more, unavailable, pending_ui}` | `src/chatview.go:1823`, struct `:190`, item `:64`, tool `:95` |
| Card send (`⏎`) | POST `/api/chat/send?host=` | `{pane_id, text}` | `{outcome: confirmed\|refused\|uncertain, detail}`. Can take about 8 s; never auto-retry `uncertain` | `src/chatview.go:2091` |
| Answer a question | POST `/api/chat/answer?host=` | `{pane_id, expect, labels, answers:[{selected,multi,options}]}` | `{outcome: sent\|refused}` | `src/chatview.go:2323` |
| Stop | POST `/api/chat/stop?host=` | `{pane_id}` | `{outcome}` | `src/chatview.go:2128` |
| Screenshot attach (`S`) | POST `/api/paste-file?host=&name=` | raw PNG body, `Content-Type: image/png` | `{path}`, which the client puts into the message text | `src/main.go:1956` |
| Bots list | GET `/api/bots` | – | `{bots:[botView]}`: `botRecord` + `state, waiting_for, last_text, last_kind, last_at, live_model, error, …` | `src/botsapi.go:47`, `src/botruntime.go:215` |
| Create assistant bot | POST `/api/bots` | `{name, runtime:"omp", start:true, instructions}` (host defaults to local; omp must be local) | `{bot, note?, error?}`; **a 200 can still carry `error`**; 409 on a duplicate | `src/botsapi.go:152`, rules `src/bots.go:178` |
| Bot detail | GET `/api/bots/<name>` | – | `{bot, dir_path, …}` | `src/botsapi.go:233` |
| Start bot | POST `/api/bots/<name>/start` | `{}` | `{bot}`; 409 if already running | `src/botsapi.go:338` |
| Bot chat | GET `/api/bots/<name>/chat[?before=]` | – | `chatPayload` + `pending_ui[{id,method,title,message,options}]`; 400 for a claude bot | `src/botsapi.go:578`, `src/botomp.go:895` |
| Bot send / stop / answer | POST `/api/bots/<name>/chat/{send,stop,answer}` | `{text}` / – / `{id, value?, confirmed?, cancelled?}` | `{outcome, detail?}` | `src/botsapi.go:597-623` |
| Bot avatar | GET `/api/bots/<name>/avatar` | – | image | `src/botavatar.go:70` |

### Proposed additions

**S1: shared desktop state in ui_state** (`src/db.go:276`, writer `src/hostpanes.go:383`)
- `assistant_bot`: a string, replaced whole. It is validated against the bot-name regex (`src/bots.go:129`), and `""` clears it.
- `dismissed_cards`: `{paneKey: transcript_at}`, changed only through a write-only `card_dismiss: {paneKey: transcript_at|null}`, the same pattern as `agent_pins`.
  - A card counts as dismissed until that pane's `transcript_at` moves past the stored value.
  - Capped at 256 entries; the oldest is dropped.
- Both bump `ui_state_rev`. The web client can ignore them.
- The app detects support by checking whether the key is present in GET.
- Tests go in `src/hostpanes_test.go`.

**S2: `/api/transcribe`**, designed in §4.3.

**S3: bot-send `meta`**
- `POST /api/bots/<name>/chat/send {text, meta:{input:"voice", engine, model}}`. `meta` is ≤1 KB with string values only.
- lasso stores `(bot, session_path, sha256(text), sent_at, meta)` in a new table `bot_message_meta`, keeping the last 500 per bot.
- `botOmpChat` annotates the first `user` item at or after `sent_at` whose text hash matches, as `chatItem.meta`.
- This is needed because omp's session file is the chat's source of truth (`src/botomp.go:641`) and has nowhere to hold metadata.
- Until S3 lands, the app shows the engine only on its own optimistic row.

**Drift guard**: a Go test writes golden JSON for `hostPane`, `botView`, `chatPayload` and `uiState` to `mac/Spec/contract/`, and Swift tests decode those goldens. Both sides then fail when a tag changes.

**Not proposed**
- Bearer auth on `/api/*` (see §0).
- Native push: Web Push is VAPID-only (`src/webpush.go`). The app polls and uses local notifications, *later*.

## 4. Voice plan

### 4.1 Capture (client, every engine)

- An `AVAudioEngine` input tap at the hardware format. `AVAudioConverter` turns it into **16 kHz mono Float32**, held in an in-memory buffer capped at 120 s.
- **Level meter**: RMS per 20 ms, mapped −60…0 dBFS to 0…1, published at 30 Hz.
- **End of speech**: an energy VAD using the tokens in §2.2.
- **`V` push-to-talk**:
  - Hold `V` to record; release to transcribe.
  - A tap (<250 ms) records hands-free until the VAD ends the turn.
  - `V` works when LassoDesktop is key. A global push-to-talk is optional and is the only thing the Accessibility permission gates.
- **Microphone permission**: `AVCaptureDevice.authorizationStatus(for: .audio)`. The request happens only from onboarding or the first `V`. Denied means the record glyph is disabled with a label explaining why. `NSMicrophoneUsageDescription` goes in `Info.plist`.
- **Encoding**: WAV PCM16, 16 kHz mono, in memory only (32 kB/s, so 120 s ≈ 3.84 MB). Nothing is written to disk.
- **The transcript goes to the bot as a normal user message**: `POST /api/bots/<name>/chat/send` with `meta` once S3 lands.
  - The engine shows on the row and in the composer's recording state.
  - The rail's record glyph turns into the recording ring while capturing.

### 4.2 Engines (one `Transcriber` protocol: `func transcribe(_ wav: Data, language: String?) async throws -> Transcript`)

| Engine | Audio path | Client side | Server side | Credential |
|---|---|---|---|---|
| **lasso** (default) | Mac → tunnel/TLS → lasso → configured provider, or lasso-local | POST the WAV | S2 picks the provider | none in the app |
| **Groq** | Mac → `api.groq.com/openai/v1/audio/transcriptions` (multipart; model `whisper-large-v3-turbo`, configurable) | all | none | Keychain `groq` |
| **OpenAI** | Mac → `api.openai.com/v1/audio/transcriptions` (`gpt-4o-mini-transcribe` default, `whisper-1` option) | all | none | Keychain `openai` |
| **Parakeet** (*later*) | Never leaves the Mac | CoreML encoder, Accelerate mel front end, TDT greedy decoder, vocab, all written in-house; weights (~0.6 B params) downloaded once with consent into Application Support, checksum-pinned | none | none |

**Keys**
- Keychain generic passwords under service `com.orangecountyai.LassoDesktop`, accounts `groq`, `openai`, `ui-auth`, `cf-access-id`, `cf-access-secret`.
- A key is read only at the moment of use, never written to UserDefaults, and settings show only "set ✓".

**Logging**
- `os.Logger` with `.private` interpolation.
- A swift-testing test fails the build if `print(` appears in `Sources/`, or if any `Logger` call interpolates a value of type `Transcript`, `Data` or `Secret`.
- No audio, transcript or header is ever logged.

### 4.3 The lasso engine: `/api/transcribe` (S2)

**`POST /api/transcribe[?language=xx&prompt=…]`**
- Body: raw `audio/wav`, PCM16, mono or stereo, 8–48 kHz.
- Limits:
  - `MaxBytesReader` at **4 MiB**
  - WAV-declared duration **≤120 s** (413 otherwise)
  - empty body: 400
  - not WAV: 415
  - at most **2 in flight** (429 beyond that)
  - upstream timeout **60 s** (504)
- 200 response: `{"text","engine":"lasso","provider","model","language","audio_ms","elapsed_ms"}`.

**`GET /api/transcribe`** returns `{available, provider, model, max_seconds:120, max_bytes:4194304, formats:["audio/wav"], reason?}` for onboarding.

**Auth tier**: UI_AUTH, like every `/api/*` route, behind `withAuthExcept`. It is not on `/mcp`, since audio in tool arguments is a poor fit.

**Provider choice** (settings key `transcribe`):
- Shape: `{provider: off|groq|openai|openai-compatible, model, base_url?, key_name?}`.
- `openai-compatible` with a loopback or tailnet `base_url` is the **provider that never leaves the server**, for example a whisper.cpp or Parakeet server run by isb on titan.
- The key comes from the env var `key_name`, else `secret <key_name>` with a 10 s timeout. That reuses `pluginSecretLookup` (`src/pluginmcp.go:110`), factored out as a shared `lookupSecret`.
- The key is cached in memory for 10 min and never stored in `lasso.db`.
- The default is `off`, which reads as `available:false` with a reason.
- Exposed as a `transcribe` section through the settings handlers (`src/mcp_settings.go`), key *names* only.

**Privacy**: the body stays in memory and is streamed upstream through an `io.Pipe` multipart. The transcript and the audio are never logged and never written.

**Files**
- New: `src/transcribe.go`, `src/transcribe_test.go` (an httptest fake provider, all limits), `docs/design/transcription.md`
- Updated: `docs/reference/http-routes.md` (one row), `CLAUDE.md` (one line)

**Core route vs plugin, costed**

| | Core route (recommended) | Plugin |
|---|---|---|
| Code | ~300 lines of Go + ~200 test, about 1 day | ~250 lines of plugin + manifest + packaging, plus a base64 path in the client, about 1–1.5 days |
| Transport | raw WAV, 4 MiB | base64 JSON through `/api/plugins/<name>/call`. The body is silently truncated at 4 MiB (`src/plugins.go:1184`), so about 3 MiB of audio, roughly 95 s, and 33 % overhead |
| Server-local provider | yes (loopback `base_url`) | **no**: isb egress refuses IP literals (`checkPluginHost`), so a sandbox cannot reach a titan-loopback ASR server |
| Ops | none | needs a human to enable and approve it, `isb serve` up, a sandbox running |
| Off-label use | none | `/call` is built for a tab iframe and trusts the name in the path; a native client calling it is off-label |
| Reuse | the web client can add a mic button against the same route | the same, through the bridge only |

## 5. Harness design

### 5.1 Producing states deterministically

- `LassoDesktop --parity <state|all> --out <dir>` runs the shipped binary in parity mode.
- **`FixtureSource`** implements the same `LassoAPI` protocol as `LiveSource`:
  - It decodes `mac/Spec/fixtures/<state>.json`, in the server's JSON shapes, through the same `Codable` models.
  - Every action is a recorded no-op, so the harness *cannot* cause a side effect.
- Also fixed:
  - **`FixedClock`** (the fixture's `now`), so "Finished 3m ago" is stable.
  - **`FixturePermissions`** (each permission's state).
  - Recording state comes from a fixture level trace.
- **Live data is never used for parity.**
- The seven states are: rail-collapsed, rail-expanded, card-open, list-panel-open, chat-open, recording, onboarding.

### 5.2 Capture

- Each state is mounted in a borderless `NSWindow` with `colorSpace = .sRGB`.
- It is rendered with `cacheDisplay(in:to:)` into an `NSBitmapImageRep` whose pixel size is 2× its point size. That gives **2× regardless of minime's display scale**.
- The fallback is SwiftUI `ImageRenderer(scale: 2)` for subtrees AppKit cannot cache.
- The result is converted to sRGB and written as `mac/dist/parity/<state>.png`.
- Parity runs inside the Aqua session through a one-shot LaunchAgent (§7). It writes `report.json`, `report.txt` and the PNGs, and the dev script pulls them back to titan's `mac/dist/parity/`.

### 5.3 Comparison method

- **Anchors**: views carry `.parityAnchor("rail.agents.capsule")`, a preference key that is a no-op outside parity mode. It reports frames in window coordinates.
- **Geometry**:
  - Anchor frames give layout truth.
  - A pixel edge scan along a line confirms rims, glyph diameters and the card size, catching strokes drawn outside a frame.
- **Colour**: the median of a named patch region (for example `lower-half-inset-4` of the capsule, glyph centre 5×5 px), compared with **CIEDE2000** against the token.
- **Static**: every `Theme` value is compared with `tokens.json` (drift between code and spec).
- **Accessibility**:
  - A walk of the window's accessibility tree checks that every button, text field and row has a non-empty label.
  - Each rendered keycap is a `KeyBinding` value from a single registry, not a string. The check asserts each one is registered in the active key handler for that state, so a legend and its binding cannot disagree.

### 5.4 Motion

1. **Static**: Theme animation parameters equal the spec exactly.
2. **Analytic**:
   - Springs are sampled from SwiftUI `Spring(response:dampingRatio:)` at 1 ms over the spec travel. That gives overshoot and settle time (within 1 px).
   - Curves have exact durations.
3. **Probe**:
   - An `AnimatableModifier` on each animated value records `(CACurrentMediaTime, value)` every time SwiftUI interpolates. Those are the real per-frame values, with no screen capture.
   - The samples are fit to the spring or curve model. The check asserts the fit matches the spec, and that the fitted settle time and overshoot are within tolerance.
   - Probe timing at 60 Hz can't resolve ±10 ms directly. The gate is the fit residual (≤1 px), and duration comes from the fitted model.

### 5.5 Tolerances

| Check | Tolerance |
|---|---|
| Colour (CIEDE2000) on named patches | ≤ 2.0 |
| Geometry (anchor frame and pixel edge) | ±1 pt |
| Animation duration (fitted/analytic) | ±10 ms |
| Spring overshoot | ±1 px |
| Probe fit residual | ≤ 1 px |
| Accessibility labels, keycap/binding agreement | exact |

`parity-check.sh` prints a table of `state | token | spec | measured | Δ | pass` and exits non-zero on any failure.

## 6. Milestones (each ends with `mac/Scripts/dev.sh parity` exiting 0)

**M1: Rail on minime, from fixtures (visible first)**
- Probes first:
  - compile `@State` and `@Observable` with Swift 6.4 CLT and record the result; fall back to `ObservableObject`
  - minime's display scale
- Set up:
  - signing identity (§7.2)
  - tunnel unit (§7.1)
  - Package, `Theme.swift`, `tokens.json`
- Build:
  - the rail as a non-activating `NSPanel` (all Spaces, full-screen auxiliary)
  - collapsed and expanded states with the spring
  - keyboard navigation and labels
- Scripts: `build-app.sh`, the LaunchAgent, `dev.sh run`, `parity-check.sh` for rail-collapsed and rail-expanded.
- **Accept**:
  - `build-app.sh release` passes on a clean tree on minime
  - the rail is on minime's screen
  - parity is green for two states

**M2: Live fleet, read-only**
- Client side:
  - `LassoClient` with Keychain auth
  - the SSE parser over `URLSession.bytes`, with the watchdog and backoff
  - a 5 s poll floor
  - `/api/all-panes`, `/api/hosts`, `/api/ui-state`
  - the rail's live glyphs and badge (blocked agents plus blocked bots)
- The contract goldens test.
- **Accept**:
  - `swift test`
  - `dev.sh smoke`, GET-only against live lasso
  - parity green

**M3: Server additions S1–S3**
- Go, on this branch, with `mise run test`.
- Verified against a dev lasso on titan `127.0.0.1:8190`, tunnelled to minime `:18190`.
- Releasing to prod is a `/wrap` the user calls.
- **Accept**: Go tests, a `curl` check of `GET /api/transcribe` on the dev lasso, parity green.

**M4: Card**
- Chat read and paging, one-line tool rows, the status footer.
- The composer: text send, `S` screenshot through ScreenCaptureKit and `/api/paste-file`.
- Answering questions, next steps from asks.
- `J`/`K` with the unread bar.
- `X` with a confirm step.
- `E` (S1).
- `esc` blurs the composer first, then closes.
- Single-letter keys are active only while the composer is not focused.
- **Accept**: parity card-open plus the open/close/paging motion.

**M5: List panel**
- Grouped machine, repo, agent.
- Pins via `agent_pins`.
- Context menu: pin, focus, end (with a confirm).
- **Accept**: parity list-panel-open.

**M6: Chat and onboarding**
- Ensure the bot:
  - list bots
  - use `assistant_bot`
  - otherwise offer to pick an existing bot or create `assistant` (omp, start), setting `assistant_bot`
- omp chat with `pending_ui` answers.
- Onboarding: the three permission states, the host (`/api/version`, `/api/hosts`), and the bot.
- **Accept**: parity chat-open and onboarding.

**M7: Voice**
- Capture, meter, VAD, `V`.
- The lasso, Groq and OpenAI engines.
- Keychain settings.
- `meta`.
- The recording state in the rail and the composer.
- The no-log test.
- **Accept**: parity recording, and a manual transcript round-trip on minime with the user present.

**Later**
- Parakeet, or Apple's SpeechAnalyzer (Q3)
- claude-runtime bots
- local notifications
- generated next steps

## 7. Environment recipe (from titan, repo root)

### 7.1 Tunnel (owned, systemd user unit on titan)

`mac/Scripts/lasso-desktop-tunnel.service`, modelled on `minime-socks.service`:

```ini
[Service]
ExecStart=/usr/bin/ssh -F /dev/null -N \
  -R 127.0.0.1:18090:127.0.0.1:8090 -R 127.0.0.1:18190:127.0.0.1:8190 \
  -i %h/.ssh/id_ed25519 -o UserKnownHostsFile=%h/.ssh/known_hosts \
  -o ControlMaster=no -o ControlPath=none -o BatchMode=yes \
  -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  stephanfitzpatrick@minime.tail9dd8e.ts.net
Restart=always
RestartSec=5
```

Install once with `systemctl --user link $PWD/mac/Scripts/lasso-desktop-tunnel.service && systemctl --user enable --now lasso-desktop-tunnel`. This changes titan, so do it only with the user's OK.

Check it with `ssh minime curl -s 127.0.0.1:18090/api/version`.

### 7.2 Signing identity (once, on minime)

`mac/Scripts/make-signing-identity.sh`:
1. `openssl req -x509` with `extendedKeyUsage=codeSigning`, CN `LassoDesktop Dev (minime)`, 10 years.
2. Create `~/Library/Keychains/lasso-desktop-signing.keychain-db`. Its password is in 1Password and fed on stdin from titan's `secret LASSO_DESKTOP_SIGNING_KEYCHAIN`.
3. `security import … -T /usr/bin/codesign`, then `set-key-partition-list -S apple-tool:,apple:`.
4. Disable auto-lock.
5. Only if `codesign` refuses an untrusted identity: `security add-trusted-cert -p codeSign` once. That needs a click on minime's screen.

The key carries this app's TCC grants, so it never leaves that keychain.

### 7.3 Build, deploy, launch

```sh
mac/Scripts/dev.sh doctor     # swift version, display scale, tunnel, identity, agent loaded
mac/Scripts/dev.sh run        # sync → build → install → relaunch
#   sync:     rsync -a --delete --exclude .build --exclude dist mac/ minime:src/lasso-desktop/mac/
#   build:    ssh minime 'cd ~/src/lasso-desktop/mac && Scripts/build-app.sh release'
#             (swift build -c release --arch arm64 → mac/dist/LassoDesktop.app,
#              Info.plist, codesign -s "LassoDesktop Dev (minime)" --keychain …)
#   install:  ditto mac/dist/LassoDesktop.app ~/Applications/LassoDesktop.app
#             defaults write com.orangecountyai.LassoDesktop baseURL http://127.0.0.1:18090
#             (first time) launchctl bootstrap gui/501 ~/Library/LaunchAgents/com.orangecountyai.LassoDesktop.plist
#   relaunch: launchctl kickstart -k gui/501/com.orangecountyai.LassoDesktop
mac/Scripts/dev.sh parity [states]   # sync → build → kickstart the …LassoDesktop.parity agent →
                                     # wait for report.json (timeout 120 s) → rsync results back → exit with its code
mac/Scripts/dev.sh logs       # tail ~/Library/Logs/LassoDesktop.log (never holds secrets or transcripts)
```

**LaunchAgent plist**
- `ProgramArguments` = the bundle's `Contents/MacOS/LassoDesktop`
- `LimitLoadToSessionType` = `Aqua`
- `RunAtLoad` = true
- `KeepAlive` = `{SuccessfulExit: false}`
- `ProcessType` = `Interactive`

The parity agent is the same binary with `--parity all --out …`, with `RunAtLoad` false and no KeepAlive.

**`Info.plist`**
- `LSMinimumSystemVersion` 14.0
- `LSUIElement` true (Q2)
- `NSMicrophoneUsageDescription`
- `NSAppTransportSecurity.NSAllowsLocalNetworking`

**Clean-checkout acceptance**: `git clone -b lasso-desktop-mac … && mac/Scripts/build-app.sh release` on minime.

## 8. Risks and open questions

### Risks

- **TCC prompts need a person at minime** the first time each permission is asked for. Clicking Allow through the Computer Use connector is a settings change on the user's behalf, so it needs their go-ahead.
- **`cacheDisplay` may skip some SwiftUI or layer content.** The `ImageRenderer` fallback exists, and M1 proves the method on the rail before anything else depends on it.
- **A non-activating `NSPanel` has to take key status** for keyboard navigation without stealing focus from the user's app. The fix is a `canBecomeKey` override, and M1 proves it.
- **Swift 6 strict concurrency friction with the `AVAudioEngine` tap.** The plan is an audio actor plus `@preconcurrency import AVFoundation`.
- **Prod lasso only gets S1–S3 after a release.** Until then the app feature-detects them, and the blocked surfaces read as disabled.
- **It is tested on macOS 27 only**, although the target is 14.
- **The jessica-triage mux on minime:8090 is not ours.** It also means a stray `8090` default silently talks to prod through someone else's tunnel. `dev.sh doctor` warns when `baseURL` is 8090.

### Open questions for the user

- **Q1**: `↳` vs `→` semantics (§2.3-5).
- **Q2**: accessory app (no Dock icon, `LSUIElement`) or a regular app?
- **Q3**: on-device engine: hand-rolled Parakeet (big, later), or Apple's first-party SpeechAnalyzer (macOS 26+, runtime-gated)?
- **Q4**: recording colour: danger `#EB6E62`, or the accent?
- **Q5**: default bot name `assistant`?
