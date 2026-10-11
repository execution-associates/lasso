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
| `dist/LassoDesktop.app`, `dist/parity/` | `mac/dist/…` | The repo-root `dist/` is the release workflow's output (`dist/lasso-*`). |
| Mac's `127.0.0.1:8090` | The app defaults to `http://127.0.0.1:8090` as the brief says. On minime, the dev script points it at our own tunnel on `127.0.0.1:18090`. | `jessica-triage`'s mux holds 8090 on minime, and binding over it would fail `ExitOnForwardFailure`. |
| Frameworks: SwiftUI, AppKit, Foundation, AVFoundation, CoreGraphics only | The rule is **no third-party code**. Other Apple frameworks are allowed: Combine (`@Published`), Security (Keychain, required by constraint 4), ScreenCaptureKit (`S` attach), ApplicationServices (`AXIsProcessTrusted`), Carbon (`RegisterEventHotKey`, optional summon key). CoreML and Accelerate come later, for on-device ASR. | Constraints 3 and 4 can't be met with the brief's list. |
| One executable target | One executable `LassoDesktop` plus a library `LassoDesktopKit` and a test target | The harness is `LassoDesktop --parity`, the same shipped binary, so parity measures the real code. |

## 1. Scope

| # | Surface | Status | Notes |
|---|---|---|---|
| 0 | **Inbox**: the latest asks, finished turns and notices across lasso's claude and omp agents, answered in place | **next**, M8 | §13. Discard needs S1 (built on this branch, unreleased). Free text on a pane's ask needs S4 (proposed). |
| 1 | Rail at rest: the peek tab at the screen edge, with the most urgent state's dot | built, M1 | §10, §12 |
| 2 | Rail revealed: inbox, agents capsule with `+N`, settings | built, M1 and M2 | §12 |
| 3 | Card | built, M4 | Discard (`E`) needs S1 in production. Next steps come only from open asks; the API has no source of generated suggestions, so those are *later*. |
| 4 | Agents list (workspace groups, label else tab name; pins; context menu) | built, M5 | Pins are `agent_pins` in ui_state. |
| 5 | Bot chat | built, M6 | omp bots through `/api/bots/<name>/chat…`; claude-runtime bots through their pane's chat routes. |
| 6 | Settings (lasso connection, permissions; opens itself on first launch) | built, M6 | |
| 7 | Voice | removed for now | §12. S2 and S3 stay on the server, unused. |

Server additions:
- **S1**: ui_state card dismissals (`dismissed_cards`, written through `card_dismiss`). Built on this branch. The Inbox's Discard uses it as is.
- **S2**: `/api/transcribe`. Built, unused.
- **S3**: bot-send `meta`. Built, unused.
- **S4**: free text on `POST /api/chat/answer`, for a pane ask's "Other" row (§13.4). Built on this branch, verified against live claude and omp dialogs.
- **S5**: `pending_ui` on `GET /api/bots`, `id`/`at` on SSE notices, `dev`/`listen` on a `-dev` lasso's `/api/version`. Built on this branch.

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
| Focus in herdr (list menu only) | POST `/api/focus?host=` | `{pane_id}` | `{ok, reattach}` | `src/main.go:1355` |
| Kill agent (`X`) | POST `/api/agent/close` | `{pane_id, agent_id, host, remove_worktree:false}` | `{agent_killed, pane_closed, removed_worktree}` | `src/closeme.go:34` |
| End a pane without an agent record | POST `/api/close?host=` | `{pane_ids:[…]}` | `{closed, errors}` | `src/main.go:1558` |
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

**S4: free text on a pane's ask** (built, for the Inbox, §13.4)
- `POST /api/chat/answer`'s `answers[i]` gains an optional `custom: string` (≤ `chatAskQuestionCap` runes, no control characters). With it, `selected` may be empty, and lasso walks to the dialog's own "Other" row, types the text and submits, after the same `askScreenHolds` check as a pick.
- Both dialogs have that row (`chatAskQuestion.custom` is what the transcript records from it). The keystroke walk for each harness is measured against a real dialog before it ships, as `claudeAskKeystrokes` and `ompAskKeystrokes` were, and is unit-tested the same way.
- As built: claude's Other is a text field the cursor walks onto; omp's is "Other (type your own)", whose Enter opens a prompt. The text goes in a write of its own and the following keys in a later one (`chatAskSegmentGap`, 200 ms), since claude takes a long write as a paste and drops its Enter, and omp loses keys that arrive with its prompt's Enter. A claude single-select question laid out with previews has no Other row, so a `custom` answer there is refused; the client sends `previews: true` for such a question. Text is at most 1200 characters with no newline (both dialogs take a newline as Enter).

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

- Each state is mounted in a borderless `NSWindow` (ordered in, off every screen) and drawn with `cacheDisplay(in:to:)` into an `NSBitmapImageRep` of our own: tagged sRGB, pixel size exactly 2× the point size. That gives **2× regardless of the Mac's display scale**.
- Pixels are read from the bitmap's raw bytes. `colorAt(x:y:)` labels them generic RGB (gamma 1.8), and converting that "to sRGB" brightened every colour by 3–8 ΔE; that was the first run's only colour failure.
- The PNG is written as `mac/dist/parity/<state>.png`.
- Parity runs inside the Aqua session through a one-shot LaunchAgent (§7). It writes `report.json` (last, so its presence means done), `report.txt`, the PNGs and the motion samples (`probe-*.csv`); `dev.sh parity` copies them back to titan's `mac/dist/parity/`.

### 5.3 Comparison method

- **Anchors**: views carry `.parityAnchor("rail.agents.capsule")`, a preference key that is a no-op outside parity mode. It reports frames in the root view's coordinates.
- **Geometry** (`tokens.json` `geometry`): anchor frames give layout truth (width, gap, pitch, pad_top, span…); pixel scans along a row confirm what was drawn (opaque width, fill vs rim width by nearest colour, glyph diameter against the capsule fill).
- **Colour** (`tokens.json` `patches`): the per-channel median of an explicit rect (pt, relative to the anchor), compared with **CIEDE2000** against the token, and required opaque.
- **Static**: every `Theme` token is compared with `tokens.json`: value, provenance and `proposed`, both ways (a token missing from either side fails).
- **Fixture**: the state's `expect` (badge count, rail slot order) against the models.
- **Accessibility**: the harness walks its own process through the accessibility client API (`AXUIElementCreateApplication(getpid())`), as VoiceOver would. SwiftUI builds its tree only for a client, so `NSView.accessibilityChildren` shows nothing; reading one's own process needs no Accessibility permission. Every `AXButton` must have a description, and the walk must find at least one.
- **Keycaps** (from M4): each rendered keycap is a `KeyBinding` value from a single registry, not a string; the check asserts it is registered in the active key handler for that state.

### 5.4 Motion

1. **Static**: Theme animation parameters equal the spec exactly.
2. **SwiftUI's spring, analytically**: `Spring(response:dampingRatio:)` sampled every 0.5 ms over the spec travel matches the closed-form oscillator (settle within 1 px, overshoot) — within 0.4 ms and 0.00 px on minime.
3. **The app's animation, on SwiftUI's own clock**: the harness sets `RailModel.animationProbe`, which wraps the token's animation in `ProbedAnimation` (a `CustomAnimation`) that records the animation time and value SwiftUI computes. The same travel is animated by a **reference** SwiftUI spring built from `tokens.json`'s numbers (not Theme's) in a view of its own. Gate: the app's settle time (±0.5 pt band, interpolated between samples) and overshoot against the reference's.
   - SwiftUI steps its spring frame by frame, so as run it sits up to ~3 pt off the closed form on 290 pt (settle ~215 ms, overshoot ~0.05 px, against 186 ms and 0.88 px closed-form). No SwiftUI spring can meet a ±1 px gate against the formula; the reference comparison is what catches a wrong, extra or delayed animation in the app. The closed-form numbers are reported as `info`.
   - Timing frames by `CACurrentMediaTime` or the display link was tried first and rejected: SwiftUI evaluates an animation twice per 60 Hz frame, so per-frame timestamps scatter by several ms.
4. **Drawn frames**: an `Animatable` modifier on the reveal records every value SwiftUI draws; the last must equal the rest position.

### 5.5 Tolerances

| Check | Tolerance |
|---|---|
| Colour (CIEDE2000) on named patches | ≤ 2.0, opaque |
| Geometry (anchor frame and pixel scan) | ±1 pt |
| Animation duration (app vs reference spring; SwiftUI vs closed form) | ±10 ms |
| Spring overshoot | ±1 px |
| Drawn frames end at rest | ±0.5 pt |
| Accessibility labels, keycap/binding agreement | exact |

`parity-check.sh` prints a table of `state | check | token | spec | measured | Δ | result` and exits non-zero on any failure. States not built yet report `pending`, which is not a failure; each milestone names the states it must turn green.

### 5.6 Tests

`Scripts/test.sh` (`dev.sh test`) runs swift-testing on the Mac. The Command Line Tools ship its macro plugin in `usr/lib/swift/host/plugins/testing`, a directory SwiftPM does not search, so the script passes `-plugin-path`. The tests cover CIEDE2000 against Sharma's reference pairs, the spring model, token/spec agreement, fixture ordering, and that no source calls `print(` or `NSLog(`.

## 6. Milestones (each ends with `mac/Scripts/dev.sh parity` exiting 0)

**M1: Rail on minime, from fixtures (visible first)**
- Probes first:
  - compile `@State` and `@Observable` with Swift 6.4 CLT and record the result; fall back to `ObservableObject` (result: `@State` fails, `@Observable` compiles; the app uses `ObservableObject`)
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

**M8: Inbox** (§13; built alongside M4's card, on M1, M2 and S1)
- Membership and order (§13.2): asks, then blocked with no choices, then finished turns, then notices, newest first in each; claude and omp only.
- Asks drawn from `chatAsk` and an omp bot's `pending_ui`, answered in place with the draw-time capture (§13.3, §13.4).
- Kill (panes, behind a confirm) and Discard (no write to the agent; S1 for panes and bots, local for notices) (§13.6).
- Blocked with no choices: status plus Focus in herdr (§13.5).
- Per-item seen state, the badge as "new since you looked", `J`/`K` marking seen, the empty state (§13.7).
- S4 on the server, if the user takes it (§13.4).
- **Accept**:
  - parity for inbox-open (extended fixture), inbox-empty and inbox-ask-picked
  - the §13.9 tests
  - a live round trip on minime, with the user present: answer a real ask in a claude pane and in an omp pane from the Inbox, and Discard a finished turn and see it come back on the agent's next turn

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

Installed on titan (2026-10-11) as a copy, not a link, because the weekly prune can delete a worktree: `install -m 0644 mac/Scripts/lasso-desktop-tunnel.service ~/.config/systemd/user/ && systemctl --user daemon-reload && systemctl --user enable --now lasso-desktop-tunnel`.

Check it with `ssh minime curl -s 127.0.0.1:18090/api/version`.

### 7.2 Signing identity (once, on minime)

`mac/Scripts/make-signing-identity.sh`:
1. `openssl req -x509` with `extendedKeyUsage=codeSigning`, CN `LassoDesktop Dev (minime)`, 10 years.
2. Create `~/Library/Keychains/lasso-desktop-signing.keychain-db`. Its password is in 1Password and fed on stdin from titan's `secret LASSO_DESKTOP_SIGNING_KEYCHAIN`.
3. `security import … -T /usr/bin/codesign`, then `set-key-partition-list -S apple-tool:,apple:`.
4. Disable auto-lock.
5. `codesign` accepts the untrusted self-signed identity, so no trust setting (and no click on minime's screen) is needed. The script ends by signing a probe binary to prove it.

On minime the identity is `LassoDesktop Dev (mini)` (the CN takes `scutil --get LocalHostName`). If the keychain is locked after a reboot, `dev.sh unlock` unlocks it with the password from `secret LASSO_DESKTOP_SIGNING_KEYCHAIN`.

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

## 9. Status (2026-10-11)

M1–M7 are built and every named state passes parity on minime: 192 checks, 0 failures, 0 pending (`mac/Scripts/dev.sh parity`). `dev.sh test` runs 16 tests. The app runs live on minime against titan's prod lasso through the tunnel.

**Server additions S1–S3** are on this branch with Go tests (`mise run test`), and verified from the Mac against a dev lasso built from it (`./lasso -listen 127.0.0.1:8190 -insecure-no-auth -dev`, reached on minime as `127.0.0.1:18190`). Prod lasso gets them with the next release. Until then:
- The assistant choice is kept on the Mac and says so; Discard is hidden; the lasso voice engine reports that lasso has no transcription route.
- A dev lasso shares `lasso.db` with prod, and prod rewrites `ui_state` without the S1 fields whenever it saves it, so a choice made through the dev lasso does not survive a prod save.
- lasso answers an unknown `/api/*` path with its web app (HTML, 200). The client treats an HTML answer to an API call as a missing route.

**Changed from the plan while building**
- Claude-runtime bots chat now, through the pane chat routes on the bot's pane: every existing bot on titan is claude-runtime, so leaving them for later would have left the chat with nothing to talk to. omp bots use `/api/bots/<name>/chat…`, and only they carry `meta`.
- `V` in a card dictates into that card's composer, unsent; `V` in the chat (or the rail's record tool) sends the transcript to the assistant.
- Parity's motion and accessibility methods changed after the first runs (§5.2–5.4).

**Not done**
- A live voice round trip: the server needs a transcription provider (`/api/transcribe/config`, or the `transcribe` settings section) and a key, or the app a Groq/OpenAI key in onboarding; then the first `V` asks for the microphone on minime's screen.
- Creating the assistant bot: onboarding's Create button does it (`POST /api/bots`, omp, started); it was not clicked, since it makes a real bot.
- On-device transcription (Q3), native notifications, generated next steps, a Settings tab section for `transcribe` in the web client.

## 10. Design rework from the reference screenshots (2026-10-11)

The user supplied reference screenshots of the intended look (kept out of git). The tokens were re-measured from them by pixel scans: ref a2/a3/a5 at 2 px per point, ref b2 at 1 px per point. Neutrals are taken as sampled; saturated hues stay the brief's, because the screenshots carry the display's own colour profile, which moves saturated colours and leaves greys alone.

- **At rest the rail is a peek**: a flat black tab, 13 × 80 pt, growing out of the screen edge with flared ends, with the most urgent state as an 8 pt dot in its centre. The rail panel now sits flush against the screen edge. Resting the pointer on the peek for 80 ms reveals the rail. With nothing open beside it, 450 ms off the rail hides it again. The pointer is polled at 20 Hz, because a non-activating panel of a background app gets no hover events.
- **The rail's groups are lit from above.** The fill shades from `#2E3032` to `#0C0E10`, and the 1 pt rim from `#8F8F8F` to `#1F2021`. The badge is a 10 pt dot in the most urgent state's colour, ringed in near-black, with no count. Idle is `#98989D`. The geometry is the brief's and is unchanged.
- **The card is 516×587 on `#040306`.** It has round 28 pt buttons (paging chevrons on the left, close on the right), a 16 pt semibold title, and an identity line with an avatar. The conversation sits in an inner `#1B191C` box with a 12 pt radius. The composer row is 32 pt round controls with the microphone inside the field. A divider sits above the footer, which holds "Next steps", a Kill agent pill and a white Discard pill.
- **The unread bar** is a 6×18 accent pill for the open card and 6 pt grey dots for the others.
- **The list** has group headers, then rows of dot, avatar, name, microphone and ×.
- **The chat** puts your messages in `#1D1C1E` bubbles, with a "Talking to …" line over the composer.
- **Keycaps are hidden**, since the references show none, and appear over their controls while ⌘ is held. Parity renders each state a second time with ⌘ held (`<state>-keys.png`) and checks every legend against its live binding.
- **Parity covers all seven states: 211 checks pass.** New checks include colour patches read along a gradient, `x`/`right_inset` measures, the peek's visible width, and the ⌘-held keycap pass.
- **The app traces state changes to its log** (`dev.sh logs`): events only, never typed or spoken text. The hover reveal was verified live on minime by moving the pointer onto the peek from inside the Aqua session.

## 11. Execution Associates palette (2026-10-11)

The colours are the Execution Associates palette as isb's web app applies it (`isb web/src/index.css`, `:root.ea`, oklch converted to sRGB). The shapes and sizes from §10 are unchanged.

- **Panels and controls:** panels are ink `#1B0A2C` with a white-14 % border. The conversation box is `--muted` `#291838`. Buttons, pills, the composer and chat bubbles are `--secondary` `#2D1D3C`, and disabled ones `--card` `#210E33`. The white pill is `--primary` `#FDF6FA` with ink text.
- **Text:** `--foreground` `#F4ECF2`, with secondary text `--muted-foreground` `#B7ADBB`.
- **Brand accents:** the brand colour is glow pink `#F099BF` (the open card's unread pill and the selected segment). Focus is peach `#FEBE74`, and recording is magenta `#DD519B`.
- **States** use isb's status colours, chosen to stay clear of the pink: done `--success` `#5ED99E`, working `--info` `#80B3FD`, blocked `--destructive` `#FA686A`, idle muted at 60 % on ink.
- **The rail's glass** is re-tinted to ink: the fill runs `#352545` → ink-deep `#12071F`, and the rim `#947D99` → `#2A1D37`.

**Fonts** are the Execution Associates faces as isb applies them: Geist for text, Geist Mono for code, and Archivo at 112 % width (isb's `.font-display`) for titles.
- They are the variable TTFs from Google Fonts' repository, licensed under SIL OFL 1.1, and kept in `mac/Resources/Fonts` with their licences.
- `build-app.sh` copies them into the bundle, and `BundledFonts.register()` registers them for the app's process only.
- A font token sets the face through its variable axes (wght, and wdth for Archivo). Parity checks that each font token resolves to its face, and Archivo to its width.

## 12. Rail, inbox, bots and no voice (2026-10-11)

The user reshaped the app:

- **The rail is three buttons.** The top button is the **inbox**: what waits on the human. That means blocked agents, agents that finished unseen and aren't dismissed, bots waiting on an answer, and bots with an unread reply. The agents capsule below it opens the **agents** list. The ellipsis opens **settings**. The tools capsule is gone.
- **Voice is removed for now.** The capture, transcription and recording UI are gone, along with the V binding, the microphone permission and every voice and recording token. `/api/transcribe` (S2) and bot-send `meta` (S3) stay on the server, unused by the app. The voice code is in git history (`mac: M6 and M7 …`).
- **"Bot", never "assistant", and no default bot.** The app has no single bot of its own; that will come later. A bot's chat opens from the inbox, or from its pane in the agents list. The default-bot setting and its Create button are gone, from the app and from the server (ui_state `default_bot` was removed before release). So brief B.5's "ensure the app's bot exists" is deferred.
- **Agents are grouped by herdr workspace and named by their label, else their tab name** (herdr.dev/docs/agents, "custom agent labels").
  - The server now carries herdr's label in the fleet listing as `hostPane.agent_label`, from `agent.list`'s `name`.
  - Until that reaches production, tab names already name the agents in titan's Agents workspace (stub, clem, jessica, …).
  - A pane that is a bot's (by `pane_id` or label) is listed as the bot, counted once in the badge, and opens the bot's chat.
- **Parity's named states** are now rail-collapsed, rail-expanded, inbox-open, card-open, list-panel-open, chat-open and settings. "recording" went with voice, and "onboarding" is settings, which still opens itself on the first launch.

## 13. Inbox

The Inbox is the surface this app exists for: **the latest messages across lasso's agents in one place, where the user can address them.** It is the rail's top button, and its badge is the rail's one signal. Milestone M8 (§6).

### 13.1 Scope

**claude and omp only**: claude and omp panes (`hostPane.agent` is `claude` or `omp`), plus omp bots. No other harness enters the Inbox, even when herdr reports it blocked, and nothing here is designed for one.
- A claude-runtime bot runs in a claude pane, so it enters as that pane, through the pane chat routes.
- A pane that is an omp bot's (by `pane_id` or herdr label, `FleetModel.bot(for:)`) enters once, as the bot.

### 13.2 Membership and order

| Group | An item when | Read from |
|---|---|---|
| **1. Waiting on you: ask** | A pane has `agent_status == "blocked"` and its chat's newest tool item carries an open `ask` (no `selected`, no `custom`). Or an omp bot has `state == "blocked"` and its chat carries `pending_ui`. | `GET /api/all-panes` (`src/hostpanes.go:923`; status from `src/panestatus.go`), then `GET /api/chat?pane=&host=` (`src/chatview.go:1823`, `ask` on `chatTool`, `:100-175`). Bots: `GET /api/bots`, then `GET /api/bots/<name>/chat` (`src/botsapi.go:578`), `pending_ui` (`src/botomp.go:125`). |
| **1. Waiting on you: no choices** | A pane is blocked and its chat has no open ask (§13.5). | The same reads. |
| **2. Finished turn** | A pane is `done` or `idle` and its `transcript_at` is newer than this Mac's seen mark for it. Or an omp bot's `last_kind == "agent"` and its `last_at` is newer than the seen mark. | `transcript_at` on the pane row; `last_at` on the bot view. |
| **3. Notice** | The server raised one. | SSE `notice {level, title, detail}` (`hub.notify`, `src/main.go:2620`). |

**Order:** group 1, then 2, then 3. Inside each group, newest first: by `transcript_at`, or by `last_at` for a bot, or by arrival for a notice.

**What a row shows**
- Every row has the state dot, avatar, the agent's name (label, else tab name), its workspace, and how long ago.
- An ask shows the ask itself (§13.3).
- A finished turn shows the agent's last message, two lines, from the newest agent item of the chat.
- A notice shows its title and detail.

**Chat reads are bounded.** The chat of a pane or bot is read only while it is a group 1 or 2 candidate.
- It is re-read only when its `transcript_at`, its `last_at` or its `pending_ui` changes, or on a `bot-chat` nudge for that bot.
- Reads are cached per (item, `transcript_at`), with at most 4 in flight.

**Notices** are sent once, each with an `id` and an `at` (unix ms) from the server.
- The app dedupes them by `id`, orders them by `at`, and keeps the last 50 in memory.
- A relaunch loses them, as a browser tab loses its toasts.
- They are this Mac's only. Nothing about them is shared.

**First launch:** the seen store is seeded with every current pane's `transcript_at` and every bot's `last_at`, so the Inbox starts with what is waiting, not with history. After that, a pane seen for the first time enters with its first finished turn.

### 13.3 Addressing: the agent's own choices

**Never invented.** An ask is rendered from lasso's one normalised shape, `chatAsk { questions: [{ header, question, options: [{ label, description, preview }], multi, recommended, selected, custom }] }` (`src/chatview.go:141-175`).
- `header` is a chip, and `question` is the text.
- Each option shows its `label` and `description` exactly as the agent wrote them.
- `recommended` marks that option and puts the highlight on it first.
- `preview` stays folded until that option is picked, then unfolds under it.
- `multi` makes the options toggles with a Submit. A single-select question answers on its pick.
- A multi-question ask shows every question in order and is answered in one request, one entry per question in order. That is how the server types it (`ompAskKeystrokes`, `claudeAskKeystrokes`).

**An omp bot's question is `pending_ui`** (`{id, method, title, message, options[], placeholder}`, `src/botomp.go:125`): `title` and `message` are the text.
- `select` renders its `options` as given.
- `confirm` renders Yes and No.
- `input` and `editor` render a text field with the placeholder. That is the bot's free-text path, and it works today.
- If the bot chat's newest tool item carries a `chatAsk` whose question matches `title`, its descriptions and previews are shown too. The answer is still the `pending_ui` option string at the same index.

**Keys** are the card's (Q1):
- `1`–`9` highlight an option.
- `→` takes the highlighted option.
- `↳` puts it in the free-text field to edit, where the dialog has one (§13.4).
- `esc` folds a picked preview, then closes.

### 13.4 Answering, with staleness protection

**Captured at draw time, never re-derived.** When an ask row is drawn, the app freezes an immutable `AskCapture` into the item:
- the pane and host
- `expect`: the first open question's text, which is the one the dialog is showing
- `labels`: that question's option labels
- per question, the option count and `multi`

**The send uses only the capture.** It never reads the live chat:
- `POST /api/chat/answer?host= {pane_id, expect, labels, answers:[{selected, multi, options}]}` (`src/chatview.go:2337`)
- The server checks `expect` and `labels` against the pane's screen (`askScreenHolds`, `:2442`). An answer to a question the agent has moved past is `refused`, not misapplied.

**A changed ask is a new item.** The item's identity includes a hash of the ask. A new question replaces the row and drops any pick in progress, so a pick can never ride onto a different question.

**`refused`** shows the server's `detail` on the row, keeps the item and re-reads the chat. Nothing is ever retried, by the app or by the user's key-repeat.

**Bots** (`src/botsapi.go:597-623`):
- `POST /api/bots/<name>/chat/answer` with `{id, value}`, `{id, confirmed}` or `{id, cancelled: true}`.
- `id` comes from the capture. The bridge refuses an id that is no longer open ("that question is no longer open", `src/botomp.go:718`).

**Free text on a pane's ask is S4 (§3):** `answers[i].custom`, typed into the dialog's own "Other" row after the same screen check.
- A question with previews (claude lays those out with no Other row) gets no text field; it shows **Answer in the terminal** (`POST /api/focus`) for anything beyond its options.
- Against a lasso without S4 (the request's `custom` is ignored, so the app checks `/api/version` for this branch's protocol), the text field is replaced by the same action.
- The app never sends free text to a blocked pane through `/api/chat/send`: with a dialog up, that is a blind paste into it.

### 13.5 Blocked with no choices

When a pane is blocked but its chat carries no open ask, its row reads **blocked, no choices**. Examples: a claude permission dialog, or an approval outside both shapes.
- The row shows the state, the agent's last tool line if there is one, and three actions:
  - **Focus in herdr** (`POST /api/focus?host= {pane_id}`, `src/main.go:1355`)
  - Discard
  - Kill
- The app never fabricates options and never types keystrokes into the pane.
- The card opened from this row has no composer send while the pane is blocked.

### 13.6 Kill and Discard

**Kill ends the agent.** It is for panes only, behind a confirm step on the row (`X`, then `X` or ⏎ again; `esc` backs out).
- A pane with an agent record: `POST /api/agent/close {pane_id, agent_id, host, remove_worktree: false}` (`src/closeme.go:34`).
- A pane without one: `POST /api/close?host= {pane_ids}` (`src/main.go:1558`).
- A bot has no Kill in the Inbox. Stopping a bot is managed in lasso's Bots view.

**Discard changes nothing on the agent.** It clears the user's attention on that item: it never closes, stops, sends to or writes anything to the agent. It is S1's `dismissed_cards`, shared through ui_state so every client agrees. It writes `POST /api/ui-state {client_id, card_dismiss: {key: at}}`, where the key and value depend on the item:

| Item | Key | Value | It returns when |
|---|---|---|---|
| Pane (ask, no choices, finished turn) | paneKey `host\u0000pane_id` | its `transcript_at` | `transcript_at` moves past the value: a new question, or a new turn |
| omp bot ask | `bot:<name>#<pending_ui id>` | `0` | a different question opens (a new id, so a new key) |
| omp bot finished turn | `bot:<name>` | `last_at` in unix ms | `last_at` moves past it |
| Notice | none: removed from this Mac's memory | | never; a notice is one-shot |

- The server takes any key of 1–300 bytes (`mergeDismissedCards`, `src/hostpanes.go:711`), so bot keys need no server change.
- The 256-entry cap drops the smallest value first, so bot-ask keys (value `0`) go first. Losing one only brings its item back.
- **A dismissed item is hidden, not deleted:** the rule above decides on every update whether it shows.
- Against a lasso without S1 (no `dismissed_cards` in GET), Discard is hidden, as the card's is today.

### 13.7 Seen, the badge and the empty state

**Seen and discarded are separate.**
- **Seen** is per-item and kept on this Mac (UserDefaults), like the bot read marks of §12. Seen is about this screen.
- **Discarded** is shared (§13.6). Discard is about the user's attention everywhere.
- Each mark is the item's `transcript_at`, `last_at`, ask hash, or arrival time.

**The Inbox opens on its newest message, never a list.** The rail's inbox button opens the card on the first item in Inbox order (the reference prototype's behaviour, which the user chose). `J`/`K` page through the rest in that order, and the unread bar shows where you are. With nothing waiting, a small panel shows the empty state. Pressed again while the card is open, the button closes it.
- A bot's item opens on the pane the bot runs in. For an omp bot, the card reads and answers through the bot's routes: its `pending_ui` is the next steps, and a message goes through `chat/send`. A bot has no Kill in the card.
- The Inbox panel's rows remain only for an item with no pane to show, such as a bot whose pane is gone.

**What counts as seeing an item:**
- An item is seen when the card lands on it: opened from the rail, or paged to with `J`/`K`.

**The badge means "new since you looked":**
- It shows when at least one item that is not discarded is unseen.
- Its colour is the most urgent such item's state.
- The peek's dot follows the same rule.
- Seen items stay listed until they are answered, discarded, or no longer qualify. Seeing an item only takes it out of the badge.

**Empty state:** "Nothing waiting on you." with a second line: "Questions, finished turns and notices from your claude and omp agents land here." An empty Inbox is the normal state, not an error, so it has no illustration and no call to action.

### 13.8 What exists, and what M8 changes

**Built (§12):**
- The rail's inbox button and badge.
- `InboxModel`, which lists blocked and done agents, blocked bots and bots with an unread reply.
- Discard of a pane through `dismissed_cards`.
- Per-Mac bot read marks (`BotReads`).
- The inbox-open parity state.

**M8 changes:**
- The claude/omp filter, with claude-runtime bots entering as panes.
- Asks rendered and answered in place, with the draw-time capture.
- Blocked with no choices.
- Notices.
- An omp bot's `pending_ui`.
- Bot Discard keys.
- Per-item seen state and the badge rule.
- `J`/`K` and the empty state.
- S4, if taken.

### 13.9 Harness and tests

**Parity states:**
- **inbox-open**: the card the Inbox opens on, its newest item (a bot's question), with the unread bar over the rest. Its fixture grows to cover: a claude single-select ask with a `recommended` option and a folded `preview`, an omp multi-select ask, a blocked pane with no ask, a finished turn, an omp bot's `select`, and a notice.
- **inbox-ask-picked**: a preview unfolded under its pick.
- **inbox-empty**: the empty-state panel.
- Each state gets geometry, colour and accessibility labels, and a ⌘-held keycap pass checking every legend against its live binding.

**Contract:** the `chat_payload` golden already carries an `ask` (`mac/Spec/contract/chat_payload.json`). M8 adds `pending_ui` to the bot chat golden and a `notice` golden, written by `TestDesktopContract`.

**Swift tests:**
- Membership and order, including the harness filter and a bot's pane listed once.
- The capture: the answer body equals the draw-time capture after the model has updated underneath it.
- A changed ask drops the pick.
- Discard's return rule for each key kind.
- Seen versus discarded, and the badge.
- `J`/`K` marking seen.
- No action ever calls `/api/chat/send` for a blocked pane.

## 14. Connecting to a dev lasso

The app can talk to any lasso this Mac reaches, and switches between them in place.

- **Settings lists every lasso that answers** `/api/version`: the candidates are `lassoCandidates` in UserDefaults (on minime, the tunnel's 18090 and 18190–18199), else production's 8090 and the 8190–8199 a `mise run dev` lasso binds. Each row says production or dev (`/api/version`'s `dev`), its version, and for a dev lasso the address it bound. **Use** switches.
- **Switching is in place.** Every live action shares one `LassoClient`. `FleetSync.retarget` repoints it, empties the fleet, restarts the event stream and refills from the new lasso, and the choice is remembered. Open panels close, since they belonged to the old lasso.
- **From titan:** `mac/Scripts/dev.sh use dev` finds the `mise run dev` backend (this worktree's, else the first on 8190–8199), checks it answers through the tunnel and relaunches the app on it. `use prod` goes back, and `use PORT` picks one.
- **The tunnel** (`lasso-desktop-tunnel.service`) forwards minime's 18190–18199 to titan's 8190–8199, so a dev lasso that bumped past 8190 is still reachable.
- **A reinstall keeps the choice.** `agent.sh install` sets `baseURL` only on a fresh Mac or when `LASSO_BASE_URL` is given.
- **A dev lasso is not a sandbox.** It drives the same herdr and reads the same `lasso.db` as production, so the agents in it are real, and Kill and answers act on them. Two lassos of different versions on one `lasso.db` also rewrite each other's `ui_state`: an older one drops fields it does not know, such as `dismissed_cards`. So while the server changes are unreleased, titan's production lasso runs this branch's build (§15).

## 15. titan runs this branch's server until release

The Inbox needs S1, S4 and S5 on the lasso the app talks to, and a dev lasso shares `lasso.db` with production (§14). So titan's production lasso runs this branch's build, not the 6.4.0 release, until these changes are released.
- The binary is `~/.local/libexec/lasso-desktop/lasso`, built with `mise run build` from this worktree and copied there, so the weekly worktree prune cannot delete it.
- A systemd drop-in, `/etc/systemd/system/lasso.service.d/desktop-branch.conf`, points `lasso.service` at it with production's own flags.
- It reports `6.4.0 (dev)`: Go does not stamp a build made in a linked worktree with its commit. It is not a `-dev` instance and never self-updates.
- **To update it:** `mise run build`, then `install -m 0755 lasso ~/.local/libexec/lasso-desktop/lasso.new && mv ~/.local/libexec/lasso-desktop/lasso.new ~/.local/libexec/lasso-desktop/lasso && sudo systemctl restart lasso`.
- **To undo it, and once the release ships:** `sudo rm /etc/systemd/system/lasso.service.d/desktop-branch.conf && sudo systemctl daemon-reload && sudo systemctl restart lasso`. That puts back the mise-installed release.

## 16. Hover interaction language (reference recording)

The spec steward supplied a screen recording of an earlier prototype (`~/lasso-desktop-reference/reference-prototype-hover.mp4`, 60 fps, kept out of the repo). Every value below was measured frame by frame from it, and each token's `source` names its frames.

**What the recording actually shows**
- **Two prototypes are cut together.** One is purple (frames 1–528) and the other graphite (579–992). The recorder also zooms (f198–241, 2×) and pans (f483–660), so geometry was read only while the camera was still, at 2 px per point.
- **The purple one is clicked, not hovered**: the recorder's click ripples are at f216, 303, 353 and 450. So the hover language comes from the graphite one.
- **Several things the steward's notes expected are not in it.** No capsule expands on hover, no agent dot becomes a ✕ (the ✕s are per-row buttons in a panel), no neighbour dims, and the card's ✕ circle is always drawn and does not change on hover.
- **The recorder draws a smoothed cursor** that arrives 33–150 ms after the change it causes. So delays are measured from each control's own highlight, never from the drawn pointer.

**What the app takes from it**
- **Hover fill** (`rail.hover.fill`): a control under the pointer takes a 20% white overlay, stepped in one frame with no animation (f655, f734). The whole agents capsule takes it, and its neighbours do not change. One value says what is hovered: `RailModel.hovered`.
- **Pressed** (`rail.pressed`): the whole control drops to 0.71 opacity while pressed (f219–225, f355–361).
- **Resting opens** (`rail.hover.item_open_ms`, 117 ms, f655→f662): the inbox, the agents capsule and settings open by hover alone. A hover-opened panel is ordered front **without the keyboard**, so typing in another app never lands on the card's one-letter keys. A click on it or on its control, or the summon key or inbox shortcut, gives it the keyboard.
- **A hover-opened panel is a glance** (`rail.hover.panel_close_ms`, 317 ms, f707→f726): once the pointer has left both the rail and the panel for that long, it closes, unless a click made it the user's.
- **The rail hides** 370 ms after the pointer leaves (`rail.hover.close_ms`, f959→f981).
- **Panels fade in over 117 ms, ease-out, sliding 4.5 pt away from the rail**, and fade out over 67 ms (`motion.card_open`, f662–669; `motion.card_close`, f726–730). This replaces the brief's 70 ms.
- **Not taken:**
  - The tools capsule and its tooltips ("Start recording", "Take a screenshot of a region, then ask about it") are left out, because the user removed the tools capsule and voice (§12). The tooltips' lesson stands: a control's label says what it does.
  - Tooltips over the inbox, agents and settings are left out too, because resting on those opens them at 117 ms, before a tooltip could be read.

**Hover comes from polling, not tracking areas.** The steward asked for `NSTrackingArea` with `.activeAlways`. The app instead polls `NSEvent.mouseLocation` at 20 Hz against `RailMetrics.itemFrames`, the same frames the rail draws (a test checks them against the rendered anchors). It does so because a non-activating panel of a background app gets no hover events from SwiftUI, and polling also drives the reveal, the drag and the glance close from one loop.

**Parity states** `rail-hover-inbox` and `rail-hover-agents` check the overlay on the hovered control and the untouched neighbour. The motion pass checks the 117 ms and 67 ms curves.

## 17. Shortcut, sound and brand

- **Inbox shortcut**, in Settings › Inbox (UserDefaults `inboxShortcut`):
  - **Default ⌥⌥**: double-tap Option. A tap is the modifier alone for at most 300 ms, and the second tap must end within 400 ms of the first (`keys.inbox_double_tap`). Any other key or modifier in between starts over.
  - **⌘⌘ and ⌃⌃** work the same way.
  - **Record…** takes any combination with ⌘, ⌥ or ⌃ through Carbon's `RegisterEventHotKey`, which needs no permission.
  - **Off.**
  - A double tap reads modifier keys in every app through a global event monitor, which macOS delivers only with **Accessibility**, so Settings says so until it is granted.
  - The shortcut opens the inbox with the keyboard in it; pressed again while the inbox has the keyboard, it closes it.
- **Arrival sound**, in Settings › Inbox (UserDefaults `inboxSound`, default Whoosh):
  - Six sounds made with ElevenLabs on fal (`mac/Resources/Sounds/README.md` has the prompts), all levelled to a −4 dBFS peak. Choosing one plays it.
  - It plays when the inbox holds an item, a new turn or a new question it did not hold before (`InboxItem.arrivalKey`). The first reading is a baseline, and arrivals within 2 s make one sound (`live.arrival_sound`).
- **Settings footer**: the Execution Associates lockup as isb ships it (`Resources/Brand`), tinted like secondary text, linking to execution.associates.
- **App icon**: the EXA monogram, exactly as `exa/brand-assets/logo/exa-mark-white.png` draws it, on a macOS icon plate in the rail's ink gradient (`Resources/AppIcon.png`). `build-app.sh` packs `AppIcon.icns`.

## 18. Later: signing in to a deployed lasso through Cloudflare Access

The user's lasso is `https://lasso.orangecountyai.com`, behind Cloudflare Zero Trust (Access). The app should connect to it by URL and authenticate the way a browser does, rather than needing a tunnel. Not built yet.
- **Settings › lasso gains a URL field.** For an `https` lasso, the app first probes `/api/version` without credentials. A redirect to `<team>.cloudflareaccess.com` means Access sits in front.
- **Sign-in through the browser, the way `cloudflared access login` does it.** The app opens the Access login for that hostname in an `ASWebAuthenticationSession`, so the user's identity provider, MFA and device posture all apply as they do for the web app. It keeps the resulting `CF_Authorization` application token in the Keychain, then sends it on every request as the `cf-access-token` header (the event stream included), never in plain storage.
- **Refresh:** a 401 or 403, or a redirect to the Access login, marks the token expired. The app keeps the user's state, asks once to sign in again, and resumes the event stream afterwards.
- **lasso's own `UI_AUTH`** still applies behind Access when it is set: basic credentials from the Keychain, as today. A service token (`CF-Access-Client-Id`/`Secret`, which the client already sends when stored) stays the non-interactive alternative for a shared Mac.
- **Open questions:**
  - Whether this team's Access application allows the CLI/native login flow, or needs `cloudflared`'s transfer endpoint.
  - Whether to support several saved deployments at once, alongside the tunnel and dev entries of §14.
