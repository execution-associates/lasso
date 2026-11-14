<!-- Design rationale and contract: read before changing the code it names. -->

# Client text to speech (`src/clienttts.go`, `src/clienttts_stream.go`, `src/clienttts_inworld.go`, `ClientTTSSettings.tsx`, `scripts/tts-live-check.py`)

The phone apps can speak a bot's reply in a cloud voice: Inworld's TTS-2. The app sends the reply sentence by sentence to lasso, lasso synthesizes each one with Inworld and streams raw PCM back. **The Inworld key lives only in lasso**: the app never holds it, never sees it, and cannot make lasso call anything but Inworld's fixed host. Everything else about speech (the offline voice, playback, which voice to use when) stays in the app. Tests: `clienttts_test.go`, against a stand-in that speaks Inworld's documented protocol.

Contents: [The client contract](#the-client-contract) · [Limits](#limits) · [Cost and the monthly cap](#cost-and-the-monthly-cap) · [Settings and credentials](#settings-and-credentials) · [How the relay works](#how-the-relay-works) · [Inworld](#inworld) · [Turning it on](#turning-it-on) · [Privacy](#privacy) · [Live check](#live-check)

## The client contract

Both routes need a device token like every client route (`Authorization: Bearer lcd_…`). The token travels in that header only: lasso has no `Sec-WebSocket-Protocol` convention and a token in a URL is never accepted. A revoked or unknown token reads `401` before anything about the route is revealed.

`GET /tts` is cheap: the key and the settings are held in memory and the usage is two indexed reads; the voice catalog is cached for ten minutes and a stale one is answered at once and refreshed in the background (only the very first call, with nothing cached, waits for Inworld, at most 3 s). `usage.characters_this_month` is current to the last sentence **sent upstream** (counted when sent, so it already includes a sentence in flight).

`GET /me` lists `"tts"` in `features` (alongside `threads`, `search`, `push`, `proposals`).

### `GET /client/v1/tts`

```json
{"engines": [{
  "id": "inworld", "name": "Inworld",
  "available": true, "reason": null,
  "models": [{"id": "tts-2-realtime", "name": "TTS-2 Realtime", "default": true}, {"id": "tts-2-flash", "name": "TTS-2 Flash"}],
  "voices": [{"id": "Ashley", "name": "Ashley", "gender": "female", "language": "en", "description": "A warm, natural female voice"}],
  "default_voice": "Ashley", "default_model": "tts-2-realtime",
  "sample_rate": 24000, "format": "pcm_s16le",
  "usage": {"characters_this_month": 1234, "limit_characters": 500000}
}]}
```

- `available` is false with `reason`: `not_configured` (no key reference, or the engine is switched off), `unreadable` (the key reference cannot be read), `failing` (Inworld refused the key or could not be reached repeatedly; clears 30 s after the last failure or at the next working call) or `quota_exceeded` (this person's, or everyone's, allowance for the month is used). `reason` is `null` when available.
- Unavailable engines still list `models`; `voices` is `[]` and `default_voice` is `null` while the key cannot be read. `usage.limit_characters` is the person's monthly limit, always present.
- `voices` is Inworld's own catalog for lasso's workspace (system voices and the workspace's own), sorted by name, at most 400. `gender` is `female`, `male` or `null`; `language` is the primary language subtag (`en`). The list is cached for ten minutes; when Inworld cannot be asked the last list stands, else only the configured default voice.
- `default_model` is not in the original contract; it names the owner's default. Apps may ignore it.

### `GET /client/v1/tts/stream` (WebSocket)

One WebSocket per spoken reply. What can be known before the upgrade is answered as **plain HTTP with the client API's error JSON** (`{"error": {"code", "message"}}`), so an app can tell the cases apart:

| status | code | when |
| --- | --- | --- |
| 401 | `unauthorized` | no, bad or revoked token (checked first, before anything about the route is revealed) |
| 404 | `not_found` | the route does not exist (an older lasso) |
| 404 | `not_configured` | the relay is not set up, is switched off (`enabled: false`) or its key cannot be read |
| 426 | `upgrade_required` | a request without `Upgrade: websocket` |
| 429 | `rate_limited`, with `Retry-After` | too many streams open for this device, person or lasso, or too many opened per minute (429 rather than 409: nothing conflicts, the app is only early) |

After the upgrade everything is in-band. Quota and a failing upstream change while the app is connected, so they are frames (`quota_exceeded` and `upstream_unavailable`, fatal) followed by a close.

Client to server, text frames of JSON, at most 16 KiB each:

| frame | meaning |
| --- | --- |
| `{"type":"start","engine":"inworld","model":"tts-2-realtime","voice":"<id>","sample_rate":24000}` | once, first. `model`, `voice` and `sample_rate` are optional (the owner's defaults, 24000). `sample_rate` is 16000, 24000 or 48000. The voice must be one `GET /tts` lists |
| `{"type":"text","id":"s1","text":"One sentence."}` | one sentence. `id` is 1 to 64 of `A-Za-z0-9._:-`, chosen by the app, used once per connection. Text sent after `start` but **before `ready`** is accepted and queued (up to the queue limit) and spoken once the upstream is connected; text before `start` is a fatal `invalid_request` |
| `{"type":"cancel"}` | drop everything queued and in flight |
| `{"type":"cancel","id":"s1"}` | drop one sentence (an id that is done or unknown is ignored) |
| `{"type":"end"}` | finish what is queued, then close |

Server to client:

| frame | meaning |
| --- | --- |
| `{"type":"ready","sample_rate":24000,"format":"pcm_s16le","channels":1,"model":"tts-2-realtime","voice":"Ashley"}` | the upstream connection is open; send text now |
| `{"type":"audio","id":"s1","seq":0,"bytes":N}` followed at once by exactly one **binary** frame of N bytes | PCM signed 16-bit little-endian, mono, at the ready rate. `seq` counts from 0 per id. N is even and at most 32768 |
| `{"type":"done","id":"s1","characters":13,"audio_ms":820}` | the sentence is complete. `characters` is exactly what was counted against the month for it (Inworld's count of the cleaned text, an emoji being two, so it can differ from the app's own length); `audio_ms` is the audio actually sent, `bytes / 2 * 1000 / sample_rate` |
| `{"type":"progress","id":"s1"}` | keep-alive: sent whenever nothing else has been sent for 0.8 s while a sentence is outstanding (the oldest one is named), so an app that treats a quiet socket as dead is kept waiting only while lasso is. An app that does not know the frame ignores it. Nothing is sent while nothing is outstanding |
| `{"type":"error","id":"s1"\|null,"code":"…","message":"…","fatal":true\|false}` | `id` is null when it is not about one sentence |

Order: sentences are synthesized and answered in the order sent; frames of different ids never interleave; an `audio` header and its binary frame are never separated. After a `cancel` (one id or all) is handled, **no further `audio`, `done`, `error` or `progress` frame is sent for a cancelled id**, and a frame already being written finishes first (the app may still receive that one). A cancelled sentence gets no `done` and no `error`. A sentence cancelled before lasso sent it upstream is never sent and never counted; one already sent stays counted (see [Cost](#cost-and-the-monthly-cap)).

Error codes:

| code | when | fatal |
| --- | --- | --- |
| `not_configured` | nothing set up, switched off or the key unreadable | yes |
| `quota_exceeded` | the person's or the total monthly allowance is used (at open, or when a sentence would pass it; Inworld's own quota message too). **Fatal: the stream is closed** (1000, reason `quota_exceeded`) right after the frame, with `id` the sentence that would have passed the limit, or null at open; the app should drop the cloud voice for the rest of the call | yes |
| `upstream_unavailable` | Inworld refused the key, could not be reached, or dropped the connection (for one sentence; the next reconnects) | at `start` yes, per sentence no |
| `upstream_timeout` | no first audio within 1.5 s of the text going upstream (inside the app's 1.8 s wait, so the app gets this frame rather than silence), no audio for 10 s mid-sentence, or 60 s for one sentence | no |
| `rate_limited` | per-person sentence or character rate, too many sentences waiting, too many streams, or Inworld's concurrency limit | no, except too many streams |
| `invalid_request` | a bad `start` (fatal) or a bad frame, id, empty or non-UTF-8 text, a repeated id, text after `end` (not fatal; ten bad frames end the stream) | at `start` yes |
| `text_too_long` | more than 1000 characters in one sentence | no |

`message` is lasso's own wording; Inworld's text is never passed on.

Closing: the server closes with 1000 after `end` and its last `done`, after a fatal error (the reason is the code), and when idle; 1001 (`lifetime`, `ping failed`, `write failed`) when the stream ran out of time or the phone stopped taking data; 1003 for a binary frame from the app; 1008 `access_revoked` within 2 s of the device being revoked, the person disabled, or the token's device removed. The server pings every 20 s.

## Limits

Constants in `clienttts.go` (`ttsTuning` holds those tests shorten):

| limit | value |
| --- | --- |
| text in one sentence | 1000 characters (Inworld accepts 2000 UTF-16 units) |
| sentences waiting or in flight on one stream | 20 |
| distinct ids on one stream | 5000 |
| streams per device / per person / in total | 2 / 4 / 8 |
| stream opens per device | 12 per minute |
| sentences per person | 90 per minute |
| characters per person | 12000 per minute |
| connection lifetime | 30 minutes |
| idle (no frame from the app, nothing in flight) | 2 minutes |
| wait for `start` | 10 seconds |
| a write the phone does not take | 20 seconds |
| first audio / between chunks / one sentence | 1.5 s / 10 s / 60 s |
| connecting upstream at `start` | 1.2 s per try, two tries (inside the app's 3 s wait for `ready`) |
| progress keep-alive | after 0.8 s of silence while a sentence is outstanding |
| audio held per sentence | 1 MiB |
| per-person monthly characters | 500,000 (configurable) |
| total monthly characters | 1,600,000 (configurable) |

The totals of 8 streams and Inworld's concurrency: each stream uses up to two Inworld generations (the sentence playing and the one after it), and the on-demand plan guarantees 5 ([concurrency limits](https://docs.inworld.ai/resources/concurrency-limits)); beyond that Inworld serves best effort and may answer `code: 8`, which reaches the app as `rate_limited`.

## Cost and the monthly cap

Inworld bills per character of input text ([pricing](https://inworld.ai/pricing), [billing](https://docs.inworld.ai/tts/resources/billing)). On-demand, no plan: TTS-2 $25 per million characters, TTS-2 Flash $15. The owner's card allows $50 a month.

- At the dearest rate, $50 / ($25 per 1,000,000) = **2,000,000 characters** is the most the card can pay for.
- **Total cap: 1,600,000 characters = $40.00** at TTS-2 (80%; $24.00 at Flash). The 20% margin covers differences between how lasso and Inworld count and a price change. It binds everyone together, however many people use the relay.
- **Per-person cap: 500,000 characters = $12.50** at TTS-2. A person's limit never exceeds the total.
- About 15 characters make a second of speech, so 1,600,000 characters is roughly 30 hours.
- Both are settings (`monthly_character_limit`, `total_monthly_character_limit`); the owner may raise them knowingly.

Counting. Characters are counted **as Inworld counts them** (a character outside the Basic Multilingual Plane, an emoji for one, counts two; spaces, punctuation and tags count) **on the text lasso actually sends** (after cleaning), **when it is sent upstream**, per UTC month, per person, in `app_tts_usage`. A sentence refused for quota, rate or length is never sent and never counted; a sentence whose upstream write failed is refunded. A sentence cancelled after it was sent stays counted: Inworld bills text it had begun to synthesize ("disconnecting does not avoid charges"), and only text it had not yet processed is free ([billing](https://docs.inworld.ai/tts/resources/billing)), so lasso counts at least what it will be billed. The owner's test phrase counts under `_owner_test`. A deleted person's usage moves to `_deleted`, so the total still counts it.

When a month's allowance would be passed, the sentence is refused (`quota_exceeded`, fatal) before it is sent. The count resets at 00:00 UTC on the first of the month.

## Settings and credentials

One `settings` row, `client_tts`, holding choices and a **reference** to the key, the same pattern as `client_push` and `llm`:

```json
{"inworld": {"key_ref": "op://Lasso/Inworld/api key", "default_model": "tts-2-realtime", "default_voice": "Ashley",
             "monthly_character_limit": 500000, "total_monthly_character_limit": 1600000, "enabled": true}}
```

- `key_ref` is an environment variable name in lasso's environment or an `op://vault/item/field` reference, read with `lookupSecret`, held in memory until the reference changes (a failure is remembered a minute), never logged, never returned. The value must be the Base64 credential Inworld's portal shows, unchanged: it is sent as `Authorization: Basic <value>`, never re-encoded and without a second `Basic`. A value with spaces, control characters or more than 512 bytes reads as unreadable.
- `default_model` is `tts-2-realtime` (default) or `tts-2-flash`. Zero or absent limits mean the defaults. `enabled` defaults to true.
- **Nothing in a setting or a request names a host.** The upstream URLs are constants in `clienttts_inworld.go` (`wss://api.inworld.ai/tts/v1/voice:streamBidirectional`, `https://api.inworld.ai/voices/v1/voices`); redirects are not followed. The relay is not an open proxy: an app chooses an engine, a model and a voice from what `GET /tts` offers, and speaks text.

The owner manages it at `/api/client-tts`, behind `UI_AUTH` and off machine callers like `/api/client-push`:

- `GET` answers the choices (the reference, never the key), the state (`unconfigured`, `unreadable`, `ready`, `failing`), the effective limits and this month's usage in total and per person (`usage.people`).
- `PUT` merge-patches `{"inworld": {…}}`; `""` or `null` clears a field; unknown fields (a URL, say) are `400`.
- `POST /api/client-tts/test` (optional `{"model","voice"}`) opens a real upstream connection, speaks the fixed phrase "Text to speech works." and answers `{"ok", "connect_ms", "first_audio_ms", "total_ms", "audio_ms", "audio_bytes", "characters", "model", "voice"}` or `{"ok": false, "code", "message"}`. It counts toward the month.
- `get_settings` and `update_settings` gain the section `client_tts` (the same fields). `update_settings` changes it only for a real MCP credential with fleet reach; an anonymous `/mcp` caller, which is what a local bot is, is refused before anything changes, and `get_settings` never shows it a person's usage.
- Settings → Phone app speech (`ClientTTSSettings.tsx`) edits the fields, shows the state and usage and has a Test button.

With nothing configured nothing runs: no goroutine, no connection, no secret read; the stream answers `not_configured`.

## How the relay works

**One upstream WebSocket per app stream.** Inworld multiplexes contexts on a connection, but a per-stream connection keeps one person's sentences, cancellation and failures apart, and a dial at `start` surfaces a bad key before the app sends any text. `ready` is sent only after the upstream is connected.

**One Inworld context per sentence**: `create` (voice, model, `audioConfig` PCM at the stream's rate, `autoMode` off), then `send_text` with `flush_context`, then audio until `flushCompleted`, then `close_context`. `cancel_context` drops a sentence upstream. Audio that still arrives for a cancelled context is discarded.

**Three workers per stream.** The reader takes the app's frames, validates and queues sentences. The feeder sends sentences upstream one at a time, in order, and sends the next only when the previous one has been fully received. The player writes each sentence's audio to the app in the same order. The hand-off holds one sentence, so the next is being synthesized while the current one plays and never more: audio held is at most two sentences, each bounded to 1 MiB. Past that the reader stops reading from Inworld, and TCP slows Inworld down; a phone that is slow slows the relay instead of growing its memory, and one that takes nothing for 20 s loses the stream.

**Failure handling.** A failed dial is tried twice (1.2 s each, 200 ms apart) and then fails the stream at `start`, or the sentence mid-stream. A dropped connection fails the sentence in flight and the next sentence reconnects. A refused key or repeated failure marks the engine `failing` for 30 s: new streams are turned away without calling Inworld, and the next working call clears it. Timeouts cancel the context upstream.

**Text.** Valid UTF-8 only; control characters, invisible formatting (zero-width, bidirectional controls, byte order marks) and private-use characters are removed; line breaks, tabs and runs of spaces become one space; empty text is refused. The text is the person's bot replies and leaves lasso for Inworld.

**Authorization.** The device and the person are re-checked every 2 s. A revoked device, a disabled person or a deleted device closes the stream (1008).

**Logs** carry ids, sizes, counts, timings and Inworld's status codes only: `tts: sentence sent device=d_… id=s1 chars=13`, `tts: stream closed device=d_… sentences=2 chars=40 audio_ms=…`. Never text, audio, the key, or anything Inworld said.

**Audio details.** Chunks are forwarded as they arrive, split at 32 KiB, carried to even lengths, and a WAV header that should not be there is dropped.

## Inworld

What the relay's upstream code is built on, read from Inworld's documentation on 2026-11-14. No key existed: **nothing below has been run against the live service**. The places that may differ are marked `UNVERIFIED` in `clienttts_inworld.go`, the only file that knows the protocol.

**Verified in the documentation**

- Models: `inworld-tts-2` ("Realtime TTS-2", first audio P90 100 ms server-side) and `inworld-tts-2-flash` (P90 20 ms, lowest cost); 200+ languages; `inworld-tts-1`, `-1-max` were discontinued 2026-06-15 and `-1.5-*` are deprecated. [TTS models](https://docs.inworld.ai/docs/tts/tts-models). lasso's ids `tts-2-realtime` and `tts-2-flash` map to them in `ttsModels`.
- Streaming API: `wss://api.inworld.ai/tts/v1/voice:streamBidirectional`; client messages `create` (`voiceId`, `modelId`, `audioConfig`, `autoMode`, …), `send_text` (`text` at most 2000 UTF-16 code units, optional `flush_context`), `flush_context`, `close_context`, `cancel_context`, each with a `contextId`; server messages wrapped in `result` with `contextId`: `contextCreated`, `audioChunk.audioContent` (Base64, with `usage.processedCharactersCount`), `flushCompleted`, `contextClosed` (last for a context), errors as `status.{code,message,details}` with gRPC codes. Several contexts share one connection; the session closes after 10 minutes without activity; `cancel_context` discards unsynthesized text but audio already generated may still arrive. [WebSocket reference](https://docs.inworld.ai/api-reference/ttsAPI/texttospeech/synthesize-speech-websocket).
- Audio: `audioEncoding` `PCM` has no header (`LINEAR16` has a WAV header in every chunk, `WAV` in the first); `sampleRateHertz` 8000 to 48000, default 48000 capped at the model's native rate, a higher rate fails. Same page.
- Auth: `Authorization: Basic <key>` where the key is the Base64 credential copied from the portal, used as is and not re-encoded; the same header is accepted on the WebSocket handshake; a Standard key is enough for synthesis; browsers and apps should not hold it (hence this relay). [Getting started](https://docs.inworld.ai/docs/tts-api/getting-started), [API key auth](https://docs.inworld.ai/portal/api-key-auth).
- Voices: `GET https://api.inworld.ai/voices/v1/voices` (Voice API; `pageSize` up to 2000, `pageToken`, `filter` such as `source = "SYSTEM" AND lang_code = "en"`), answering `voices[]` of `voiceId`, `displayName`, `description`, `gender` (`male`, `female`, `neutral`, empty), `languageCode` (`en-US`), `langCode` (`EN_US`), `source`, `tags`, plus `nextPageToken`. The older `/tts/v1/voices` is deprecated. [List voices](https://docs.inworld.ai/api-reference/voiceAPI/voiceservice/list-voices). The Voice API has a free voice preview.
- Pricing, on-demand: TTS-2 $25, TTS-2 Flash $15 per million characters; plans cut it (Creator $20/$10, Builder $17.50/$9, Developer $15/$8, Growth $12.50/$7); on-demand includes up to 70 minutes of TTS free. [Pricing](https://inworld.ai/pricing).
- Billing: per character of text sent, as written (spaces, punctuation and tags count; characters outside the BMP count two); counted when synthesis starts (flush or close); `cancelContext` text not yet processed is not billed; a disconnect does not avoid charges; audio never played is billed; `synthesisContext` is free. [Billing](https://docs.inworld.ai/tts/resources/billing).
- Concurrency: on-demand 5 concurrent generations (Creator 10, Builder 50, Developer 150, Growth 500); a WebSocket context counts while packets flow; WebSocket connections are limited to 10 times the generation limit (over it: HTTP 429); over the limit a `code: 8` error (RESOURCE_EXHAUSTED) arrives on the connection and other contexts keep working; limits are per account across all keys. [Concurrency](https://docs.inworld.ai/resources/concurrency-limits), [best practices](https://docs.inworld.ai/tts/best-practices/overview).
- Data: Zero Data Retention for TTS redacts input text from logs, is a workspace setting managed by Inworld, and (per the search summary of [the page](https://docs.inworld.ai/tts/resources/zero-data-retention), which required a login to fetch) covers the TTS-1.5 models only; generated audio artifacts sit behind signed URLs and are deleted after 7 days. Inworld describes itself as SOC 2 Type II, HIPAA and GDPR compliant. No statement was found on whether API text is used for training.

**Unverified until a key exists** (the live check covers each)

1. That `autoMode: false` with `send_text` carrying `flush_context` and no further messages behaves as described, and that `flushCompleted` arrives after the last audio of the sentence.
2. That `PCM` at 24000 Hz is signed 16-bit little-endian mono, and that 16000 and 48000 are accepted for TTS-2 (the native rate is not documented; lasso's `ready.sample_rate` is the requested one).
3. The exact JSON field names and casing of server messages in practice (the code reads `result.contextId`, `result.audioChunk.audioContent`, `result.flushCompleted`, `result.contextClosed`, `result.status.code`, and a top-level `error.{code,message}`).
4. How a quota or billing refusal appears (assumed `code: 8` with a message naming credits, quota or balance, mapped to `quota_exceeded`; any other `code: 8` is `rate_limited`).
5. That a connection survives many sequential contexts, and how many contexts one connection may hold (a third-party transcription of the API says five; lasso uses at most two).
6. The voice list's shape for lasso's workspace, and that a voice id from it is accepted by `create`.
7. Whether the platform's rate limit page (`resources/rate-limits`, "100 requests per second" in an older snippet) adds anything that matters at this scale. It was not fetched.

## Turning it on

1. In Inworld's portal, create a workspace and a **Standard** API key; billing on the on-demand plan with the owner's card (limit $50 a month). Copy the key as the portal shows it (the Base64 credential), without a leading `Basic `.
2. Store it where lasso can read it, one of: a 1Password item whose field holds the key, referenced as `op://<vault>/<item>/<field>` (lasso's environment must authorize `op read`, as for the other references); or an environment variable in lasso's unit (`lasso.service`), referenced by its name, for example `INWORLD_API_KEY`.
3. Tell lasso, either in Settings → Phone app speech, or `PUT /api/client-tts`, or `update_settings` `client_tts`: `{"inworld": {"key_ref": "<name or op:// reference>", "default_voice": "<a voice id from Inworld's catalog>", "default_model": "tts-2-realtime"}}`. The limits default to 500,000 per person and 1,600,000 in total; set `monthly_character_limit` and `total_monthly_character_limit` only to change them. No restart: the reference is read on the next request.
4. Press Test (or `POST /api/client-tts/test`): it reports connect and first-audio times, or the code that failed.
5. Run the [live check](#live-check) once, then pair the phone.
6. To switch it off without losing the settings: `{"inworld": {"enabled": false}}`. To rotate the key, change the item (or the variable) and `PUT` the same reference again, which drops the held value.

## Privacy

- **What a person's bot said leaves the machine.** Each sentence the app asks to speak is sent to Inworld (`api.inworld.ai`) as text, tied to lasso's workspace and key, not to the person's name or device. lasso keeps no text or audio: only counts per person and month.
- Inworld's own retention applies to that text. Zero Data Retention is not enabled by default and, as documented, covers older TTS models only; assume Inworld may log input text. The app should make speaking a choice the person makes, and say that the text goes to a cloud service.
- A bot's reply that was never spoken is never sent. The relay forwards only what the app sends, and the app sends only what the person has chosen to hear.
- The key never reaches an app, a log, an API answer, or an MCP answer; the owner's views show the reference only.
- A person who is disabled, whose device is revoked, or who is deleted loses the stream within 2 s; deleting a person removes their rows except the usage counts, which move to `_deleted`.

## Live check

`scripts/tts-live-check.py` (run with `uv`, needs only a configured key) makes a throwaway person and paired device through the owner's `/api`, checks `/me` and `GET /tts`, speaks one sentence over the stream, verifies the audio (whole 16-bit samples, a plausible length and level, not byte-swapped, `done` matching the bytes and the text), writes it as a WAV and deletes the person. It prints no secret and no token, and spends about 55 characters.

```sh
scripts/tts-live-check.py --url http://127.0.0.1:8090 --out tts-live-check.wav   # UI_AUTH=user:password if set
```
