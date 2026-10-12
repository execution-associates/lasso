# Lasso, the Mac app — plan

A native macOS client for a running lasso: a rail on the screen edge for the fleet, a card per agent, and a chat
with a lasso bot (the assistant), with speech-to-text input. This document is the plan; the brief it answers is
the Mac app brief (plan mode, 2026-10-11). The spec the harness asserts against is `mac/Spec/tokens.json`;
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
| Optional bearer token for a remote lasso | Whatever the lasso's network proves (§18): nothing on loopback or the tailnet, a bearer token from the network's OAuth server behind Cloudflare Access or with `MCP_OAUTH`, UI_AUTH basic for an older install, and an Access service token for an unattended Mac. All come from the Keychain. | A host-scoped MCP credential must not reach the whole UI API, which has no `requireHost` checks. So the token is the device's own sign-in, never a copied MCP token (the lasso-auth design). |
| Ad-hoc `codesign` | Sign with a self-signed identity in a dedicated keychain on minime. Ad-hoc is only the fallback for a fresh Mac and prints a warning. | TCC keys grants on the designated requirement. An ad-hoc cdhash changes every build. |
| `dist/Lasso.app`, `dist/parity/` | `mac/dist/…` | The repo-root `dist/` is the release workflow's output (`dist/lasso-*`). |
| Mac's `127.0.0.1:8090` | The app defaults to `http://127.0.0.1:8090` as the brief says. On minime, the dev script points it at our own tunnel on `127.0.0.1:18090`. | `jessica-triage`'s mux holds 8090 on minime, and binding over it would fail `ExitOnForwardFailure`. |
| Frameworks: SwiftUI, AppKit, Foundation, AVFoundation, CoreGraphics only | The rule is **no third-party code**. Other Apple frameworks are allowed: Combine (`@Published`), Security (Keychain, required by constraint 4), ScreenCaptureKit (`S` attach), ApplicationServices (`AXIsProcessTrusted`), Carbon (`RegisterEventHotKey`, optional summon key). CoreML and Accelerate come later, for on-device ASR. | Constraints 3 and 4 can't be met with the brief's list. |
| One executable target | One executable `Lasso` plus a library `LassoDesktopKit` and a test target (`LassoDesktopTests`) | The harness is `Lasso --parity`, the same shipped binary, so parity measures the real code. |
| No third-party code | Sparkle, for updates, is the one exception (the user asked for it). Only the executable target links it; the kit, its tests and the parity harness do not. | Updates need a signed feed and an installer, and Sparkle is the standard one. Recap already ships it the same way. |

## 1. Scope

| # | Surface | Status | Notes |
|---|---|---|---|
| 0 | **Inbox**: the latest asks, finished turns and notices across lasso's claude and omp agents, answered in place | **next**, M8 | §13. Acknowledge needs S1 (built on this branch, unreleased). Free text on a pane's ask needs S4 (proposed). |
| 1 | Rail at rest: the peek tab at the screen edge, with the most urgent state's dot | built, M1 | §10, §12 |
| 2 | Rail revealed: inbox, agents capsule with `+N`, settings | built, M1 and M2 | §12 |
| 3 | Card | built, M4 | Acknowledge (`E`) needs S1 in production. Next steps come only from open asks; the API has no source of generated suggestions, so those are *later*. |
| 4 | Agents list (workspace groups, label else tab name; pins; context menu) | built, M5 | Pins are `agent_pins` in ui_state. |
| 5 | Bot chat | built, M6 | omp bots through `/api/bots/<name>/chat…`; claude-runtime bots through their pane's chat routes. |
| 6 | Settings (lasso connection, permissions; opens itself on first launch) | built, M6 | |
| 7 | Voice | removed for now | §12. S2 and S3 stay on the server, unused. |

Server additions:
- **S1**: ui_state card dismissals (`dismissed_cards`, written through `card_dismiss`). Built on this branch. The Inbox's Acknowledge uses it as is.
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
5. **`↳` / `→` keys are ambiguous.** The proposal: `→` takes the highlighted step (sends it), `↳` (bound to Tab) puts it into the composer to edit, `1`–`9` highlight a step. **Open question Q1.**
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
| Attach a file (paperclip, paste, drop) | POST `/api/paste-file?host=&name=` | the raw file, `Content-Type` its MIME type; at most 1 GiB, never empty | `{path}`, which the client puts into the message text | `src/main.go` `servePasteFile` |
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
  - A card counts as acknowledged until that pane's `transcript_at` moves past the stored value.
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
  - `V` works when Lasso is key. A global push-to-talk is optional and is the only thing the Accessibility permission gates.
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
- Keychain generic passwords under service `com.orangecountyai.Lasso`, accounts `groq`, `openai`, `ui-auth`, `cf-access-id`, `cf-access-secret`.
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

- `Lasso --parity <state|all> --out <dir>` runs the shipped binary in parity mode.
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
- **Keys** (from M4): keys work but are not drawn. Each key is a `KeyBinding` value from a single registry; the check presses every key the state has live and asserts the surface's router (`CardKeys.route` among its set) sends it to that key's action.

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
| Accessibility labels, key routing | exact |

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
- Kill (panes, behind a confirm) and Acknowledge (no write to the agent; S1 for panes, bots and notices) (§13.6).
- Blocked with no choices: status plus Focus in herdr (§13.5).
- Per-item seen state, the badge as "new since you looked", `J`/`K` marking seen, the empty state (§13.7).
- S4 on the server, if the user takes it (§13.4).
- **Accept**:
  - parity for inbox-open (extended fixture), inbox-empty and inbox-ask-picked
  - the §13.9 tests
  - a live round trip on minime, with the user present: answer a real ask in a claude pane and in an omp pane from the Inbox, and Acknowledge a finished turn and see it come back on the agent's next turn

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
#             (swift build -c release --arch arm64 → mac/dist/Lasso.app,
#              Info.plist, codesign -s "LassoDesktop Dev (minime)" --keychain …)
#   install:  ditto mac/dist/Lasso.app ~/Applications/Lasso.app
#             defaults write com.orangecountyai.Lasso baseURL http://127.0.0.1:18090
#             (first time) launchctl bootstrap gui/501 ~/Library/LaunchAgents/com.orangecountyai.Lasso.plist
#   relaunch: launchctl kickstart -k gui/501/com.orangecountyai.Lasso
mac/Scripts/dev.sh parity [states]   # sync → build → kickstart the …Lasso.parity agent →
                                     # wait for report.json (timeout 120 s) → rsync results back → exit with its code
mac/Scripts/dev.sh logs       # tail ~/Library/Logs/Lasso.log (never holds secrets or transcripts)
```

`src/lasso-desktop/mac` is the default folder on the Mac, and `publish.sh` always builds releases there. `LASSO_MAC_DIR=<folder under ~>` gives a run another one (`LASSO_MAC_DIR=src/lasso-desktop-keys/mac mac/Scripts/dev.sh test`): sync is an `rsync --delete`, so two worktrees, or a worktree and a release, sharing one folder overwrite each other's tree mid-build.

**LaunchAgent plist**
- `ProgramArguments` = the bundle's `Contents/MacOS/Lasso`
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
- The assistant choice is kept on the Mac and says so; Acknowledge is hidden; the lasso voice engine reports that lasso has no transcription route.
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
- **The card is 516×587 on `#040306`.** It has round 28 pt buttons (paging chevrons on the left, close on the right), a 16 pt semibold title, and an identity line with an avatar. The conversation sits in an inner `#1B191C` box with a 12 pt radius. The composer row is 32 pt round controls with the microphone inside the field. A divider sits above the footer, which holds "Next steps", a Kill agent pill and a white Acknowledge pill.
- **The unread bar** is a 6×18 accent pill for the open card and 6 pt grey dots for the others.
- **The list** has group headers, then rows of dot, avatar, name, microphone and ×.
- **The chat** puts your messages in `#1D1C1E` bubbles, with a "Talking to …" line over the composer.
- **Keys are not drawn.** No keycap appears on any surface, hovered, clicked or with ⌘ held. Labels name the key ("Acknowledge (A)"), and parity checks every live key is routed.
- **Parity covers all seven states: 211 checks pass.** New checks include colour patches read along a gradient, `x`/`right_inset` measures and the peek's visible width.
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

- **The rail, top to bottom:** the **inbox** (what waits on the human: blocked agents, agents that finished unseen and aren't dismissed, bots waiting on an answer, and bots with an unread reply), the agents capsule (the **agents** list), the **Bots** button while lasso has bots (§23), the **terminal** (§22), the **Browser** button (§24), the **+** (the New form, §20) and the gear (**settings**; its tokens and parity anchor keep the name `ellipsis`). The tools capsule is gone.
- **Voice is removed for now.** The capture, transcription and recording UI are gone, along with the V binding, the microphone permission and every voice and recording token. `/api/transcribe` (S2) and bot-send `meta` (S3) stay on the server, unused by the app. The voice code is in git history (`mac: M6 and M7 …`).
- **"Bot", never "assistant", and no default bot.** The app has no single bot of its own; that will come later. A bot's chat opens from the inbox, or from its pane in the agents list. The default-bot setting and its Create button are gone, from the app and from the server (ui_state `default_bot` was removed before release). So brief B.5's "ensure the app's bot exists" is deferred.
- **Agents are grouped by herdr workspace and named by their label, else their tab name** (herdr.dev/docs/agents, "custom agent labels").
  - The server now carries herdr's label in the fleet listing as `hostPane.agent_label`, from `agent.list`'s `name`.
  - Until that reaches production, tab names already name the agents in titan's Agents workspace (stub, clem, jessica, …).
  - A pane that is a bot's (by `pane_id` or label) is listed as the bot, counted once in the badge, and opens the bot's chat.
- **The rail and its pop-outs are on every Space**, over full-screen apps, and shown on the one the user is on (§16, "Spaces").
- **Parity's named states** are now rail-collapsed, rail-expanded, inbox-open, card-open, list-panel-open, chat-open and settings. "recording" went with voice, and "onboarding" is settings, which still opens itself on the first launch.

## 13. Inbox

The Inbox is the surface this app exists for: **the latest messages across lasso's agents in one place, where the user can address them.** It is the rail's top button, and its badge is the rail's one signal. Milestone M8 (§6).

### 13.1 Scope

**claude and omp only**: claude and omp panes (`hostPane.agent` is `claude` or `omp`), plus omp bots. No other harness enters the Inbox, even when herdr reports it blocked, and nothing here is designed for one.
- A bot of either runtime enters on the pane it runs in (by `pane_id` or herdr label, `FleetModel.bot(for:)`), once. Its finished turn is its last reply (`last_at`), not its pane's `transcript_at`. A claude-runtime bot is read and answered through the pane chat routes, an omp bot through its own.
- **One rule, lasso's state only.** The Mac (`FleetModel.inbox`) and the phone's simple app (`src/web/src/simple/inbox-rule.ts`) compute membership from what lasso serves and nothing a device keeps: the fleet, the bots, ui_state's `dismissed_cards` and `inbox_since`, and `/api/notices`. So both list the same entries and the rail's badge equals the phone's count. Both run the same cases, `mac/Spec/inbox/rules.json` (the phone reads a copy, kept equal by the Go test `TestInboxRulesCopyInSync`).

### 13.2 Membership and order

| Group | An item when | Read from |
|---|---|---|
| **1. Waiting on you: ask** | A pane has `agent_status == "blocked"` and its chat's newest tool item carries an open `ask` (no `selected`, no `custom`). Or an omp bot's pane is listed and the bot has `pending_ui`. | `GET /api/all-panes` (`src/hostpanes.go:923`; status from `src/panestatus.go`), then `GET /api/chat?pane=&host=` (`src/chatview.go:1823`, `ask` on `chatTool`, `:100-175`). Bots: `GET /api/bots`, then `GET /api/bots/<name>/chat` (`src/botsapi.go:578`), `pending_ui` (`src/botomp.go:125`). |
| **1. Waiting on you: no choices** | A pane is blocked and its chat has no open ask (§13.5). | The same reads. |
| **2. Finished turn** | A pane is not working (`done`, `idle`, or a status herdr does not name) and its `transcript_at` is newer than lasso's `inbox_since`. For a bot's pane: the bot's `last_kind == "agent"` and its `last_at` is newer than `inbox_since`. Either way, not acknowledged at or past it. | `transcript_at` on the pane row; `last_at` on the bot view; `inbox_since` in `GET /api/ui-state`. |
| **3. Notice** | lasso keeps it and nobody has acknowledged it. | `GET /api/notices` (`src/inboxnotices.go`), and the SSE `notification` event for a new one (same `id`). |

**Order:** group 1, then 2, then 3. Inside each group, newest first: by `transcript_at`, or by `last_at` for a bot, or by arrival for a notice.

**What a row shows**
- Every row has the state dot, avatar, the agent's name (label, else tab name), its workspace, and how long ago.
- An ask shows the ask itself (§13.3).
- A finished turn shows the agent's last message, two lines, from the newest agent item of the chat.
- A notice shows its title and detail.

**Chat reads are bounded.** The chat of a pane or bot is read only while it is a group 1 or 2 candidate.
- It is re-read only when its `transcript_at`, its `last_at` or its `pending_ui` changes, or on a `bot-chat` nudge for that bot.
- Reads are cached per (item, `transcript_at`), with at most 4 in flight.

**Notices** are lasso's own notices and agents' messages (`lasso_notice`, `agent_message`), each with an `id` and an `at` (unix ms).
- lasso keeps the last 50 in memory (`GET /api/notices`, oldest first), whether or not a client was open when one was raised. A lasso restart starts the list empty.
- The app reads the list every 30 s and adds one heard live (the `notification` event) at once, deduped by `id`.
- Acknowledge is shared: `notice:<id>` in `dismissed_cards`.

**The baseline is lasso's:** `ui_state.inbox_since` (unix ms), set once by lasso (at boot, or by the first read that finds none) and never by a client. A finished turn counts only after it, so a new lasso starts with what is waiting, not with history, and every device starts from the same moment. Against a lasso too old to have one, the app counts from its own launch.

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
  - Acknowledge
  - Kill
- The app never fabricates options and never types keystrokes into the pane.
- The card opened from this row has no composer send while the pane is blocked.

### 13.6 Kill and Acknowledge

**Kill ends the agent.** It is for panes only, behind a confirm step on the row (`X`, then `X` or ⏎ again; `esc` backs out).
- A pane with an agent record: `POST /api/agent/close {pane_id, agent_id, host, remove_worktree: false}` (`src/closeme.go:34`).
- A pane without one: `POST /api/close?host= {pane_ids}` (`src/main.go:1558`).
- A bot has no Kill in the Inbox. Stopping a bot is managed in lasso's Bots view.

**Acknowledge changes nothing on the agent.** (The user named it: acknowledging says you have seen it, where "discard" read as throwing it away. The wire names stay S1's, `card_dismiss` and `dismissed_cards`, which the web `/m` app reads too.) It clears the user's attention on that item: it never closes, stops, sends to or writes anything to the agent. It is S1's `dismissed_cards`, shared through ui_state so every client agrees. It writes `POST /api/ui-state {client_id, card_dismiss: {key: at}}`, where the key and value depend on the item:

| Item | Key | Value | It returns when |
|---|---|---|---|
| Pane (ask, no choices, finished turn) | paneKey `host\u0000pane_id` | its `transcript_at` | `transcript_at` moves past the value: a new question, or a new turn |
| omp bot ask | `bot:<name>#<pending_ui id>` | `0` | a different question opens (a new id, so a new key) |
| omp bot finished turn | `bot:<name>` | `last_at` in unix ms | `last_at` moves past it |
| Notice | `notice:<id>` | its `at` | never; a notice is one-shot |

- The server takes any key of 1–300 bytes (`mergeDismissedCards`, `src/hostpanes.go:711`), so bot keys need no server change.
- The 256-entry cap drops the smallest value first, so bot-ask keys (value `0`) go first. Losing one only brings its item back.
- **An acknowledged item is hidden, not deleted:** the rule above decides on every update whether it shows.
- Against a lasso without S1 (no `dismissed_cards` in GET), Acknowledge is hidden, as the card's is today.

### 13.7 Seen, the badge and the empty state

**Seen and acknowledged are separate.**
- **Seen** is per-item and kept on this Mac (UserDefaults), like the bot read marks of §12. Seen is about this screen.
- **Acknowledged** is shared (§13.6). Acknowledge is about the user's attention everywhere.
- Each mark is the item's `transcript_at`, `last_at`, ask hash, or arrival time.

**The Inbox opens on its newest message, never a list.** The rail's inbox button opens the card on the first item in Inbox order (the reference prototype's behaviour, which the user chose). `J`/`K` page through the rest in that order, and the unread bar shows where you are. With nothing waiting, a small panel shows the empty state. Pressed again while the card is open, the button closes it.
- A bot's item opens on the pane the bot runs in. For an omp bot, the card reads and answers through the bot's routes: its `pending_ui` is the next steps, and a message goes through `chat/send`. A bot has no Kill in the card.
- The Inbox panel's rows remain only for an item with no pane to show, such as a bot whose pane is gone.

**What counts as seeing an item:**
- An item is seen when the card lands on it: opened from the rail, or paged to with `J`/`K`.

**The badge counts the inbox:**
- It shows the number of messages the inbox holds: entries plus notices (`FleetModel.badgeCount`), "99+" past 99. It is hidden when the inbox is empty. It is the phone's Inbox count for the same lasso: seen marks never change it.
- It is red while one of them waits on the human, and green when only finished turns and notices are left.
- **The peek's dot means "new since you looked":** it shows when at least one item that is not acknowledged is unseen, in the colour of the most urgent such item.
- Seen items stay listed until they are answered, acknowledged, or no longer qualify. Seeing an item only takes it out of the peek's dot.

**Empty state:** "Nothing waiting on you." with a second line: "Questions, finished turns and notices from your claude and omp agents land here." An empty Inbox is the normal state, not an error, so it has no illustration and no call to action.

### 13.8 What exists, and what M8 changes

**Built (§12):**
- The rail's inbox button and badge.
- `InboxModel`, which lists blocked and done agents, blocked bots and bots with an unread reply.
- Acknowledge of a pane through `dismissed_cards`.
- Per-Mac bot read marks (`BotReads`).
- The inbox-open parity state.

**M8 changes:**
- The claude/omp filter, with claude-runtime bots entering as panes.
- Asks rendered and answered in place, with the draw-time capture.
- Blocked with no choices.
- Notices.
- An omp bot's `pending_ui`.
- Bot Acknowledge keys.
- Per-item seen state and the badge rule.
- `J`/`K` and the empty state.
- S4, if taken.

### 13.7a Keys in the inbox

**The inbox card's keys:**

| Key | Action |
|---|---|
| `j` / `k` | Next / previous item, in Inbox order (wrapping). The card lands on it, the unread bar shows where you are, and it is marked seen. |
| `x` | Kill, behind its confirm: a first `x` arms it ("Press X again"), a second kills, and any other key disarms. A bot has none. Until lasso answers (`/api/agent/close` can take seconds), the button reads "Killing…" with a spinner and is off, the conversation and the choices dim (`card.killing`) under "Killing <agent>…", and every key but `j`, `k` and `esc` waits (`CardModel.killingKey`, so paging away shows the other card as it is). A failure clears the wait and says why in the same place. Parity state `card-killing`. |
| `a` | Acknowledge (S1): writes the entry's own key, and nothing to the agent. It returns when the item has something new. Off while the reply composer holds a draft: the button is greyed with the hint "Send or clear your reply first", `a` does nothing, and a focused composer types it. |
| `r` | Rename the agent in place, as a click on its name does (§17): ⏎ or a click away keeps the name, esc keeps the old one. A bot has none. |
| `l` | Put the cursor in the reply composer, draft or not. Every key is then text until esc leaves the field. |
| `1`–`9`, `→`, `↳` | Pick a choice, answer with it, or edit it into your own words. |
| `⏎` | To the composer when it is empty, else send what it holds. |
| `u` | Attach a file (the paperclip; U for upload, since `a` is Acknowledge). |
| `t` | Show the agent in herdr: focus its pane and bring the terminal window forward (§22). Not in the hover-lent set, since the terminal takes the keyboard. |
| `⌘.` | Stop: interrupts the agent's turn (one Escape through `/api/chat/stop`, or an omp bot's `chat/stop`). It works while the agent is working or starting, even with the composer focused, since a text field would take `⌘.` as esc. |
| `esc` | Close. |

The keys are `KeyBinding`s in `Theme/Keys.swift`; `CardKeys` (`Card/CardController.swift`) routes a key to a `CardKeyAction` among a surface's set (`cardSet`, `hoverSet`, `windowSet`, `previewSet`), and `CardModel.perform` does it, so the card, the agent window and the preview cannot drift apart.

**A resolved entry moves the card on.** Kill and Acknowledge share one path (`CardModel.resolved`). Once the action succeeds:
- The entry leaves the inbox at once, without waiting for a poll. A killed pane is in `FleetModel.killed`, hidden from the whole fleet until a poll stops listing it. An acknowledged entry is in `FleetModel.acknowledged` with the mark it had, hidden until ui_state catches up or something new gives it another mark. So a stale poll cannot bring either back.
- The card shows the next entry in the Inbox order taken when the key was pressed, or the previous one when it was the last. With none left, it gives way to the empty inbox (`onInboxEmpty`).
- A kill from an agent window closes that window.

**How the card gets the keyboard:**
- **By click or keyboard:** the inbox shortcut, ⏎ on the summoned rail's inbox button, or a click on the inbox button gives the card the keyboard in the same press. Opening never puts the cursor in the composer, so the next `j` pages. None of these pins the card: when the pointer leaves it closes (§16), and a shortcut's card first waits for the pointer to come (or esc, or a click elsewhere).
- **By hover:** an inbox opened by hover is shown without the keyboard (§16). **While the pointer is over the inbox (the rail or its card)**, the card lends itself the keyboard for `j`, `k`, `x`, `a`, `esc`, `r` and `l` only. So `j`/`k` work the moment the inbox opens under the pointer, with no move onto the card.
  - **`r` and `l` take the keyboard** (`CardKeys.takesKeyboard`). They put the cursor in a field (the name, the reply), so the card first becomes the user's as a click would make it (`keyMode` full, nothing lent), then acts. The pointer drifting off the card onto the rail keeps it. The pointer leaving the rail and the card closes the card as ever: a rename in progress is committed, as a click away commits it, the reply stays as a draft for that agent's next card, and the keyboard goes back to your app. A bot's card has no rename, so its `r` hands the keyboard back like any key outside the set.
  - Any other key hands the keyboard straight back. That key is lost, since a key cannot be re-sent to another app without Accessibility, and the card keeps it back until the pointer leaves and returns.
  - The keyboard is lent **once per pointer visit**. If it goes away (a click in another app), the card does not take it again until the pointer leaves and comes back.
  - When the pointer leaves the card, the keyboard goes back to your app. A click on the card makes it fully yours until the pointer leaves the rail and the card, which closes it.
  - **Why this is safe:** the pointer is visibly on the card, so you are looking at it.
    - Paging and esc are harmless.
    - Acknowledge acts at once (the user chose no confirm for it): it never touches the agent, and the item comes back when it has something new. Kill needs a second press.
    - Typing meant for another app is cut off at its first key outside the set. A stray word can page the card or acknowledge one item. A word that starts with `r` or `l` takes the keyboard into the name or the reply, so the rest of it lands there. A rename keeps it only on ⏎ or a click away. A reply is sent only on ⏎, so a line ended with ⏎ would reach the agent. The user chose this so that R and L work on a hovered inbox.
- **Keys work but are not drawn.** The table above is the reference; no keycap shows on any control, hovered or with ⌘ held. Each button's accessibility label names its key.
- **A focused text field wins.** A key typed into the composer stays text, `j` included, even as the first character. Text focus is read from the window's first responder, not only from the field's own "did begin editing" (which fires only after the first character).

### 13.8a As built (M8)

- **One computation:** `FleetModel.inbox` serves the card, the badge, the peek's dot and the arrival sound.
  - **Waiting:** a claude or omp pane that is blocked, or an omp bot with `pending_ui`.
  - **Finished:** a turn newer than lasso's `inbox_since` and not acknowledged; a bot's turn is its last reply.
  - **Order:** newest first within each group.
- **Seen marks** are per entry and kept in UserDefaults (`inboxSeen`). They light the peek's dot only; the badge counts every entry plus every notice.
- **Draw-time capture:** `CardModel.captured` freezes the question when it is first drawn, and an answer is built only from it.
- **"Other: answer in your own words"** (where the dialog has an Other row) turns the composer into the answer (S4); ↳ does the same with a choice's words. An omp bot's `input`/`editor` request answers the same way.
- **Blocked with no readable choices** shows "Waiting on you in the terminal" and Focus in herdr.
- **Acknowledge keys:** Acknowledge writes the entry's own key (§13.6).
- **Notices** come from lasso's list (`/api/notices`) and the event stream, and list in the inbox panel, each acknowledged with × on every device.
- **Not yet built:**
  - Answering a multi-question ask in one request; only the first question is answered, and the dialog then shows the next.
  - The card's Other row, since the card shows three choices; ↳ covers it there.

### 13.9 Harness and tests

**Parity states:**
- **inbox-open**: the card the Inbox opens on, its newest item (a bot's question), with the unread bar over the rest. Its fixture grows to cover: a claude single-select ask with a `recommended` option and a folded `preview`, an omp multi-select ask, a blocked pane with no ask, a finished turn, an omp bot's `select`, and a notice.
- **inbox-ask-picked**: a preview unfolded under its pick.
- **inbox-empty**: the empty-state panel.
- Each state gets geometry, colour and accessibility labels, and a check that every live key is routed to its action.

**Contract:** the `chat_payload` golden already carries an `ask` (`mac/Spec/contract/chat_payload.json`). M8 adds `pending_ui` to the bot chat golden and a `notice` golden, written by `TestDesktopContract`.

**Swift tests:**
- The shared cases (`mac/Spec/inbox/rules.json`, `InboxRulesTests`), which the phone's tests run too.
- Membership and order, including the harness filter and a bot's pane listed once.
- The capture: the answer body equals the draw-time capture after the model has updated underneath it.
- A changed ask drops the pick.
- Acknowledge's return rule for each key kind.
- Seen versus acknowledged, and the badge.
- `J`/`K` marking seen.
- No action ever calls `/api/chat/send` for a blocked pane.

## 14. Connecting to a dev lasso

A Mac uses one lasso. Settings has three tabs, each laid out as macOS's grouped settings are: titled groups, each a rounded card of rows split by hairlines, a row's name and why on the left and its control on the right (switches for on/off), footnotes under a group. The tabs carry icons, and the chosen one is lifted onto its own pill.
- **Notifications:** the arrival sound and Notify me about.
- **Shortcuts:** opening the inbox, the new-agent shortcuts, and Accessibility, which only the double-tap shortcut needs.
- **Info:** first the lasso, named by the machine it runs on ("titan"), with its release number (never a build's `(dev)` tag), the address this Mac reaches it at, sign-in, herdr's version, how many machines it drives, and **Advanced Settings**, which opens the lasso's web app at `/?open=settings` (its sidebar on Settings › General, read once on load) in the default browser. **Change…** opens the address field for moving to another lasso (§18); it stays closed otherwise. A sign-out opens Settings here, and so does opening Settings while no lasso answers. Then the app: its version and Check for updates, **Show in the Dock** (the app turns regular, with a menu bar of Settings… ⌘, and Quit ⌘Q; clicking the Dock icon opens the inbox), **Show in the menu bar** (the EXA monogram as a template image, `Resources/Brand/monogram-template*`, with the inbox's count, and Open Inbox, New Agent…, Settings…, Quit), both off by default and applied at once (`AppPresence`, UserDefaults `showInDock`/`showInMenuBar`), **Start at login**, and **Quit Lasso**.
- **Start at login** (`Platform/LoginItem.swift`) is `SMAppService.mainApp`: the switch calls `register()`/`unregister()`, and its state is macOS's `status`, read each time Settings shows, since System Settings › General › Login Items changes it too. Nothing caches it.
  - **On by default, once.** Onboarding's Done (or Open Settings) registers the app, since the rail is meant to be always there; macOS shows its "added a login item" notice then. UserDefaults `loginItemAutoRegistered` records that the one automatic registration happened, and any use of the switch sets it too, so turning it off sticks.
  - `.requiresApproval` keeps the switch on with **Open Login Items** (`SMAppService.openSystemSettingsLoginItems()`), and the group's footnote says to allow Lasso there. A refused change is a footnote naming macOS's reason.
  - **A dev build** (Info.plist names no update feed) never touches SMAppService. Its row reads "Started by the dev launch agent", because `Scripts/agent.sh`'s LaunchAgent starts it at login and restarts it after a crash. So minime never runs two.
- **Rail position** has **Center vertically**. The rail can be dragged along its edge, and its place is kept as a fraction of the travel (UserDefaults `railPosition`). The button forgets it, as a fresh install has none, and moves the rail to the vertical middle of its screen's visible frame on the same edge (`RailController.centre`). Whatever is open beside the rail follows. The button is greyed while the rail is within a point of the middle (`RailController.isCentred`), and the row then says so.
- **Quit Lasso** says what happens next: "It starts again at your next login" while Start at login is on (or in a dev build), else "Open Lasso again to bring the rail back."
- **Switching is in place.** Every live action shares one `LassoClient`. `FleetSync.retarget` repoints it, empties the fleet, restarts the event stream and refills from the new lasso, and the choice is remembered. Open panels close, since they belonged to the old lasso.
- **From titan:** `mac/Scripts/dev.sh use dev` finds the `mise run dev` backend (this worktree's, else the first on 8190–8199), checks it answers through the tunnel and relaunches the app on it. `use prod` goes back, and `use PORT` picks one.
- **The tunnel** (`lasso-desktop-tunnel.service`) forwards minime's 18190–18199 to titan's 8190–8199, so a dev lasso that bumped past 8190 is still reachable.
- **A reinstall keeps the choice.** `agent.sh install` sets `baseURL` only on a fresh Mac or when `LASSO_BASE_URL` is given.
- **A dev lasso is not a sandbox.** It drives the same herdr and reads the same `lasso.db` as production, so the agents in it are real, and Kill and answers act on them. Two lassos of different versions on one `lasso.db` also rewrite each other's `ui_state`: an older one drops fields it does not know, such as `dismissed_cards`.

### First run

The first launch shows the **first-run panel** (`Onboarding/`), never Settings. It comes back on any later launch while this Mac has never had an answer from a lasso (UserDefaults `connectedOnce`, set by `FleetSync` on its first good fleet read) and the configured lasso does not answer. It is a `RailPanel` at the floating level, centred high on the screen and moved by dragging its header, like the New Agent form. Every number is `onboarding.layout`; the heading is `font.display`.
- **Checking:** before showing a field it asks, quietly, the configured lasso through the live client (credentials and all), then lasso's own port on this Mac (`127.0.0.1:8090`) anonymously. One that answers is used and the panel goes straight to step 3; on a later launch it then does not show at all.
- **Step 1, Connect:** the app's icon over a soft glow, "Connect this Mac to your lasso", one address field with the keyboard, and Connect (⏎). Under it, three kinds of place a lasso runs, each with an example address a click puts in the field: on your tailnet (no sign-in), behind Cloudflare Access (you sign in in your browser), on this Mac. A failure says why under the field and keeps the address.
- **Step 2, only when asked:** a browser sign-in shows "Finish signing in in the window that opened" with Cancel, which closes the sign-in window. A UI_AUTH lasso shows a user and password form (⏎ in the user field moves to the password), checked before it is kept (§18).
- **Step 3, Connected:** "Connected to <machine>" (the local pane's `host_label`, else `/api/hosts`' hostname), its release and address, then three tips: the inbox, the + (with the scratch shortcut) and the gear. Each shortcut is named in plain text, read from `ShortcutSlot` as set now; an Off shortcut is not named. **Open Settings** and **Done** (⏎) end the first run for good (UserDefaults `onboarded`).
- **esc** puts the panel away. Before step 3 that leaves `onboarded` unset; at step 3 it is Done.
- **While no lasso answers** (`FleetModel.offline`: the fleet read failed before any answer from this lasso, or twice in a row after one), the inbox's badge is a grey "!" and the peek's dot is grey. A click or a rest on the inbox, and the inbox shortcut, open the first-run panel instead of an empty inbox.
- **Parity states:** `first-run` (step 1), `first-run-connected` (step 3, the default shortcuts) and `rail-offline`.

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
- **Resting opens** (`rail.hover.item_open_ms`, 117 ms, f655→f662): the inbox, the agents capsule and settings open by hover alone. A hover-opened panel is ordered front **without the keyboard**, so typing in another app never lands on the card's one-letter keys.
- **The rail's pop-outs live under the pointer** (`Glance`, `AppCoordinator.pointerTick`): the inbox's card or panel, the agents column, the Bots column (§23), their hover previews and a bot's chat. However one opened, it closes `rail.hover.panel_close_ms` (317 ms, f707→f726) after the pointer has left the rail and every pop-out. Moving from the rail to a panel inside that time does not flicker.
  - **A click on a rail button pins nothing.** A click on the inbox button gives the card the keyboard (to reply, say), and one on the agents capsule gives the column the keyboard (↑/↓/⏎). Either still closes when the pointer leaves. A typed reply stays as a draft for that agent (`CardController.drafts`), a rename in progress is committed, and the keyboard goes back to the app that had it.
  - **Persistent windows are what a click opens:** a row of the agents column opens that agent's own window (§19), and a row of the Bots column that bot's (§23), which stays, floats over full-screen apps and follows across Spaces. The herdr window (§22) and the Browser window (§24) are the same kind of window; Settings and the New form are windows too.
  - **A pop-out opened with the pointer elsewhere** (the inbox shortcut, ⏎ on the summoned rail, the Dock icon, the menu-bar icon's Open Inbox) waits for the pointer. It goes on esc, once the pointer has come onto the rail or a pop-out and left, or on a click elsewhere. A global mouse-down monitor and a local one hear that click (`ClickAwayWatch`). They run only while such a pop-out waits, and a click on the rail, on a pop-out, or on a menu over one keeps it (`ClickAway`).
  - **Opening the herdr window** (the rail's terminal button, **T**, a header's button, a new terminal) puts the pop-outs away first, so it comes up alone.
- **Pop-outs go on the rail's screen** (`Screens`, `originBesideRail`), and windows on their anchor's (an agent or bot window beside the column, the New form and the first-run panel under the pointer). Never on `NSScreen.main`'s: it follows the key window and is nil while none is key, which in an app made of non-activating panels is most of the time. Placed against `.zero` then, every pop-out opened below the screen, out of sight but open: nothing showed on hover or click, and the rail stayed out for them (Settings, which stays until closed, held it out for good).
- **Spaces: the rail and every pop-out are on the Space the user is on**, another app's full screen included (`RailPanel`, `RailPanel.orderIn`).
  - Each panel is a non-activating `RailPanel` made with `[.canJoinAllSpaces, .fullScreenAuxiliary, .stationary, .ignoresCycle]` at `.statusBar`, before it is first ordered in (a behaviour set later applies only from the next order-in). Never `.moveToActiveSpace`, never a child of another window, and `hidesOnDeactivate` off. Agent and bot windows, the herdr and Browser windows (one `AgentWindowPanel` class for all four), the New form and the first-run panel are the same at `.floating`, without `.stationary`.
  - **Every order-in goes through `orderIn(takeKey:replaced:)`**: `orderFrontRegardless`, then the keyboard if asked, so a hover never activates the app or switches Spaces.
  - **No Lasso window goes full screen.** A panel on every Space cannot have a full-screen Space of its own, so the herdr and Browser windows, like the agent windows, have no full-screen button or behaviour (no `.fullScreenPrimary`). The user keeps them over a full-screen app instead, sized as they like.
  - **A panel found off the active Space is replaced** (`isOnActiveSpace` false right after ordering in). When a full-screen Space of the app's own ends, macOS pins every Lasso window that is off screen at that moment to one ordinary Space. `.canJoinAllSpaces` stops applying to it, and setting the behaviour again, `.moveToActiveSpace`, `setIsVisible` or a close and reorder do not undo it (measured on minime, macOS 27). A window that was on screen throughout (the rail, an open agent window) or is new is not pinned. Nothing in the app goes full screen, so this is a guard: the replacement takes the old panel's content view, settings, frame and handlers, and its controller holds it from then on (the card's key notices, a column's hover and a page window's keyboard watch read the panel of the moment).
  - `SpacesTests` checks it on minime's Aqua session. A stand-in window goes full screen and closes with another app full screen, then each pop-out must open on that app's Space. The herdr and Browser windows, opened first, must still be shown and on the active Space after another app goes full screen (a Space change), take the keyboard on a click there with that app in front, and open there. A seam pins any panel, and each must be replaced when shown. Without full screen on the Mac the full-screen checks check nothing.
- **Settings** a hover opened is a glance until clicked: it closes once the pointer has left it and the rail, and a click makes it stay.
- **The rail hides** 370 ms after the pointer leaves (`rail.hover.close_ms`, f959→f981), unless something beside it is open (`RailController.keepOpen`): the card or inbox, the agents or Bots column, either hover preview, a bot's chat, Settings.
- **Panels fade in over 117 ms, ease-out, sliding 4.5 pt away from the rail**, and fade out over 67 ms (`motion.card_open`, f662–669; `motion.card_close`, f726–730). This replaces the brief's 70 ms.
- **Not taken:**
  - The tools capsule and its tooltips ("Start recording", "Take a screenshot of a region, then ask about it") are left out, because the user removed the tools capsule and voice (§12). The tooltips' lesson stands: a control's label says what it does.
  - Tooltips over the inbox, agents and settings are left out too, because resting on those opens them at 117 ms, before a tooltip could be read.

**Hover comes from polling, not tracking areas.** The steward asked for `NSTrackingArea` with `.activeAlways`. The app instead polls `NSEvent.mouseLocation` at 20 Hz against `RailMetrics.itemFrames`, the same frames the rail draws (a test checks them against the rendered anchors). It does so because a non-activating panel of a background app gets no hover events from SwiftUI, and polling also drives the reveal, the drag and the glance close from one loop.

**A column's hover is one map and one highlight** (`ColumnHover`, the agents and the Bots column alike).
- Each tick maps the pointer to a row through AppKit: the pointer goes into the column's scroll view's document (`NSView.convert`), where each row reported the frame it is laid out at (`ColumnRowFrames`, in the scrolled content's own space). Those frames do not move when the column scrolls, so nothing waits on SwiftUI to report geometry again after a scroll.
- The row it finds is both the highlight and what the preview follows. A row is lit when it is under the pointer, or, after ↑/↓ (J/K in the Bots column), when it is the keyboard's, until the pointer moves. Never both: a row clicked open is selected for ⏎ but not lit once the pointer moves on.
- **Why:** the earlier map read each row's window-space frame, which SwiftUI reports again only after a scroll has settled into a layout pass. A momentum or elastic scroll can end without one, so the map lagged the drawn rows and named the row next to the one under the pointer. That row was lit and previewed, the row under the pointer was lit too as the selection a click had left, and a click on the row (which puts the preview away) marked the wrong row as dismissed, so no preview came back: two rows lit and none previewed.
- The timer runs in the common run-loop modes, so a scroll or an open menu does not stop it.
- **It looks at once, not at the next poll,** whenever what it maps through changes while the column is open: as the column opens, when a layout lands new row frames (a fleet update draws an agent under the pointer, or the first open is laid out on the next turn), and when `RailPanel.orderIn` replaces the panel. A pointer already resting on a row as the column opens is lit then and previews one rest (`item_open_ms`) later, with no move. Until SwiftUI has made the column's scroll view, no row is under the pointer: the panel's top is not where the rows start (the Bots column's start below its header). `ColumnHoverOpenTests` covers the open, the late layout, the replaced panel and the poll with no key and no events.

**Parity states** `rail-hover-inbox` and `rail-hover-agents` check the overlay on the hovered control and the untouched neighbour. The motion pass checks the 117 ms and 67 ms curves.

## 17. Shortcut, sound and brand

- **Inbox shortcut**, in Settings › Inbox (UserDefaults `inboxShortcut`):
  - **Default ⌥Space**, a key combination through Carbon's `RegisterEventHotKey`, which needs no permission.
  - **⌥⌥, ⌘⌘ or ⌃⌃**, a double tap of the modifier alone: each tap at most 300 ms, the second ending within 400 ms of the first (`keys.inbox_double_tap`). Any other key or modifier in between starts over. A double tap reads modifier keys in every app through a global event monitor, which macOS delivers only with **Accessibility**. So Settings says so, with a button to open that pane, only when a double tap is picked and Accessibility is not granted, and never otherwise.
  - **Record…** takes any other combination with ⌘, ⌥ or ⌃. **Off** turns it off.
  - The shortcut opens the inbox with the keyboard in it; pressed again while the inbox has the keyboard, it closes it.
- **Arrival sound**, in Settings › Inbox (UserDefaults `inboxSound`, default Whoosh):
  - Three bundled sounds made with ElevenLabs on fal: Whoosh, Marimba and Lasso (`mac/Resources/Sounds/README.md` has the prompts). They are levelled to a −4 dBFS peak, and choosing one plays it.
  - **Your own sound:** Upload picks an audio file (at most 1 MB) and stores it on lasso: `PUT /api/desktop/sound` keeps the file at `<lassoDir>/desktop/sound`, typed by its bytes, and ui_state's server-owned `desktop_sound` names it with its sha256. Every Mac on that lasso gets it. The app caches the file in `~/Library/Application Support/Lasso/Sounds/custom-<sha256>` and downloads it only when the record's hash differs from the cached one, so playing it never touches the network. Remove deletes it from lasso. Until the cache has it, the custom choice plays Whoosh.
  - It plays when the inbox holds an item, a new turn or a new question it did not hold before (`InboxItem.arrivalKey`). The first reading is a baseline, and arrivals within 2 s make one sound (`live.arrival_sound`).
- **Settings footer**: the Execution Associates lockup as isb ships it (`Resources/Brand`), tinted like secondary text, linking to execution.associates.
- **Icons** are the web app's: Lucide (lucide.dev), which the web draws through `lucide-react`, so a thing wears the same icon on the Mac, the web and the phone (Bots `bot`, the inbox `inbox`, a terminal `square-terminal`, the browser `globe`, settings `settings`, attach `paperclip`, stop a filled `square`, the agents' Pin `pin`, a channel card's `mail`/`message-square`/`clock`/`phone`). Where the web has no such thing, the nearest Lucide icon stands in (an agent's avatar: `asterisk` for Claude, `code-xml` for Codex, `pi` for omp). Icons are image assets, not code, so §0's rule is untouched. `mac/Resources/Icons` holds the SVGs and Lucide's license (ISC, with Feather's MIT for the icons derived from it); `build-app.sh` copies them into the bundle. `mac/Scripts/lucide-icons.sh` writes them from the lucide-react that `src/web/bun.lock` pins (1.17.0): it reads each icon's data inside the worktree's dev container and builds lucide-static's SVG from it on titan, accepting only plain shapes. `enum Lucide` (`Theme/Lucide.swift`) is the list; add a case and run the script. They are drawn as template images tinted like text, at Lucide's stroke 2 on its 24-unit grid, sized so the glyph spans what the SF Symbol at that point size did (`icon.lucide`); the send arrow (2.5) and small checks (3) are bolder, as the web draws them. **Window controls stay SF Symbols** so they feel native: the close ✕ on every window and pop-out, minimise and expand on agent and bot windows, and the resize grip (`WindowControl`, the only place an SF Symbol is named; `IconTests` holds the code to that and every case to a bundled SVG).
- **App icon**: the EXA monogram, exactly as `exa/brand-assets/logo/exa-mark-white.png` draws it, on a macOS icon plate in the rail's ink gradient (`Resources/AppIcon.png`). `build-app.sh` packs `AppIcon.icns`.

- **Notifications by kind** (the server side is on branch `lasso-notify-kinds`, `docs/design/notifications.md` there):
  - The Mac is one device. It has a stable id (UserDefaults `notifyDevice`, `mac-` plus 16 characters) and registers as "Lasso on <Mac name>".
  - Settings › Inbox lists **Notify me about** from the server's catalog (`GET /api/notify?device=`), and each On/Off is written per kind (`POST /api/notify/device`).
  - The arrival sound plays only for a kind this Mac wants. Each arrival maps to a kind:
    - a waiting entry is `agent_blocked`
    - a finished turn is `agent_finished` (off by default)
    - a bot's turn is announced once, under lasso's `notifDelivers` rule: as `bot_message` when the Mac takes replies, else as `agent_finished`. When the bot's own `notify` is off, its turn makes no sound. A bot's question is `agent_blocked`.
    - a notice is `lasso_notice` or `agent_message`
  - A choice never hides anything from the inbox.
  - lasso's notices and agents' pings arrive as the `notification` event when the server has kinds, and as the older `notice` event otherwise; the app takes one or the other, never both.
  - Against a lasso without kinds, the list is absent and every arrival sounds.

**An agent's prose is rendered as Markdown** (`Agent/Markdown.swift`), in the window, the hover preview and the inbox card. That covers headings, bullet and numbered lists (nested), fenced code in the terminal face, pipe tables (right-aligned where the delimiter row says so; short cells keep their width, long ones wrap; sideways scrolling only when the table cannot fit), quotes and rules. Inline Markdown (bold, inline code in the terminal face, links) is AttributedString's. Your own messages stay plain.

**Waits are visible.** The conversation's foot shows three pulsing dots and what it waits on (`CardModel.phase`):
- **Loading the conversation** before the first answer.
- **Starting <harness>** while a new agent has no transcript yet.
- **<harness> is working** while it is on its turn: lasso's chat says `running` (an unanswered call in the transcript) or herdr's live state for the pane, read from the fleet's current row and not the one the window opened on, says working. This is never shown while it waits on you.

A message you send shows at once, faded, under **Sending…**, until it appears in the transcript.

**Claude Code's envelopes are not your bubbles** (`Agent/Envelope.swift`, the web chat's `taskNotification` and `channelMessage`), in the window, the preview, the inbox card and the bot chat:
- **`<task-notification>`**, which Claude Code writes as a user turn when a background agent or shell finishes, folds to its summary line in a tool run's look ("Background task finished" when it has none), with its status beside it when that is not `completed` (in the danger colour) and a click unfolding its result. It is the harness speaking, so a preview's latest turn starts after your last real message, not after it.
- **`<channel source=… from_name=…>body</channel>`**, the `incoming` rows a Claude Code channel delivered (mail, texts, Mattermost, a bot's scheduled loops), is a card on the bot's side: a glyph and the source as a label (Email, Mattermost, the triage channel's service, Scheduled, Hotline, else the source's name without `-channel`), the sender and the subject, and the body, clamped to four lines when long with Show more. A claude bot's chat asks for them (`/api/chat?incoming=1`), as the web's Bots view does.
- Both parse in pure functions (`TaskNotification.parse`, `ChannelMessage.parse`); which rows are unfolded is kept by row (`Unfolded`), since rows are drawn afresh on every poll.

**Renaming an agent:** click the agent's name in the inbox card or either agent window (the hover preview and the window), where the pointer becomes a pointing hand, or right-click it there or on a row of the agents list and choose **Rename…**, or press R in the inbox card or either window. A click on a list row still opens the agent. The name becomes a field: it holds the agent's label, or is empty with the shown name as its placeholder when there is none, so a click away with nothing typed changes nothing. herdr's agent names are identifiers (a lowercase letter first, then lowercase letters, digits, `-` and `_`, at most 32), so what is typed becomes one: "Check HN page" is `check-hn-page` (`AgentName`, the same rule as lasso's `agentName`). ⏎ or a click anywhere else (or another app taking the keyboard) keeps the new name, as Finder does; esc keeps the old one; an empty name clears the label so the tab's name shows again. It is herdr's custom agent label, set through lasso's `POST /api/agent/rename`. A bot has no Rename, because lasso finds a bot by that name; bots are renamed in their own settings.

## 20. The New form: agents and terminals (the web app's ⌘O, ⌘⇧O and ⌘I)

The Mac launches agents and terminals the way lasso's web New dialog does (`src/web/src/components/NewDialog.tsx`), over the same routes:
- `GET /api/agent-config?host=` returns the harnesses, the default or last agent and the last repository.
- `GET /api/repos?host=` lists the repositories, and `GET /api/repo-branches?path=&host=` their branches.
- `POST /api/create-agent` creates the agent. It answers as soon as the record, workspace and root pane exist, and the agent boots afterwards. So the new agent's **own window** (§19) opens right away on that pane, and its conversation fills in as it starts. The form gives up the keyboard as it closes, and the window takes it with the cursor in its composer (as L does), so you can write to the agent at once, while it shows "Starting <harness>".

**The form** is titled **New** and has three modes: In a repository, Scratch and Terminal. The form does not remember the last mode: each shortcut opens its own.
- In a repository (a Git agent: repository plus base branch, picked with a filter, since titan has 183 repositories and 502 branches in lasso), or Scratch.
- The prompt: its first line names the agent.
- Agent, model and thinking effort.
- The machine, when lasso drives more than one. Every machine shows by its name: lasso's own machine is "local" on the wire, but by its hostname on screen, because it is rarely the Mac the app runs on. Cards and agent windows always name the machine for the same reason.
- Plan first, where the harness has it.
- ⌘⏎ creates and esc cancels. A failed create says why and keeps the form.
- Dragging the header moves the form. It opens where it was last left (UserDefaults `newAgentOrigin`), or centred high on the screen when that spot is off every screen.

**Terminal mode** is the web's Terminal tab (`NewTerminalForm.tsx`) over `POST /api/create-terminal`:
- **The prompt is the command.** It is optional: blank opens an interactive shell, and several lines run as one script. It is at most 512 characters, the web form's limit.
- **The chips** are the machine (when lasso drives more than one) and an optional working directory. Blank uses the workspace's default, as the web does.
- **Agent, model, thinking and plan-first are not shown.** The button reads **Run**, with ⌘⏎.
- **The body is the web's** (`NewAgentModel.terminalRequest`). The workspace is the host's `default_terminal_workspace` from `/api/agent-config`, else Scratch. It is sent by id when `/api/workspaces` lists it live and by name otherwise, and lasso finds or makes Scratch itself. A bare shell's tab is numbered after the workspace's last. A command's tab is left for lasso to name. `focus` is true.
- **Run** closes the form and opens the herdr window on that host, on the new pane (§22's focus with `reveal`). A command or tab name lasso could not apply shows as the window's subtitle. A failed create says why in the form, and the form stays open.


**Shortcuts** are configurable in Settings › New, like the inbox's:
- **⌥⇧O** for an agent in a repository, **⌃⇧O** for a scratch agent and **⌥⇧I** for a terminal (the web's ⌘I), from any app. ⌘⇧I is taken in many apps (Safari's Mail This Page), and ⌥I alone is a dead key. The user's rule is at most three keys, never four.
- Shift keeps ⌃O for terminals (Claude Code's transcript view, nano's save, vim's jump back, the shell) and ⌥O for typing "ø". ⌥⇧O no longer types "Ø" while it is set.
- **⌘O, ⌘⇧O and ⌘I** do the same while a Lasso window has the keyboard, as in the web app. They are never system-wide, because ⌘O is every app's File › Open.
- The rail has a **+** button ("New…") under the Browser button (§24) and above the gear (settings), and the agents list a **New** button that opens the same form. The **+** opens the form on a click only: hovering lights it but does not open it, because the form takes the keyboard.
- Each chip opens its menu inside the form, toward the side with room (a system popover from a floating panel can land half off screen). Long lists (repositories, branches, a big fleet) and the model have a filter that takes the typing, like the web app's combobox; ↑/↓ move and ⏎ picks. The model's suggestions are a hint, as in the web's EditableCombobox: a typed name no suggestion has is offered first as Use “<name>”.

All four system-wide shortcuts (inbox, new agent, new scratch agent, new terminal) are `ShortcutSlot`s, each stored on its own and recorded the same way.

## 18. Signing in to a lasso

The app uses one lasso at a time and reaches it by address (`API/Auth.swift`, `API/LassoClient.swift`, Settings › lasso). This is step 8 of the lasso-auth design. The server's side is on branch `lasso-auth`.

**The app needs lasso 7.0.0 or later** (`LassoCompat.minimum`). The anonymous probe of `/api/version` compares the release at the front of `lasso_version` and answers `tooOld` for an older one, so Connect refuses it with the reason, the first run's look at `127.0.0.1:8090` passes it over, and a configured lasso that is older does not count as answering; a version with no release number passes. Behind a sign-in the probe cannot see the version, so Settings › Info shows it after: "lasso 2.13.0, too old: needs 7.0.0 or later", with a red dot. The version is re-read every minute, not once, so a first read that lost a race with sign-in, or a lasso updated under the app, does not leave a stale "Not answering".

**Settings › Info's Change… and the first-run panel share one connector** (`Settings/LassoConnector.swift`), so they behave and word their answers the same. A bare hostname means `https://`, and `127.*`, `localhost` and bare IP addresses (a tailnet's 100.x) mean `http://`; a bare hostname nothing answers over https is asked once over http, since a tailnet listener often speaks plain http. Connect asks `GET /api/version` with no credential, sending `X-Requested-With: XMLHttpRequest`, which is Cloudflare's way of getting a clean 401 rather than a login redirect.
- **200:** nothing to send. The address is loopback, or a tailnet listener that knows the device by Tailscale whois. The app switches to it.
- **401 with `WWW-Authenticate: Bearer … resource_metadata="<url>"`:** RFC 9728 discovery, then a sign-in.
  - Read that document. Take `authorization_servers[0]`, then its RFC 8414 metadata.
  - Behind Cloudflare Access this is Access's Managed OAuth: titan answers `knowsuchagency.cloudflareaccess.com`, with registration, S256 and public clients. On a lasso with `MCP_OAUTH` and no edge, it is lasso's own server.
- **401 with `Basic`:** an older UI_AUTH install. The connector asks for the user and password (fields under the address in Settings, step 2 in the first-run panel), checks them with the same probe carrying them, and only when lasso lets them in keeps `user:pass` in the Keychain item `ui-auth` and sets it on the live client (`LassoClient.setBasicAuth`).

**The sign-in:**
1. Dynamic client registration (RFC 7591) as a public native client, with a loopback redirect `http://127.0.0.1:<port>/callback`. The port is the system's choice, and the listener binds 127.0.0.1 only.
2. Authorization code with PKCE S256 in an `ASWebAuthenticationSession`. It is not ephemeral, so the browser's own sign-in session is reused. The RFC 8707 `resource` is the lasso's origin, never the probed path. Access's metadata names the probed path (`/api/version`) as the resource.
3. The browser lands on the loopback listener, which answers "Signed in to lasso" and closes the window.
4. The token exchange.

The access token, refresh token, client id and token endpoint are kept as one Keychain item, `oauth:<origin>`, under the app's service.

**Every request carries the token**, the event stream included:
- `Authorization: Bearer`. When an Access service token is stored (an unattended Mac), the service token takes the edge, and lasso's token rides in `X-Lasso-Token`.
- A token within a minute of expiry is refreshed first.
- A 401 refreshes once and retries. A refresh that fails marks the lasso expired (`AuthState`). Settings opens with **Sign in again**, and everything on screen stays as it was. Sign in again runs the same flow against the same lasso.
- **Sign out** forgets that lasso's tokens.

**Still to settle:**
- Whether Access accepts a loopback redirect from DCR without an allowlist entry (`allow_any_on_loopback`). If it does not, a custom-scheme redirect is the fallback.
- Whether a non-Access bearer passes through Access.

Both are step 2 of the auth plan: `~/.lasso/worktrees/lasso/lasso-auth-notes/step2-access.md`.

## 21. Releases and updates (Sparkle)

Lasso, the Mac app, ships with every lasso release, at lasso's version, as assets on lasso's GitHub release. The app's feed is `https://github.com/execution-associates/lasso/releases/latest/download/appcast.xml`, so it always names the newest release.

**Build** (`Scripts/build-app.sh`):
- Bundles `Sparkle.framework` from SwiftPM's artifacts. The rpath is set in `Package.swift`.
- Signs Sparkle's pieces innermost first, then the app.
- **A dev build** uses the self-signed identity. It has no feed or key in its Info.plist, so it never updates itself, and Settings shows no Check for updates.
- **A release build** gets `LASSO_DESKTOP_VERSION`, a Developer ID `SIGN_IDENTITY`, `SPARKLE_FEED` and `SPARKLE_ED_KEY`.

**Release:** `mac/Scripts/publish.sh [version]` runs on titan once lasso's release `v<version>` exists. It runs over ssh on minime, because the repo is public and a fork's workflow must never reach a logged-in Mac. It:
1. Syncs `mac/` to minime.
2. Runs `Scripts/release.sh <version>` there over ssh, Recap's pattern:
   - Import the Developer ID .p12 from fnox into a throwaway keychain.
   - Build with hardened runtime and timestamps.
   - Notarize and staple the app with an App Store Connect API key.
   - Make `Lasso-<version>.dmg` (volume "Lasso": `Lasso.app` and an Applications link), then sign, notarize and staple it.
   - Run Sparkle's `sign_update` (it ships in SwiftPM's Sparkle artifact) and write a one-item `appcast.xml`.
3. Copies the DMG and appcast back to `mac/dist/release/`.
4. Attaches them to the release with `gh`, the DMG also as `Lasso.dmg`, so `releases/latest/download/Lasso.dmg` is a stable download link. The appcast names the versioned DMG. `--no-upload` stops before this step.

**Credentials:**
- The certificate and notary key are Recap's, the same Developer ID `JF4UNR89NW`, read from minime's fnox: `RECAP_SIGN_P12_B64`, `RECAP_SIGN_P12_PASSWORD`, `RECAP_ASC_*`.
- The EdDSA update key is the Mac app's own:
  - Private half: 1Password `titan/global` field `LASSO_DESKTOP_SPARKLE_PRIVATE_KEY`, and minime's global fnox under the same name.
  - Public half: committed as `Scripts/sparkle-public-key.txt`.
- fnox on minime decrypts with the Mac's ssh key (`FNOX_AGE_KEY_FILE`). In fnox 1.26, `fnox set -d` with a piped value stores only the description, so set the value without `-d`.

The first Developer ID build replaces the self-signed identity. macOS then asks once more for the app's permissions, because TCC keys them on the signing identity.

## 19. Agent windows and hover previews (the user's screenshots, 2026-10-11)

**Hovering an agent's row in the agents list shows its preview,** fixed to the left of the list (`agent.preview`: 508 × 580 pt, 9 pt from the list), with its top level with the row.
- It shows:
  - the header (avatar, name, "state · harness in herdr · workspace", the state as a dot)
  - the agent's side of the latest turn, with tool runs folded into one line under the last call's family icon (the web's `FAMILY_ICON`), the reply as a bubble with inline markdown, and any open question
  - the question's choices
  - the composer
  - the folder and the model
- It appears `rail.hover.item_open_ms` after the pointer rests on the row. It goes `panel_close_ms` after the pointer has left both the row and the preview, clicked or not (§16). It is ordered front without the keyboard.
  - **Every row, every time** (`PreviewHover`, ticked by the list's 20 Hz poll). The rest is timed per row key, so a fleet poll redrawing the rows does not restart it, and moving to another row swaps the preview after the same rest. The row under the pointer is read through AppKit's scroll view into the rows' laid-out frames (§16, `ColumnHover`), and it is also the row lit.
  - **What the preview shows is read each tick**, not remembered. Esc, a click on the row (its window opens) and a click elsewhere put the preview away without the list knowing. A preview put away while the pointer stays on its row does not pop back. It opens again once the pointer has left that row.
- Rows report their frames through a preference key, and the column's `ColumnHover` polls the pointer at 20 Hz, for the same reason the rail does.
- **Once clicked it takes typing**: the click makes its panel key and the composer takes the keys, an agent's and a bot's preview alike.

**One component draws an agent's and a bot's window and preview** (`Agent/Conversation.swift`). An agent's `CardModel` and a bot's `BotWindowModel` conform to `Conversation`: the title, status line and dot, the transcript, the phase, the composer (a bot's forwards to its `ChatModel`), the terminal button, the keys, and five slots for what only one has: the avatar (an agent's harness glyph, a bot's picture), the title (an agent's renames in place), what sits above the conversation (a stopped bot's Start), the choices, and the footer (an agent's folder, model and Kill; a bot's model, Conversations, Start or Stop and ⋯). `ConversationWindowView`, `ConversationPreviewView`, `ConversationWindowController` and `ConversationPreviewController` are generic over it, so the header, the conversation, the composer with paste and drop, ⌘. and ⌘W, the move, resize and collapse, and the preview's click-to-type are the same code for both.

**Clicking a row opens the agent's own window** (`agent.window`: 488 × 632 pt, 60 pt header, 18 pt corners). Bots open one too, since a bot's pane chats through the bot's routes.
- The header has the terminal button, minimize and close, as plain glyphs (no menu; X X kills from the keyboard). The buttons are laid out first, so a long title or status line truncates and never pushes them out of the window.
- **⌘W** closes it, whatever has the keyboard inside it, menu bar or not (`AgentWindowPanel` takes it as a key equivalent and as a key-down: a borderless panel has no close button for AppKit's own ⌘W, and an accessory app no Window menu).
- Below it are the whole conversation (the last 160 items), the choices, the composer and the footer.
- It is a non-activating panel at the floating level, on every Space, and over full-screen apps (`.canJoinAllSpaces`, `.fullScreenAuxiliary`). So it stays put while the user switches desktops or maximizes other windows. It is shown through `RailPanel.orderIn` like the pop-outs (§16, "Spaces"). The herdr window (§22) and the Browser window (§24) are this window around a lasso page (`Page/PageWindow.swift`): the same `AgentWindowPanel`, the same minimize and close (`WindowControls`), the same corner (`WindowGrip`).
- It moves by dragging its header and resizes by its corner (at least 360 × 320). **A double-click on the corner** puts the size a fresh window opens at back (`agent.window.size`), its top-left kept as a resize keeps it and the whole window kept on its screen's visible frame, and forgets the remembered frame: until it is moved or resized again, closing it remembers nothing, so the next open is a fresh window's. The corner's accessibility label is "Resize", its hint "Drag to resize; double-click for the default size", with a "Default size" action. **Minimize collapses it to its header** (`agent.window.collapsed`, 416 × 60 pt) with ⤢ to expand. The top edge stays fixed both ways.
- **Media:** the files an agent shows the human render in its window and preview. That covers Claude Code's `SendUserFile` (lasso puts its files on the chat row as `tool.files`, each typed image, audio, video or file), and the images an agent writes as markdown (`![](path)`, resolved against the session's folder).
  - An image renders inline; a click opens it in its app.
  - Audio gets a play button; video gets AVKit's inline player.
  - Anything else is a chip that opens the file.
  - The bytes come through `/api/file` (`download=1`) from the host that served the transcript. They are cached in `~/Library/Caches/Lasso/Media` per path and showing, since an agent may overwrite a file in place.
- **Choices need a confirm.** A click (or 1–9) only selects a choice; the selected one shows **Answer**, and that button (or →) sends it. An answer cannot be taken back, and the preview appears under a moving pointer.
- **No header menu:** the window's header is minimize and close. **Kill** is a red pill at the trailing end of the folder/model row (`AgentKill`; a bot has none, and neither does a hover preview), behind the inbox's confirm: X or a click arms it ("Confirm kill"), a second kills. Its accessibility label is "Kill <agent>". While lasso closes the pane it reads "Killing…" with a spinner and the window waits as the card does (§13.7a); a failure clears it and the notice line says why. Once the kill goes through, the window closes and the agent's inbox entry goes with it (§13.7a).
- The composer's paperclip (U) attaches any files: each goes through `/api/paste-file` to the agent's machine and its path into the message.
- **Paste and drop attach too** (`Platform/Attachments.swift`), in every composer (inbox card, agent window and preview, bot chat) and the New Agent prompt.
  - The composer's NSTextField uses its own field editor (`ComposerCell` → `AttachingTextView`), so ⌘V and Edit › Paste reach it. A drop works on the field and on its editor alike.
  - `Attachments.plan` decides from the pasteboard: **file URLs first**, then image data (PNG before TIFF, any other image read as TIFF), then text. Screenshot tools like BetterShot and Finder's Copy put the file next to its path or name as text and an icon as an image, and the file is what goes. Image data beats text only when the text is a lone URL or path (a browser's Copy Image); words with a rendering (Word, Excel) paste as text.
  - Raw image data is written to `$TMPDIR/LassoPaste/<uuid>/pasted-<time>.png`, uploaded, then deleted.
  - Each uploaded path is inserted at the caret (the drop point, or the end for the paperclip), set off by spaces. Uploads go one by one, so a failure keeps the paths that already landed. The notice names the file and the reason.
  - Refused before any upload: folders, empty files (lasso refuses an empty body), and anything over lasso's `maxUpload` (1 GiB, `src/main.go`).
  - The New Agent prompt has no agent yet, so its files go to the chosen host through the same route, and Create waits for them.
- One window per agent; a second click brings it forward. Where each agent's window was is kept in UserDefaults `agentWindowFrames`.
- Keys while it has the keyboard (`CardKeys.windowSet`): 1–9, →, ↳, ⏎ (to the composer, or send), L (to the composer), R (rename), U (attach), ⌘. (Stop), and X X to kill. They are the inbox card's, with the same letters, minus paging, esc and Acknowledge: a window has no inbox entry to resolve. A clicked hover preview has the same minus Kill, and esc closes it. A focused composer keeps every key as text except ⌘. (§13.7a).
- **Stop** (the web chat's rule). While the agent is working or starting and the composer is empty, the send button is Stop, a filled square with the label "Stop <agent>". A draft brings Send back.
  - It is in every composer: the inbox card, the agent window and preview, and the bot chat.
  - A press sends one Escape (`CardActions.stop`: `/api/chat/stop?host=` with `{pane_id}`, or an omp bot's `chat/stop`). The button then stays dimmed until a poll shows the turn over, or `live.stop_wait` (10 s) passes.
  - A refusal comes back at once with "Not stopped: <reason>", and an uncertain outcome says to check the terminal.
  - The `agent-window-markdown` parity state renders it.

**Parity states:** `list-hover-preview`, `agent-window` and `agent-window-collapsed`, from the `card-open` fixture.

## 22. lasso's herdr in a window

**The rail's terminal button** (under the Bots button, or the agents, and above the Browser button, `rail.terminal`) opens lasso's herdr in a window of its own. Like the **+**, it opens on a click only, because the terminal takes the keyboard. **Every agent's header** has a terminal button too: the agent window's (beside minimize, `agent.herdr`) and the inbox card's (beside close, `card.herdr`), with **T** as its key. It focuses that agent's pane and shows the window. A blocked agent's **Focus in herdr** opens the window the same way. There is one window: a second open brings it forward.

**As a window it is an agent window** around the terminal (`Page/PageWindow.swift`, §19). It is the same `AgentWindowPanel`: non-activating, at the floating level, on every Space and over full-screen apps, shown through `RailPanel.orderIn` (§16, "Spaces"). So it stays in front of a maximized or full-screen app and comes along when the user changes desktops, and it stays until it is closed.
- **Its header** (`terminal.window.header`, 40 pt, the Browser window's bar height) is the terminal glyph, "herdr on <host>", the last focus's line under it, then minimize and close (`WindowControls`). Dragging it moves the window.
- **Minimize collapses it to its header** (`agent.window.collapsed`'s width), hiding the page; expand puts the size back, top edge fixed both ways, and showing it (the rail's button, an agent's) expands it.
- **The corner resizes it** (`WindowGrip`, at least `terminal.window.min`), and a double-click puts `terminal.window.size` back, its top-left kept, and forgets the remembered frame as an agent window's does. Otherwise its frame is kept under UserDefaults `LassoHerdrTerminalFrame` (a frame AppKit kept for the titled window of that name is read once). A fresh one opens centred on the screen under the pointer.
- **No full screen.** A panel on every Space cannot have one (§16), so there is no green button and no full-screen behaviour.
- **The keyboard without activating the app.** Opening it makes the panel key and the terminal's web view first responder, with the app left inactive, as an agent window's composer is; a click on the page while another app is in front does the same, and that click reaches the page (`LassoPageWebView.acceptsFirstMouse`). Typing, ⌘C and ⌘V (the main menu's Edit items, which the app keeps whether or not it shows a menu bar), ⌘= ⌘- ⌘0 and ⌘W reach it while it is key. `PageWindowTests` checks each of these with the test process never active, which is Lasso's state while another app is in front, and `SpacesTests` checks the panel, its Spaces and a click on another app's full-screen Space.

**The window is lasso's own web terminal**, ttyd's document under `/terminal/<slug>/`, the page the web app frames. It runs in a WKWebView whose page never reaches the network itself. This is how every lasso page runs in the app (`Page/LassoPage.swift`, shared with the Browser window, §24); `Terminal/TerminalBridge.swift` adds the terminal's part:
- **Its document and its `token` fetch** come through the `lasso-terminal:` URL scheme. The app answers each one with a request through its `LassoClient`, carrying the same credentials as every API call.
- **Its WebSocket** is a stand-in, injected before ttyd's script, that hands every frame to a native `URLSessionWebSocketTask`. That task carries the same headers on its upgrade.
- **Both are confined to the one terminal** the window shows. Any other path is refused, so the page cannot reach `/api/file` with this Mac's sign-in.

**Why this approach.** It works for every lasso the app can reach, including a remote one behind Cloudflare Access, with the sign-in the app already has and no new secret.
- A WKWebView cannot put an `Authorization` header on a websocket. Access would then want its own cookie, which means a second sign-in inside the web view, and Google refuses embedded sign-ins.
- lasso's local guard refuses a websocket upgrade whose `Origin` is not lasso's own. A page on another origin cannot open `/terminal/…/ws` directly, but a native task sends no `Origin`.
- A native terminal emulator (SwiftTerm) would be third-party code (§0).
- `ssh <host> herdr` in Terminal.app works only where the Mac has ssh to lasso's machine.

**Checked from titan** (2026-10-12):
- `lasso.orangecountyai.com` answers a credential-less websocket upgrade to `/terminal/local/ws` exactly as it answers `/api/version`: a 401 with Access's Managed OAuth `Bearer` challenge. So the bearer token that gets the app past Access on `/api` gets it past on `/terminal` too.
- On loopback, an upgrade with no `Origin` and `Sec-WebSocket-Protocol: tty` gets `101` from lasso's proxy and ttyd. The same upgrade with `Origin: lasso-terminal://…` gets `403`.
- ttyd starts its herdr client only on the first frame, so the handshake alone started nothing.

**Hosts.** The window shows one host's herdr at a time.
- Showing an agent first focuses its pane: `POST /api/focus?host=<pane host>` with `{pane_id, reveal: true}`. herdr's focus is one per herdr, so every client of that herdr moves with it, as the web app's explicit focus does.
- If the window shows another host (or none), it then attaches the pane's host (`POST /api/host`, as a web tab moving onto a host does), reads its `host_slug` from `/api/active?host=`, and loads that terminal. A fresh herdr client opens on the focused pane.
- `reattach` in the focus answer (the client showed a saved machine) reloads the page, which starts the client again on Local.
- The document's palette is read off lasso's redirect (it is not followed), so the page's own URL carries `theme=`, where ttyd reads it.

**The shared size.** Every client of one herdr shares its size, and herdr reflows every pane on each change. So the window takes part in lasso's term claim as a web tab does (`lib/term-claim.ts`).
- It holds its own event stream on its host, as client `lasso-desktop-term-…`. That stream also keeps a remote host's terminal from being retired as idle. Its `active` frames say who holds the size.
- A key, a pointer or the window taking focus claims the size (`/api/term-claim`), renewed at most every `live.term_claim` (15 s).
- While another client holds the size, the page's resize frames are held and the last one goes when the size is free or ours. A size taken before layout settles (under 20×4) is never sent.

**Right-clicks are herdr's.** WebKit's context menu (Reload, Inspect Element, Copy) never opens in the window. The page script cancels the `contextmenu` event and nothing else, so a right button's press and release reach xterm, and through its mouse reporting herdr, as in a browser tab. A Control-click is a right-click, as everywhere on the Mac: the script swallows its left-button press and release and hands xterm the right button's instead. `LassoPageWebView.willOpenMenu` empties the menu for a click the page did not see (one before it loaded). ⌘C and ⌘V are key equivalents and unaffected.
- **Checked on minime** (macOS 27.0.1, 2026-10-12), with clicks posted through the window server into the app's own window: a right-click reaches ttyd 1.7.7's page as the right button's press and release, xterm reports both (`ESC[<2;col;rowM`, then `…m`), and herdr 0.9.3 opens its pane menu (Rename pane … Close pane). A Control-click does the same, and so does a right-click while the app is in the background. No WebKit menu tracks. `rightClicksInTheHerdrWindowReachHerdr` pins the app's half of this (window, page script, native socket) against a stand-in page and ttyd.
- A pane whose herdr menu has **Send right-clicks to pane** on hands its right-clicks to the program in it whenever that program reads the mouse, so herdr shows no menu there. That is herdr's setting, the same in a browser tab.

**A new terminal** from the New form's Terminal mode (§20) opens the window on its host and pane, as an agent's header button does.

**The page script also** opens terminal links in the browser, puts herdr's OSC 52 copies on the clipboard, and sends Shift/Ctrl+Enter as a Kitty CSI-u key (as the web app's `lib/terminal.ts` does). ⌘W (`AgentWindowPanel`'s, whatever has the keyboard) or the close button closes the window. Closing it closes the socket, and with it that herdr client. Switching to another lasso closes it.

**Text size.** **⌘=** or **⌘+** makes the text bigger and **⌘-** smaller, one step at a time within bounds, and **⌘0** returns to lasso's own size (`terminal.zoom`: 14, steps of 1, 8–32, the web app's terminal text range). The size is xterm's `fontSize`, the option the web app's Settings › Terminal text sets. It is not `WKWebView.pageZoom`: herdr reflows on xterm's columns and rows either way, and a page zoom would scale ttyd's own frame and overlays as well.
- **The keys** are `TerminalWebView.performKeyEquivalent`'s, so they work without a menu bar (the app often runs as an accessory). The page, xterm and the shell never see them. Option or Control makes them the terminal's. The main menu's **View** menu has Zoom In, Zoom Out and Actual Size, sent through the responder chain, so they are live only while this window is key.
- **ttyd's own handling** (checked against ttyd 1.7.7's bundle). ttyd exposes `window.term` with `term.fit()`. On every connect (`SET_PREFERENCES`) it merges lasso's `-t` options with the page's URL query and refits on any `font…` option. So the page loads with `fontSize=` in its query, and ttyd sizes it in one fit. A zoom sets `term.options.fontSize`, calls `term.fit()` now and on the next frame (the web app's nudge), and writes the size into the page's query with `history.replaceState`. The script also sets the size again after each `SET_PREFERENCES` frame, so a reconnect cannot put 14 back.
- **The shared size.** A zoom is a human resizing, so it claims the size first, as a key does. The resulting resize frame goes through the gate above.
- **Remembered** under UserDefaults `terminalFontSize`, one size for the app. lasso's own size is kept as no value, and a host switch or reload loads at the remembered size.

**Parity:** `rail-hover-terminal`, plus `rail.terminal`, `card.herdr` and `agent.herdr` rows in `rail-expanded`, `card-open` and `agent-window`. The window itself has no parity state, since its page comes from a live lasso.

**Not yet verified live** (it needs an install on a Mac, and minime's app belongs to another session):
- The page loading through the scheme handler, including ttyd's `fetch` of `token` over a custom scheme.
- The native socket's upgrade through Access with the app's bearer token.
- Typing, paste, resize and the claim handover with a browser tab open on the same herdr.
- A remote host's terminal (`herdr --remote`).
- The window following a real desktop switch (Mission Control, a swipe) and keeping the keyboard there in the installed app; the tests reach another Space only through another app going full screen.

## 23. Bots

lasso's bots get a place of their own on the rail, following the web app's Bots view. Creating, deleting and configuring a bot (env, secrets, skills, jobs) stays in the web app. Bots still come to the Inbox when they ask or finish.

**The rail's Bots button** sits between the agents and the terminal while lasso has bots (`RailModel.hasBots`). Its icon is Lucide's `bot`, as on the web and the phone. Its dot is the most urgent bot's state: red while one asks or failed, blue while one works, none otherwise (`BotStanding.railDot`). Like the agents capsule, resting on it opens its column and a click pins nothing (§16).

**The Bots column** (`Bots/BotsColumn.swift`) is a rail pop-out the size of the agents column. Each bot has a row with its picture (`/api/bots/<name>/avatar`, else its initial), its name, its state and its last line: the question it asks, its last words, or why it failed.
- **Order:** asking (blocked, or holding a `pending_ui` request), working, failed, idle, stopped, by name within each (`BotStanding`).
- **Resting on a row** previews its latest exchange to the column's left, with the same timing as the agents column (`PreviewHover`).
- **Keys:** J/K and ↑/↓ move, ⏎ opens, and esc closes. No keycaps are drawn.
- **Manage bots ↗** opens the web app's `/bots/manage` at this lasso's address.

**A bot's window and preview are an agent's** (§19, `Conversation`): the same header, conversation, composer and keys, drawn by the same views. Resting on a row previews it with the agents' preview, which takes typing once clicked, as an agent's does (`CardKeys.botPreviewSet`: esc closes it).

**A row's click opens the bot's window** (`Bots/BotWindow.swift`). It is an agent window's panel with an agent window's behaviour: it persists, floats over full-screen apps, follows across Spaces, moves by its header, resizes by its corner and collapses to its header. Its frame is kept per bot (UserDefaults `botWindowFrames`). The chat is the bot chat's own `ChatModel`, on the bot's routes (`chat`, `chat/send`, `chat/answer`, `chat/stop`; a claude bot's pane chat).
- **Header:** the picture, the name, "state · harness bot · N jobs", the terminal button, minimize and close.
  - **The terminal button** (and **T**) shows the bot's herdr pane in herdr's window, as an agent's does (`/api/focus?host=` with `{pane_id, reveal}`). An omp bot's `mise run` and a claude bot's run in a pane alike: the pane is the fleet's row for lasso's `pane_id`, else the pane on the bot's host carrying its name (`BotWindowModel.pane`). A stopped bot has none, so the button is dimmed and says "<name> isn't running: start it to see its terminal".
- **Conversation:** as an agent window renders it. An open request's options are listed with Answer (`chat/answer`).
- **Composer:** paste and drop upload, ⏎ sends, and ⌘. stops the turn.
- **Footer,** in an agent footer's place: the model (an omp bot's is a menu: `POST /api/bots/<name>/model`, which switches a running bot at once and otherwise says the change takes effect after a restart; a long name truncates), Conversations (New conversation, `sessions/new`; a pick resumes with `sessions/resume`), Stop bot or Start bot (`stop`, `start`), and ⋯ with Restart, Jobs ↗, Settings ↗ (`/bots/<name>/settings`) and Manage bots ↗. A preview's footer is the folder and the model, as an agent preview's.
- **Refusals are said.** lasso refuses a conversation switch mid-turn with a 409, and its reason shows in the window ("Not now: …"). Every other failure is named too.
- **A stopped bot** shows "<name> isn't running." with **Start <name>** above its last conversation, and the composer is greyed until it starts.

**Parity states:** `bots-column`, `bot-window` (an omp bot holding an approval, with a long model name: close stays 16 pt from the edge, the terminal button 80), `bot-window-stopped` (the terminal button drawn, dimmed) and `bot-preview` (clicked, holding a reply). `rail-expanded`'s fixture has an asking, a working, an idle and a stopped bot, so the rail draws the Bots button with a red dot.

## 24. lasso's browsers in a window

The web Browser tab's **Agent** view, in a window of its own: the rail's Browser button (`globe`, between the terminal and the **+**, `rail.browser`) opens it on a click. It shows one of the connected lasso's browsers at a time (lasso's own Chromium, or a remote one lasso dials, `docs/design/shared-browser.md`). There is no Iframe view and no mode switch.

**The window** (`Browser/BrowserWindow.swift`) persists until it is closed (⌘W, or its close button), remembers its frame (UserDefaults `LassoBrowserFrame`) and starts at `browser.window`. It is the herdr window's kind of window (§22, `Page/PageWindow.swift`): an agent window's `AgentWindowPanel`, over every app and on every Space, following the user across desktops, with the keyboard in its page without activating the app, collapsing to its bar, resized by its corner, and never full screen. Opening it puts the rail's pop-outs away first.
- **Its bar** is its header: the title and the **browser picker**, a menu of the browsers `GET /api/browser` lists for this lasso, each with its open pages counted, then minimize and close. Dragging it moves the window. The pick is remembered (UserDefaults `browserProfile`); one lasso no longer has gives way to its default.
- **Below it is lasso's page** `/desktop-browser.html` (`src/web/src/desktop/browser-window.tsx`): the web tab's `LiveBrowser` and nothing else of the web app. Its toolbar (back, forward, reload, the port-or-URL field, **↗** open in the Mac's browser), its page tabs (✕ closes one, **+** opens one) and its live view are the web tab's own: a CDP screencast with clicks, scrolling and typing forwarded to the real browser. A page an agent asked to show is selected.
- **A click acts on the page at once**, the one that gives the window the keyboard included (`LassoPageWebView.acceptsFirstMouse`): WebKit would spend it on making the window key, and the strip's **+** clicked with another app in front did nothing. `theStripsPlusOpensANewPage` drives lasso's real built page (when `src/web/dist` is beside the package on the Mac) against a stand-in Chromium: a click on **+** sends `Target.createTarget`, discovery's `targetCreated` selects the new page, and a refusal shows in the page's own status line.
- **It streams only while it is in view.** Closing it, or collapsing it to its bar, hides the page, which stops the screencast and closes its socket, as the web tab does, so lasso's idle stop still fires.

**How the page runs.** It is a lasso page (§22's machinery, `Page/LassoPage.swift`) under the `lasso-browser:` scheme (`Browser/BrowserPage.swift`). Its requests go through the app's `LassoClient`, with its credentials, and its websockets are native tasks.
- **What it may reach:** its document, the build's `/assets/` files (one level, no escapes), and `/api/browser` (read, and POSTed to start a stopped browser). Its sockets go only to `/cdp` and `/cdp/p/<id>` (ids as lasso makes them). `/api/browser/profiles`, `/api/file` and every other route are refused.
- **`cdpOriginAllowed`** passes the native socket, which carries no Origin (lasso's rule for a client that is not a web page). `/cdp`'s gate then takes the app's credentials as it takes an agent's: UI_AUTH basic, or under `MCP_OAUTH` the app's lasso token with `local` reach.
- **The bar and the page talk** through `window.__lassoBrowser` (`select(id)`, `show({profile, tab_id, url, from})`). The page says `ready` once mounted, and the window hands over what came before it.

**When an agent opens a page for the human** (`open_browser_tab`, `show_browser_tab`: lasso's `browser-open` event on the app's stream, `FleetSync.onBrowserOpen`):
- **With the Browser window in view**, the page is selected there, as the web tab does. The window is not raised and the keyboard stays where it is.
- **Otherwise nothing opens.** The rail's Browser button **pulses** (`rail.browser.pulse`: an accent ring on its rim, breathing) and a **banner** comes up beside the rail (`Browser/BrowserBanner.swift`): "<agent> opened a page for you", the page's host and path, **Dismiss** and **Open**. The banner's panel never becomes key, and it is a rail pop-out: shown through `RailPanel.orderIn` (§16, "Spaces"), on the Space the user is on, another app's full screen included, and replaced (as a `BannerPanel`) when macOS pinned it elsewhere. It goes by itself after `browser.banner`'s linger.
- **Open**, or a click on the pulsing button, opens the window on that browser and that page. **Dismiss** stops the pulse and leaves the page open in the browser. A pulse whose banner went unanswered stops once the rail has been opened and closed again (`BrowserAttention`). A newer page replaces an older one.

**Parity:** `browser-window` (the bar over the opening status: the page itself comes from a live lasso), `browser-banner`, `rail-browser-pulse` (drawn still at its brightest), and `rail.browser` rows in `rail-expanded`.

**Not yet verified live** (it needs an install on a Mac, and minime's app belongs to another session):
- The screencast's frame rate through the native socket. (The real `/desktop-browser.html` and its module scripts load over the custom scheme in `theStripsPlusOpensANewPage`.)
- `/cdp` through Cloudflare Access with the app's bearer token, and under `MCP_OAUTH`.
- Typing, paste and the page tabs against a browser an agent is using at the same time.
- The **+** against minime's Chrome (a remote browser) in the installed app.
## 25. Anonymous usage analytics

The app sends anonymous usage analytics straight to PostHog's capture API (`POST https://posthog.execution.associates/batch/`), not through lasso: it may talk to a lasso it does not control, and lasso's own setting covers lasso and its web app only. `Platform/Analytics.swift` holds the host, the project's public ingestion key (`AnalyticsConfig.key`, a write-only `phc_…` key; empty sends nothing) and the whole vocabulary as closed enums, so no caller can pass a free string. There is no third-party SDK.

**What it sends.** Every event carries `app` = `mac`, `app_version`, `os` = `darwin`, `arch`, `$lib` = `lasso`, and PostHog's `$geoip_disable` = true and `$process_person_profile` = false. Its `distinct_id` is a random UUID made on the first send (UserDefaults `analyticsInstallId`). Never a name, address, path, hostname, prompt, message, bot or agent name, token, or anything typed.
- `install_active`, once per UTC day while the app runs on a live lasso (UserDefaults `analyticsActiveDay`): `hosts_count` (1, 2-3, 4+), `agents_running` and `bots_count` (0, 1-5, 6-20, 21+), from the fleet the app already holds.
- `view_opened` with `view`: `inbox_card` (an inbox card, or the empty inbox), `agents_column`, `bots_column`, `chat` (a bot's chat panel), `agent_window`, `bot_window`, `terminal_window`, `browser_window`, `new_form`, `settings`. At most once per view per ten minutes.
- `agent_created` (`harness`: claude, codex, opencode, omp, pi, other; `kind`: git, scratch; `from`: mac) when the New form creates an agent.
- `inbox_action` (`action`: acknowledge, kill, reply, answer, stop, rename; `from`: mac) when lasso took one from a card or an agent window (`Card/LiveCardActions.swift`).
- `feature_used` (`feature`): `shortcut_used` (a global shortcut, the summon key, ⌘O/⇧⌘O/⌘I), `paste_upload` (a file pasted or dropped onto a composer or the New form), `terminal_zoom` (the herdr window's text size).
- `onboarding_completed` when the first-run panel is finished, only if analytics is on after the choice. The choice itself is never sent.

**Off means nothing is sent**, not even an "opted out" event, and what waits is dropped:
- the first-run panel's **Share anonymous usage analytics** switch, under the tips (on unless switched off);
- **Settings › Info › Privacy › Anonymous usage analytics** (UserDefaults `analyticsEnabled`, absent = on), beside a link to the privacy policy (https://www.executionassociates.com/privacy/); its footnote says when the environment overrides it;
- `DO_NOT_TRACK` = 1/true/yes or `LASSO_TELEMETRY` = 0/false/off/no in the app's environment, or an empty key (`LASSO_TELEMETRY_KEY=` overrides the build's; `LASSO_TELEMETRY_HOST` the host).
A fixture or parity run and the test process never send (`Analytics.inert`), and the coordinator counts only with a live lasso.

**Never in the way.** Events queue in memory, capped at 200 (the oldest go), and go out in batches of up to 100, 30 s after the first or at once when 20 wait. One request at a time on a detached utility task, an ephemeral `URLSession` (no cookies, no cache) with 5 s request and resource timeouts. A failure or an error status drops the batch: no retry, nothing shown, a debug log line at most. Quitting does not wait for it.

**Tests:** `AnalyticsTests` (a fake transport): every off path sends nothing; every event the enums can make carries only the listed keys and values; an error status, an unreachable host and a hanging one leave one request in flight and a bounded queue; the view throttle and the daily ping; the first-run and Settings switches.
