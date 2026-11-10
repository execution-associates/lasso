<!-- Design rationale and contract: read before changing the code it names. -->

# Client API (`src/clientapi.go`, `src/clientapi_store.go`, `src/clientapi_bot.go`, `src/clientapi_admin.go`, `src/mcp_appusers.go`)

The client API is how a phone app (iOS today, Android next) lets a person talk to the bots they were given, by text and by voice call. It is `/client/v1` on lasso's origin, beside `/talk` (the OpenAI-compatible door) and nothing like the owner's `/api`. Tests: `clientapi_test.go`.

What it adds over Talk, and why it is a surface of its own:

1. **Every person has their own credential**, and it reaches their bots and only their bots. Talk has one shared key per bot.
2. **The bot knows who is speaking, proven by lasso.** Identity comes from the device token and lasso's own records, never from the client, the caller or the message text.
3. **A conversation is stable**: one per person and bot, with a history lasso keeps, and a call has a stable id.
4. **The bot can hang up a voice call.**
5. **Attachments go both ways**: pictures and files from the person; images and files from the bot that the app shows inline.
6. **Both runtimes**: claude through its `lasso-channel`, omp through the bridge's prompt (below, with the gaps).

OpenAI compatibility is not a goal. The protocol is shaped for these clients.

## Contents

- [Model](#model)
- [Pairing](#pairing)
- [Authentication and errors](#authentication-and-errors)
- [Routes](#routes)
- [The event stream](#the-event-stream)
- [Voice calls](#voice-calls)
- [Attachments](#attachments)
- [Limits](#limits)
- [Delivery to the bot](#delivery-to-the-bot)
- [Managing people](#managing-people)
- [Security model](#security-model)
- [Deployment](#deployment)
- [Gaps and seams](#gaps-and-seams)

## Model

- An **app user** is a person: an id (`u_…`), a display name, an optional email, a disabled flag.
- A **grant** lets a user reach one bot. `owner` marks the person as that bot's owner, its human. A bot may have several owners, and a user may own some bots and not others. Without a grant a bot does not exist for the user.
- A **device** is one paired phone: an id (`d_…`), a label, a platform, who paired it, when it was last seen, and a token hash. Tokens are `lcd_` plus 32 random bytes (base64url), shown once at pairing and stored only as their SHA-256, looked up by hash. A device is revoked on its own.
- A **pairing** is a one-time code (`lpc_` plus 16 random bytes), valid ten minutes, stored as its hash. It records who minted it.
- A **conversation** (`c_…`) is one user's with one bot. It is lasso-owned: only this person's messages and the bot's replies to them. It is **not** the bot's terminal transcript, which can hold other people's business and is never read for the history. The id is unguessable and every call that names one checks it against the token's user.
- A **message** (`m_…`) is from the user or from the bot. A **call** (`call_…`) is one voice call in a conversation. An **attachment** (`a_…`) is a file either side sent.

Storage is `lasso.db`: `app_users`, `app_grants`, `app_devices`, `app_pairings`, `app_convs`, `app_messages`, `app_events`, `app_attachments`, `app_calls`, plus `bot_events.meta` and `bots.channel_client`. Two lassos sharing a db both serve the API; a stream wakes at once on its own lasso's events and looks at the db every second for the other's.

## Pairing

The owner (through `/api/app-users` or the matching `/mcp` tools, [below](#managing-people)) creates a user, grants bots, and mints a pairing. Minting answers, once:

```json
{
  "pairing": {"id": "p_…", "user_id": "u_…", "minted_by": "you@example.com", "created_at": "…", "expires_at": "…"},
  "code": "lpc_…",
  "link": "https://lasso.example.com/client/pair#code=lpc_…",
  "deep_link": "lasso://pair?code=lpc_…&server=https%3A%2F%2Flasso.example.com"
}
```

`minted_by` is the verified caller's name when the owner came through a network listener (the Access email, the tailnet login), `"local"` on the loopback listener, and `mcp:<client id>` for an `/mcp` credential that has one. It is never anything the request says about itself, and it is copied to the device as `paired_by`, which is what the bot is told.

**The link.** `link` is an HTTPS page, not the app link, so it works from a message or an email and falls back gracefully when the app is not installed. `GET /client/pair` is static: it reads the code from the URL **fragment** (which a browser never sends to a server, so no access log, proxy or Cloudflare log ever holds it), builds `lasso://pair?code=…&server=<its own origin>` and opens it, and shows the code to copy if the app does not open. It reflects nothing from the request, has a per-request CSP nonce, `Referrer-Policy: no-referrer`, `Cache-Control: no-store` and cannot be framed, and it rewrites its own address to drop the fragment. The base of `link` is `base_url` when the owner passes one (an origin: scheme and host only), else the address the owner reached lasso at (`X-Forwarded-Host` and `X-Forwarded-Proto`, else `Host`). Behind a tunnel or on loopback that may not be the public address the phone needs, so the owner's UI should pass `base_url`.

**The exchange.** The app calls `POST /client/v1/pair`:

```json
{"code": "lpc_…", "label": "Ann's iPhone", "platform": "ios"}
```

`label` is one line up to 64 characters (default `phone`); `platform` is `ios` or `android` (else `other`). Success:

```json
{"api": 1, "token": "lcd_…", "device": {"id": "d_…", "label": "…", "platform": "ios"}, "user": {"id": "u_…", "name": "Ann"}}
```

The app keeps `token` in the Keychain or Keystore. The code's use and the device's creation are one transaction, so a code works once even raced.

**Failure is one answer.** An unknown code, an expired one, one already used, one for a disabled or deleted user, and a malformed code all answer `401 pairing_failed`, byte for byte the same, with the same work done. Attempts are rate-limited: ten failures from one source address (as the edge reports it, `Cf-Connecting-Ip` first) or sixty overall in ten minutes, and further attempts get `429 rate_limited` with `Retry-After: 600` without trying the code. A code is 128 random bits, so the limit is hygiene, not the defence. A request whose body is not JSON gets `400 invalid_request`: that says nothing about any code.

## Authentication and errors

Everything except `GET /client/pair` and `POST /client/v1/pair` takes `Authorization: Bearer lcd_…`. Only that header: a token in a query string is not accepted (query strings get logged). An unknown, malformed, revoked or disabled-user token, and no token at all, all answer the same `401 unauthorized`. Authentication runs before routing, so an unauthenticated caller learns nothing about which routes exist. Revoking a device, disabling a user and removing a grant take effect on the next request, and end open streams.

Errors are JSON with a status and a stable `code`:

```json
{"error": {"code": "not_found", "message": "Not found."}}
```

| status | code | when |
| --- | --- | --- |
| 400 | `invalid_request` | a malformed body, field, cursor or id |
| 401 | `unauthorized` | the token does not identify an enabled user's device |
| 401 | `pairing_failed` | the pairing did not work, for any reason |
| 404 | `not_found` | the id is unknown, or belongs to someone else, or to a bot the user is not granted: the same answer |
| 405 | `method_not_allowed` | `Allow` names the methods |
| 409 | `bot_unavailable` | the bot is stopped, or cannot take app messages now; `message` says why |
| 413 | `too_large` | a body or file over its limit |
| 415 | `unsupported_type` | an attachment of a type not allowed |
| 429 | `rate_limited` | `Retry-After` in seconds |
| 502 | `bot_unavailable` | lasso cannot reach the bot's host |
| 500 | `internal` | lasso's side |

Messages never contain the request's text, tokens or internal detail. Every response carries `Cache-Control: no-store` (except an attachment download, which is `private, max-age=86400`) and `X-Content-Type-Options: nosniff`. Nothing is logged by the API: no tokens, no message text.

## Routes

All under `/client/v1`, JSON unless stated. `{bot}` is the bot's name, ids are the opaque strings above.

| route | purpose |
| --- | --- |
| `POST /pair` | exchange a pairing code for a device token (above) |
| `GET /me` | who the token is, and the user's bots |
| `GET /bots` | `{"bots": […]}`, the same bot objects as `/me` |
| `GET /bots/{bot}` | `{"bot": {…}}` |
| `GET /bots/{bot}/avatar` | the bot's picture (PNG, JPEG, WebP or GIF, `Cache-Control` immutable); 404 when it has none |
| `GET /conversations/{id}/messages` | the history, paged |
| `POST /conversations/{id}/messages` | send a message |
| `GET /conversations/{id}/events` | the live stream (SSE) |
| `POST /conversations/{id}/attachments` | upload a file |
| `GET /attachments/{id}` | download a file of the user's (their own upload, or one the bot sent them) |
| `POST /conversations/{id}/calls` | start a voice call |
| `GET /calls/{id}` | a call |
| `POST /calls/{id}/end` | hang up |
| `DELETE /device` | sign this device out: revoke its token |

### `GET /me`

```json
{
  "api": 1,
  "user": {"id": "u_…", "name": "Ann"},
  "device": {"id": "d_…", "label": "Ann's iPhone", "platform": "ios", "created_at": "…"},
  "bots": [{
    "name": "jess",
    "avatar_url": "/client/v1/bots/jess/avatar?v=1731200000",
    "state": "available",
    "state_reason": "",
    "capabilities": {"chat": true, "voice": true, "attachments_in": true, "attachments_out": true, "end_call": true, "streaming_text": false},
    "is_owner": true,
    "conversation_id": "c_…"
  }]
}
```

Only granted bots appear, and only this: a name, a picture, a coarse state, what the bot can do for the app, whether the user owns it, and their conversation id. No folder, host, model, runtime, instructions or other user.

`state` is one of `available`, `busy` (working a turn or waiting on its human), `starting` (running but not yet listening: a message waits for it), `stopped` (a message is refused) and `unavailable` (cannot take app messages now: for a claude bot, a channel without the client tools that needs the bot restarted, or no channel; `state_reason` says). The state is cached for five seconds per bot. `capabilities.chat` and `voice` are false when `unavailable`. `attachments_out` and `end_call` are the bot's ability to send files and hang up: always true for a claude bot, and for an omp bot only when it has lasso's MCP server ([omp](#omp-bots)). `streaming_text` is always false: replies arrive whole ([omp](#omp-bots)). `avatar_url` is absent without a picture; its query is the revision, so the URL can be cached forever.

### `GET /conversations/{id}/messages`

Query: `limit` (1-100, default 50), and at most one of `before` and `after`, each a `cursor` taken from a message or `older_cursor`. With neither it answers the newest page. Messages are always oldest first.

```json
{
  "conversation_id": "c_…",
  "messages": [{
    "id": "m_…", "conversation_id": "c_…", "cursor": "42",
    "role": "user", "text": "…", "done": true, "mode": "text", "call_id": "",
    "attachments": [{"id": "a_…", "name": "photo.jpg", "type": "image/jpeg", "size": 123456, "url": "/client/v1/attachments/a_…"}],
    "state": "delivered", "failed_reason": "", "created_at": "…"
  }],
  "older_cursor": "40",
  "events_cursor": "57"
}
```

- `role` is `user` or `bot`. `done` is true except for a bot's interim note (`done: false`). `mode` is `text` or `voice`; `call_id` is set for voice, and for a bot message sent during a call.
- `state` is on the user's own messages: `queued` (lasso has it, the bot has not taken it), `delivered` (the bot's channel took it), `failed` (with `failed_reason`: the bot was stopped, or never picked it up within a day).
- `older_cursor` is the `before` for the next older page, or `null` at the start of history. After a page fetched with `after` it is `null`.
- `events_cursor` is the stream's position as of just before the page was read. Open the stream with it as `Last-Event-ID` and nothing that landed while paging is missed; a message seen twice (in the page and the stream) is deduplicated by `id`.

### `POST /conversations/{id}/messages`

```json
{"text": "What is on my calendar?", "attachments": ["a_…"], "mode": "text", "call_id": "", "client_id": "7b1c…"}
```

- `text` is up to 16000 characters of UTF-8. A message needs text, attachments, or both. At most four attachments, each an id from an upload to **this** conversation by **this** user, not yet used.
- `mode` is `text` (default) or `voice`. A voice message needs `call_id` of an open call in this conversation; `call_id` with `text` mode is refused. The app does the speech recognition and sends text.
- `client_id` (1-64 of letters, digits, `.`, `_`, `-`) makes a send idempotent: resending the same `client_id` in the conversation answers `200` with the original message and `"replayed": true`, queuing nothing. Use a fresh UUID per message and reuse it when retrying after a dropped connection.

Answers `202 {"message": {…}, "replayed": false}`, with `state: "queued"`; delivery to the bot is asynchronous and reported by a `message_state` event. `409 bot_unavailable` when the bot is stopped or `unavailable`; a bot that is only `starting` takes the message, which waits for up to a day.

The identity sent to the bot comes from the token. Any other field in the body (`user_id`, `is_owner`, `verified`, …) is ignored.

## The event stream

`GET /conversations/{id}/events` is Server-Sent Events (`text/event-stream`), one stream per conversation. Persisted events carry an SSE `id:` (an integer that rises within the conversation); ephemeral ones do not.

- **Start:** with no `Last-Event-ID` (header, or `last_event_id` in the query for clients that cannot set headers) the stream begins at *now*: nothing is replayed. Fetch the history first and pass its `events_cursor`.
- **Resume:** with `Last-Event-ID: n` it replays every persisted event after `n`, once, then goes live. Events are kept 48 hours. If `n` is older than the oldest kept event the stream sends `resync` and then goes live: re-read the history.
- **Keepalive:** a `: keepalive` comment every 15 seconds (Cloudflare cuts a silent response at 100 seconds; so do phones' radios). A stream lives at most 55 minutes, then ends with `closed` (`reconnect`); reconnect with the last id. At most three open streams per device and 20 opens a minute.
- **Revocation:** every second the stream re-checks that the device is not revoked, the user is enabled and the grant stands. If not it sends `closed` with `access_revoked` and ends.

| event | `id:` | `data` |
| --- | --- | --- |
| `hello` | no | `{conversation_id, bot_state, bot_state_reason?, events_cursor, call}`: always first. `call` is the open call object or `null` |
| `resync` | no | `{reason: "events_pruned"}` |
| `message` | yes | `{…message…}`: a user message (also from the user's other devices) or a bot message, interim ones included (`done: false`) |
| `message_state` | yes | `{message_id, state: "delivered"\|"failed", reason?}` |
| `turn_end` | yes | `{message_id}`: the bot finished the turn for that message without a closing message (omp, a turn that ended on tool calls) |
| `call_started` | yes | `{call_id, started_at}` |
| `call_ended` | yes | `{call_id, reason: "user"\|"bot"\|"timeout"\|"replaced", ended_at, last_words?}`: the app speaks `last_words` (if any) and hangs up; they are also stored as a bot message |
| `activity` | no | `{text, message_id}`: what the bot is doing, e.g. "Reading a file", "Running a command". **Plain only**: the kind of each step, never a path, command or server name (the terminal may hold other people's business). claude bots only |
| `bot_state` | no | `{state, reason?}`: when the bot's coarse state changes (polled every three seconds) |
| `closed` | no | `{reason: "access_revoked"\|"reconnect"}` |

## Voice calls

The app does speech recognition and synthesis; lasso carries text. A call exists so the server knows one is open.

- `POST /conversations/{id}/calls` → `201 {"call": {"id": "call_…", "conversation_id": "c_…", "state": "open", "started_at": "…"}}`. Starting a call ends one still open in the conversation (`call_ended` `replaced`). `409 bot_unavailable` when the bot is stopped or unavailable.
- Voice messages carry `mode: "voice"` and the `call_id`. The bot's replies during the call carry `call_id` too.
- `POST /calls/{id}/end` hangs up: `{"call": {…, "state": "ended", "end_reason": "user", "ended_at": "…"}}`. It is idempotent. The bot is told once ("The person hung up") so it does not answer into silence.
- **The bot can hang up** (`end_call`, [below](#delivery-to-the-bot)): the stream gets `message` (the last words, if any) then `call_ended` `reason: "bot"` with `last_words`. The app speaks them and ends the call. Further voice messages on the call are refused (`400`).
- A call with no voice message or bot reply for 30 minutes, or open for four hours, is ended by lasso (`timeout`), and the bot is told.

## Attachments

**Upload:** `POST /conversations/{id}/attachments?name=photo.jpg` with the file as the raw request body (any `Content-Type`, it is ignored). The name is display only: lasso keeps a base name without separators or control characters (up to 80 characters), and never uses it as a path. Answers `201 {"attachment": {id, name, type, size, url}}`. The file is saved in the bot's folder at `.lasso/client/<user id>/<attachment id>.<ext>` (0600 in a 0700 directory), so the bot can read it; the extension comes from the sniffed type.

**Types are sniffed from the bytes** (`http.DetectContentType`), never taken from the name or the client's header. Allowed: PNG, JPEG, WebP, GIF, PDF, plain text (valid UTF-8 with no NUL; this includes SVG, which is therefore only ever text and is downloaded, never rendered), MP3, WAV, AIFF, Ogg, MP4, WebM and ZIP. Anything else is `415`. **HEIC is not detected**: the app converts photos to JPEG before it uploads. An upload not sent in a message within 24 hours is deleted, file and row.

**Download:** `GET /attachments/{id}` serves the bytes as what they are (the sniffed type must still equal the recorded one), with `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none'; sandbox`, a `Content-Disposition` of `inline` for images, audio, video and PDF and `attachment` for the rest, and Range support for audio and video. Only attachments of the token's user and a conversation they still hold are served; anything else is `404`.

**What the bot sends** are files the bot attaches to a message, by path, in `client_reply`:

- Registered **at send time**. Lasso reads each file from the bot's folder once, confined to it, and stores **a copy** (`.lasso/client/<user id>/out/<attachment id>.<ext>`) addressed by id. Later changes to the original change nothing; there is no route from an id to any other path, and no directory, glob or link is ever served.
- The path is absolute or relative to the bot's folder. It is refused if it leaves the folder lexically, if **any component below the folder is a symbolic link**, if it is not a regular file, over 20 MB, of a type not allowed, or among lasso's and the runtimes' own files and the usual credential places: anything under `.lasso`, `.mise`, `.omp`, `.claude`, `.git`, `.ssh`, `.gnupg`, `.aws`, and `fnox.toml`, `age.txt`, `.env*`, `id_rsa`, `id_ed25519`. On lasso's machine the read is also an `os.Root` open, which refuses to leave the folder at the system call. On another host the folder is read over SFTP with the same component walk.
- A refusal fails the whole `client_reply`: nothing is sent, and the bot is told which file and why (over the spool, where nobody can be told, the text still goes and says an attachment could not be sent).

By design a bot can send any non-denied file in its folder to the person it is talking to; the folder is the bot's, and an owner who does not want a file sent keeps it outside.

## Limits

| | |
| --- | --- |
| message text | 16000 characters; request body 256 KB |
| attachments | 20 MB each, 4 per message, 20 uploads waiting to be sent, 20 uploads a minute |
| messages | 30 a minute per user |
| history page | 100 messages |
| bot message text | 64 KB (the bot sends longer answers in parts with `done=false`) |
| streams | 3 open per device, 20 opens a minute, 55 minutes each |
| pairing | 10 failures per source and 60 overall per 10 minutes; a code lives 10 minutes |
| events kept | 48 hours; calls idle 30 minutes end; messages and conversations are kept until the user is deleted |
| name, label | 64 characters, one line |

## Delivery to the bot

One bot has one session, and it serves everyone: the app's users, its jobs, Talk and the human at the terminal. A person's message is a `bot_events` row of kind `client`, with `meta` that lasso alone fills in from its records:

| attribute | value |
| --- | --- |
| `event` | `message`, or `call_ended` |
| `user_id`, `user_name` | the token's user and their display name (one line, 64 characters) |
| `verified` | always `true` here: the token proved it |
| `verified_via` | always `paired`: lasso paired this person's device itself |
| `paired_by` | who minted the pairing (the verified caller's name, `local`, or `mcp:<id>`) |
| `is_owner` | `true` when the person holds an owner grant on this bot, else `false` |
| `conversation_id` | this person's conversation with this bot |
| `message_id` | the message |
| `mode` | `text` or `voice` |
| `call_id` | the call, for voice |
| `attachments` | a JSON list `[{path, name, type, size}]` of the files the person sent, absolute paths in the bot's folder |
| `end_reason` | on `call_ended`: `user` or `timeout` |

The message text is the event's content, followed by `Attached image: <path>` / `Attached file: <path>` lines. Those lines are the person's side of the page; the `attachments` attribute is lasso's.

Because the identity is in attributes lasso sets and in nothing else, no header, body field or sentence in a message can claim to be the owner or another user. The bot is told to trust only the attributes.

### claude bots

The event reaches the bot through `lasso-channel`, as `<channel source="lasso-channel" trigger="client" user_id="…" user_name="…" verified="true" verified_via="paired" paired_by="…" is_owner="false" conversation_id="…" message_id="…" mode="text" …>`. `event_id` and `fired_at` are added as for jobs. The tools beside Talk's `reply` (Talk's only):

- **`client_reply {conversation_id, text?, done?, attachments?}`**: the bot's message to the person, in that conversation only. `done=false` is an interim note ("On it, checking the invoices"); `done=true` (default) closes the turn. `attachments` is a list of paths. Text or attachments are required.
- **`end_call {call_id, last_words?}`**: hang up. The app speaks `last_words` and ends the call.

On lasso's machine these are HTTP calls to `/bot-channel/<bot>/client-reply` and `…/end-call` on the bot's own channel token, and errors come back to the bot. A bot on another host leaves them in the spool's `outbox/` (`{"kind":"client_reply",…}`, `{"kind":"end_call",…}`), which the spool loop collects over ssh within seconds; the tool answers "handed to lasso" and cannot report an error afterward (a refused attachment still delivers the text, with a line saying so). The launch line allows all three tools (`--allowedTools`), so a bot outside bypass mode is not stopped by a permission dialog.

`botChannelInstructions` tells the bot: only a verified owner is its human, everyone else is an outside person it helps only as its instructions allow, who gets nothing of its human's or of other people's; replies go through `client_reply` with the tag's `conversation_id`; interim notes for long work; voice replies are short plain spoken sentences with no markdown, lists, code or links; when and how to `end_call` (on goodbye or when finished, never mid-answer); and `event="call_ended"` needs no reply. The channel never declares `claude/channel/permission`, so a person can never approve the session's tool use.

A channel announces what it can do: its poll header is `X-Lasso-Channel: talk client` (a spool channel's heartbeat ends `talk client`), stored as `bots.channel_client`. A connected channel without `client` is an older one without the client tools: the bot reads `unavailable` and sends are refused until it restarts (on another host, until lasso there is updated). A channel not yet connected reads `starting` and the message waits.

### omp bots

There is no channel process. The message is a bridge prompt, written by `botOmpClientMessage`:

```
[lasso client message from=u_… name="Ann" verified=paired paired_by="you@example.com" owner=yes conversation=c_… message=m_… mode=voice call=call_… nonce=0123456789abcdef]
----- the person's words begin 0123456789abcdef -----
…the person's text…
----- the person's words end 0123456789abcdef -----
```

The first line is lasso's: a plain line, never an XML tag (omp reads tags in its context as system-authored). The person's words sit between two markers that carry a random nonce nobody could know when they wrote them, so nothing inside can pose as the header or close the quote. The system prompt (`botOmpClientInstructions`) says what the line means, the same rules as the channel's, and that the answer text is delivered automatically.

**Replies are routed by what lasso delivered, never by text.** lasso keeps each nonce it issued. The bridge already forwards omp's `message_end` events (token deltas are the only thing it drops); lasso reads them. A *user* message whose first line carries a nonce lasso issued for this bot opens that person's turn; any other user message (the human typing in the terminal, a job, a forged header with no valid nonce) closes it. During a person's turn, each assistant message's text goes to that conversation: a message that calls tools is an interim note (`done: false`), one that does not is the closing message (`done: true`). When the run ends without a closing message, `turn_end`. Outside a person's turn assistant text goes nowhere. Two people's messages are separate turns (omp queues the second as a follow-up while it works), and a message lands only in the conversation whose nonce opened the turn. A late reply is never carried into another person's conversation: there is no carrying at all.

**Attachments and hanging up** use lasso's MCP server, which every local omp bot already has (`.omp/mcp.json`, `server "lasso"`): `client_reply {name, conversation_id, text?, done?, attachments?}` and `client_end_call {name, call_id, last_words?}`. `/mcp` does not know which bot is calling, so these two are limited to the conversation (or its call) the bot is **answering at that moment** (the open turn above); from outside a turn, or for another conversation, they are refused. Tool names appear to omp as its MCP prefix gives them (`lasso` server).

**Gaps for omp** (precisely):

- No token streaming: replies arrive at message level (`streaming_text` is false). The bridge drops `message_update` on purpose; forwarding deltas needs a bridge frame change and a `PROTOCOL` bump (`botBridgeProtocol` in `botomp.go`, `PROTOCOL` in `lasso-bridge.ts`, which restarts every omp bot). Neither is needed for what is built.
- No `activity` events for omp.
- A bot on **another host**, or any omp bot while `MCP_OAUTH` gates `/mcp`, has no `lasso` MCP server, so it cannot send files or hang up: `attachments_out` and `end_call` are false. Its text, its inbound attachments and the person's hang-up notice work.
- The routing relies on omp emitting a `message_end` for the queued user message when it starts it, as it does for any prompt (its session file records messages the same way). It is covered by lasso's tests against a fake bridge and has not been run against a live omp in this change; a new omp should be checked by sending a client message to a real bot.
- The shared omp session means a person's turn and a job's can be adjacent in one context: the header, not isolation, is what separates them.

## Managing people

The owner's side is `/api/app-users` (behind `UI_AUTH` like the rest of `/api`, and kept off machine callers) with a matching tool for each on `/mcp` (fleet-wide credentials only). A web UI is a later step and needs nothing more than these.

| `/api` route | `/mcp` tool | |
| --- | --- | --- |
| `GET /api/app-users[?bot=]` | `list_app_users` | every user with `grants` and `devices` (never tokens); filtered to a bot's users with `bot` |
| `POST /api/app-users {name, email?}` | `create_app_user` | |
| `GET /api/app-users/<id>` | | one user |
| `PUT /api/app-users/<id> {name?, email?, disabled?}` | `update_app_user` | disabling ends their requests and streams |
| `DELETE /api/app-users/<id>` | `delete_app_user` | the user, devices, grants, pairings, conversations, messages, events, calls and uploaded files |
| `PUT /api/app-users/<id>/grants/<bot> {owner?}` | `set_app_user_grant` | grants, or changes the owner flag |
| `DELETE /api/app-users/<id>/grants/<bot>` | `revoke_app_user_grant` | |
| `POST /api/app-users/<id>/pairings {base_url?}` | `create_app_pairing` | the code and links, once |
| `GET /api/app-users/<id>/pairings` | | recent pairings, never their codes |
| `DELETE /api/app-users/<id>/pairings/<pid>` | | cancel an unused one |
| `GET /api/app-users/<id>/devices` | | the user's devices |
| `DELETE /api/app-users/<id>/devices/<did>` | `revoke_app_device` | revoke one device |

A user view: `{id, name, email, disabled, created_at, updated_at, grants: [{bot, owner, created_at}], devices: [{id, label, platform, paired_by, created_at, last_seen_at, revoked_at?}]}`. Deleting a bot removes its grants, conversations and attachments rows (its folder is the owner's).

The tools that mint pairings return the code to the calling agent: it is single-use and expires in ten minutes, but it should go to the person, not into a shared channel.

## Security model

- **The token is the only credential and the only identity.** There is no session cookie, no ambient credential and no state a web page could ride, so `/client` is exempt from the cross-site check and from CORS (none is sent). `Authorization` is read only from the header.
- **Self-credentialed, like `/talk`:** exempt from `UI_AUTH`, in `selfCredentialed` (a network listener passes it unproven, with no caller, `Authorization` untouched), in `machinePaths` and `crossSiteExempt`, and bypassed at Cloudflare Access. The local guard's `Host` check still applies on every listener except the access one: a public hostname on another listener must be in `LASSO_HOSTNAMES`.
- **It exposes nothing of `/api`.** The router serves only the routes above; a path that does not match one is `404` after authentication. A device token is not a lasso token and satisfies no gate of `/api`, `/mcp`, `/herdr-mcp` or `/cdp`: on a network listener those answer `403` to a caller with no verified edge identity whatever it sends (tested), and on the loopback listener the token is simply not looked at.
- **Scoping.** Every route that names a conversation, attachment or call resolves the id, compares its owner to the token's user, and checks the user still holds the grant. Unknown, someone else's and ungranted are the same `404` (compared byte for byte in tests). Ids are 96 to 128 random bits and are never trusted alone.
- **No client-supplied identity.** The bot's attributes come from lasso's records (the token's user, the grant, the pairing). The body's other fields are ignored. Display names and `paired_by` are scrubbed to one line before they become attributes.
- **Constant-time comparisons.** Tokens and codes are compared by their hashes with `subtle.ConstantTimeCompare`; the lookup is by hash.
- **Revocation is immediate**: it is evaluated per request and per second on streams, not cached.
- **Replies cannot cross.** A claude reply names its conversation, which must belong to the calling bot (its channel token) and to a user still enabled and granted; an omp reply is routed by nonce. Nothing is carried from one request to another, unlike Talk's late replies.
- **Files**: described above. The sniffed type decides what a file is, with `nosniff` and a sandbox CSP; no SVG is ever an image; names are generated; the per-user directory is `.lasso/client/<id>`.
- **Untrusted text everywhere**: the person's words, the bot's words and file names are data. The page for the pairing link reflects nothing.
- **Logging**: the API logs nothing of requests. Pairing failures are counted in memory, by source address, only.
- **Residual trust:** `/mcp` is open on loopback (unless `MCP_OAUTH`), so a local process or a bot that can call `/mcp` could use `list_app_users` or `create_app_pairing` and pair a device, as it can already `create_agent`; the user tools require a fleet-wide credential, and an omp bot's `client_reply` is limited to its open turn.

## Deployment

The API is the same binary and the same listeners; nothing needs a new port.

- **Cloudflare Access**: the phone cannot sign in to Access, so bypass the whole prefix. Add a self-hosted Access application for `lasso.example.com/client/` with a single **Bypass** policy that includes everyone (the more specific path wins). That covers the pairing page, `/client/v1/pair` and the API, all of which carry their own credential. A bypassed request has no Access assertion, and the access listener passes `/client/` unproven, like `/talk/`. See [Cloudflare](../deployment/cloudflare.md#7-webhooks-and-talk-from-outside-only-if-you-need-them). Without the bypass, the alternative is an Access service token as custom headers where the app can send them.
- **Hostname**: on `-listen-access` the Host check is skipped. On any other listener the public name must be in `LASSO_HOSTNAMES`.
- **Streams** need an edge that does not buffer or cut long responses: Cloudflare Tunnel passes SSE; the 15 second keepalive keeps the 100 second idle limit quiet.
- A bot must run a channel with the client tools: after lasso is updated, claude bots restart onto it (`unavailable` with the reason until then; lasso rewrites each bot's task on the runner's start so the new `--allowedTools` applies), and a remote host's lasso must be updated.

## Gaps and seams

- Push notifications (APNs, FCM), server-side speech synthesis or recognition, and a web UI for people and devices are not built. The seams: `appendClientEvent` is the single place a message or call event is born (a push transport would subscribe there, as `publishNotification` does for bots), the app does speech, and the management routes are complete for a UI.
- Retention of messages and uploads is until the user or bot is deleted; there is no per-conversation delete, and the bot's folder keeps uploads (the owner's to prune; unused uploads are removed after a day).
- A stream's `activity` and the state are polled (two to four seconds; the terminal is read, over SFTP for a remote bot).
- The two limits that are per process (rate windows, stream counts) reset on a restart and are not shared between two lassos on one db.
