<!-- Design rationale and contract: read before changing the code it names. -->

# Asks (`src/clientasks.go`, `src/clientasks_thread.go`, `src/clientasks_operator.go`, `src/clientasks_agent.go`, `src/clihook.go`, `src/botchannel_asks.go`, `claude-plugin/lasso-asks`, `AsksDialog.tsx`, `AsksTab.tsx`, `BotThreads.tsx`, `lib/asks.ts`)

An ask is how a bot that is blocked on a person's decision gets it: one question, a card in that person's Inbox in lasso's phone app, answered with a tap. The bot calls a tool and carries on; the answer reaches it later as a client event. It extends the [client API](client-api.md) and [client threads](client-threads.md). Tests: `clientasks_test.go`, `clientasks_operator_test.go`, `clientasks_agent_test.go`.

## Contents

- [Model](#model)
- [Who a bot may ask](#who-a-bot-may-ask)
- [What a bot sends](#what-a-bot-sends)
- [Lifecycle](#lifecycle)
- [What the bot gets back](#what-the-bot-gets-back)
- [The person's routes](#the-persons-routes)
- [Events, badge and push](#events-badge-and-push)
- [The operator's Inbox](#the-operators-inbox)
- [An agent's question](#an-agents-question)
- [The owner's side](#the-owners-side)
- [Limits](#limits)
- [Security](#security)
- [Client work: iPhone](#client-work-iphone)
- [Gaps and seams](#gaps-and-seams)

## Model

- **An ask belongs to one person and one bot.** The phone app is per person (a device token is a person), so there is no shared queue: every app user has an Inbox of their own, holding only what bots asked *them*. An ask's id is `ask_` plus 96 random bits.
- **It hangs off the person's main thread with that bot** (`thread_id`), which is where "Open thread" goes, where its events are filed on the person stream, and whose mute silences its pushes. It is not a message: nothing is added to the thread's history.
- **Storage** is `app_asks` in `lasso.db`. Two lassos sharing a db both serve it; only the runner-lock holder expires asks and sends held pushes.
- **`features` in `GET /me` lists `asks`**; an app shows the Inbox only then.

## Who a bot may ask

A bot may put a card in a person's Inbox without that person having written first. That is the one deliberate exception to "a bot cannot start a conversation" ([client-api.md](client-api.md#security-model)), and it is bounded by who the person is:

- **Its owner, at any time.** `to` omitted (or `"owner"`) means the bot's owner: the person holding an owner grant on it. A bot with no owner in the app is told how its human becomes one. A bot with several owners must name one by `user_id`; the error lists them.
- **Anyone else it is granted to, only while it is answering a message of theirs.** A claude bot passes that message's open `reply_token` with the person's `user_id` as `to`; an omp bot must be inside that person's open turn. A closed or expired token, another person's token, and another bot's message prove nothing. So a bot serving outside people can ask the customer it is talking to ("Approve the quote?"), and cannot reach a person who has not written to it.
- **Never** a person it is not granted to, a disabled user, or review access's demo person. The demo bot cannot ask.

The answer carries `is_owner`, re-read from the grant when the bot takes the event, so an outside person's answer never reads as the owner's.

## What a bot sends

A claude bot has `ask` and `ask_withdraw` on its channel (`mcp__lasso-channel__ask`, allowed on the launch line). An omp bot has `client_ask` and `client_ask_withdraw` on lasso's `/mcp`, which take its `name`. A claude bot on another host leaves `{"kind": "ask", …}` in the spool's `outbox/`.

```json
{
  "to": "u_…",
  "reply_token": "lrt_…",
  "area": "EXA",
  "subject": "Body Helm",
  "blocking": "money",
  "question": "Send Curtis the NDA reply?",
  "why": "VIP client; IP + liability move to the SOW",
  "attachment": {"kind": "draft", "label": "draft · EXA Mail", "ref": "exa-mail:cfmaaaarl", "preview": "Hi Curtis, Thanks for the quick…"},
  "options": [
    {"id": "send", "label": "Send as is", "default": true},
    {"id": "cc", "label": "Send, cc Saul only"},
    {"id": "hold", "label": "Hold until after the call"}
  ],
  "allow_other": true,
  "deadline": "2026-11-18T17:00:00-08:00",
  "on_expire": "hold; re-ask in the 8am plan",
  "dedupe_key": "curtis-nda-v2"
}
```

| field | |
| --- | --- |
| `to`, `reply_token` | [who](#who-a-bot-may-ask); both optional. The bot itself is never in the body: the channel token names it (on `/mcp`, `name`, behind `requireHost`) |
| `question` | required: one line, at most 200 characters |
| `why` | what it unblocks, at most 400 characters |
| `area`, `subject` | the card's header after the bot's name: 32 and 64 characters, one line |
| `blocking` | `money`, `people`, `deadline` or `none` (default). It sets the sort and whether the person is notified |
| `options` | at most 4: `id` (`[a-z0-9][a-z0-9_-]{0,31}`, unique), `label` (60 characters), at most one `default` |
| `allow_other` | default `true`: the person may answer in their own words, typed or spoken |
| `attachment` | `kind` is `draft`, `pr`, `doc`, `link` or `upload`; `label` (64), `ref` (200, the bot's own reference, shown as text), `url` (https only; required for `link`), `preview` (2000 characters of text). `upload` is a slot the person fills with files |
| `deadline` | RFC 3339 with an offset, in the future, at most 30 days out |
| `on_expire` | what the bot will do if nobody answers, 300 characters. lasso stores it and hands it back at expiry; it runs nothing |
| `dedupe_key` | `[A-Za-z0-9][A-Za-z0-9._:-]{0,63}` |

Text a person reads is cleaned: control and invisible formatting characters become spaces, and `question`, `area`, `subject`, labels and `on_expire` are one line. An ask with no options, `allow_other` false and no upload slot is refused, since nobody could answer it. A field over its length is refused, never cut, so the bot knows to shorten it.

The tool answers at once:

```json
{"ask_id": "ask_…", "state": "created", "to": {"user_id": "u_…", "name": "Ann"}, "link": "lasso://ask/ask_…"}
```

`link` is what a bot posts where it would otherwise repeat the question (a chat channel, a morning plan). Over the spool lasso cannot answer, so the tool says the ask was handed over and the bot names it by `dedupe_key`.

**One open ask per `dedupe_key`**, per person and bot (a unique partial index). Asking again with the key of an open ask updates that card in place: same id, `revision` up by one, `state: "updated"`. A changed question or option set makes the card `new` again; nothing else does, and an update never pushes twice. Once an ask is closed its key is free: asking with it makes a new ask.

**`ask_withdraw`** takes `ask_id`, or `dedupe_key` (every open ask of the bot's with it, or one person's with `to`). The card leaves the Inbox and no event comes back.

**`ask_resolve`** (omp: `client_ask_resolve`; spool kind `ask_resolve`) is the bot saying it has finished what an ask was about: it carried out the answer, or settled it another way. It takes `ask_id`, or `dedupe_key` (that key's newest ask, one person's with `to`), and `note`, what happened, up to 2000 characters. The ask records `resolved_at` and `resolution`; the note is posted as the bot's message in the ask's own thread, when the person opened one, and that thread is archived (the note goes first, since a message reopens an archived thread). An ask still open is withdrawn first. Once per ask, only by the asking bot, and only while the person still holds it. The ask is announced, so every Inbox shows it resolved.

## Lifecycle

```
new ─► seen ─► answered ─► the bot is told
 │       │
 │       └─► snoozed ─► back at the chosen time
 ├─► expired ─► the bot is told, with its on_expire
 ├─► dropped ─► the bot is told, once, a minute later (until then the person may reopen it)
 └─► withdrawn (by the bot, or the grant went): nobody is told
```

Stored `state` is `open`, `answered`, `dropped`, `expired` or `withdrawn`; what the app reads splits `open` into `new`, `seen` and `snoozed`.

- **Snooze** ("Later") is the person's own: the ask leaves the waiting list until `snoozed_until`, the bot hears nothing, and the deadline still runs. When the time comes the runner clears it and announces the ask again.
- **Expiry**: an ask whose deadline passed is `expired`. The runner looks every 15 seconds, and every route that names an ask checks first, so an answer cannot land between the deadline and the runner's look. An ask with no deadline expires 30 days after it was made.
- **Withdrawn by lasso**: removing a person's grant on a bot withdraws that bot's open asks of them without an event. Deleting a person or a bot deletes their asks.
- **Closing happens once.** Every transition out of `open` is one `UPDATE … WHERE state = 'open'`; the loser of a race reads what won.
- A closed ask stays listed for 30 days, then is deleted.

## What the bot gets back

An outcome is a `bot_events` row of kind `client`, delivered by the same adapters as a person's message, with attributes lasso alone sets:

| attribute | |
| --- | --- |
| `event` | `ask_answered`, `ask_expired` or `ask_dropped` |
| `ask_id`, `dedupe_key`, `question` | the ask (the key only when it has one) |
| `user_id`, `user_name`, `verified`, `verified_via`, `is_owner` | as on a message; `is_owner` and the name are re-read at claim. `verified_via` is `operator` for an answer given in lasso's web UI ([below](#the-operators-inbox)) |
| `thread_id`, `conversation_id` | the person's main thread with the bot |
| `choice`, `choice_label` | the option picked, when one was |
| `other` | `"true"` when the person answered in their own words: the event's **body** is those words |
| `answer_message_id` | the person answered with a message in the thread, which reaches the bot on its own, with its files and its own reply token |
| `answered_via`, `answered_at` | the device's platform, which lasso knows, then the gesture the app named: `ios-swipe`, `android-voice`, or just `ios` |
| `on_expire` | on `ask_expired`: the bot's own fallback, handed back |
| `discussion_thread_id` | the ask's own thread, when the person opened one: where `ask_resolve` posts and what it archives |

The body is lasso's own sentence (the go-ahead covers this one action; run your fallback; close the task and do not ask again), except with `other="true"`, where it is the person's words, neutralized like any message so they cannot pose as a channel tag or an omp header. No outcome carries a `reply_token`: none needs a reply.

A claude bot reads `<channel source="lasso-channel" trigger="client" event="ask_answered" ask_id="…" choice="send" …>`. An omp bot reads a header line, with the person's words between nonce markers when there are any:

```
[lasso client event ask_answered from=u_… name="Ann" verified=paired owner=yes thread=c_… ask=ask_… dedupe_key=curtis-nda-v2 choice=send answered_via=ios-swipe answered_at=… choice_label="Send as is" question="Send Curtis the NDA reply?" nonce=0123456789abcdef]
```

**Access is checked when the bot takes the event**, as for a message: if the person was disabled or deleted, the grant removed, or the answering device revoked since, the event is dropped and the bot never sees the answer. The ask still reads `answered` to the person.

An event not taken within a day is dropped with the rest of the bot's queue (a bot stopped that long does not learn the answer; the ask keeps its record).

The instructions (`botAskInstructions` on the channel, `botOmpAskInstructions` in an omp bot's prompt) tell the bot when to ask, how to write one, that an answer is that person's go-ahead for that one action only, and that an outside person's answer is not its human's.

## The person's routes

Under `/client/v1`, with the device token. An ask that is unknown, someone else's, or of a bot the person no longer holds is the same `404`.

| route | |
| --- | --- |
| `GET /asks?state=waiting\|all` | `{"asks": […], "counts": {"waiting": n, "snoozed": n}, "events_cursor": "…"}`. `waiting` (default): open and not snoozed, sorted. `all`: those, then snoozed (soonest back first), then closed (newest first); at most 200 |
| `GET /asks/{id}` | `{"ask": {…}}` |
| `POST /asks/{id}/answer` | `{"choice"? \| "other"? \| "message_id"?, "via"?}`: exactly one of the three |
| `POST /asks/{id}/seen` | marks a `new` ask `seen` |
| `POST /asks/{id}/snooze` | `{"until": "<RFC 3339>"}`, within 30 days; `{"until": null}` brings it back now |
| `POST /asks/{id}/drop` | the person will not answer it. The ask reads `dropped` with `undo_until`, a minute out; the bot is told only once that has passed |
| `POST /asks/{id}/reopen` | undo a drop before `undo_until`: the ask is open again and the bot never hears of the drop. After it, `409 ask_closed` |
| `POST /asks/{id}/thread` | the ask's own thread ([below](#talking-an-ask-over)): `{"ask": {…}, "thread": {…}}`, made the first time, the same thread after. Open or closed alike; an agent's question is `409 no_thread` |

```json
{
  "id": "ask_…", "bot": "jessica", "thread_id": "c_…",
  "area": "EXA", "subject": "Body Helm", "blocking": "money", "rank": 0,
  "question": "Send Curtis the NDA reply?", "why": "VIP client; IP + liability move to the SOW",
  "attachment": {"kind": "draft", "label": "draft · EXA Mail", "ref": "exa-mail:cfmaaaarl", "preview": "Hi Curtis, …"},
  "options": [{"id": "send", "label": "Send as is", "default": true}, {"id": "cc", "label": "Send, cc Saul only"}],
  "allow_other": true, "deadline": "2026-11-19T01:00:00Z", "on_expire": "hold; re-ask in the 8am plan",
  "state": "new", "revision": 1, "created_at": "…", "updated_at": "…"
}
```

`discussion_thread_id` is the ask's own thread once it was opened. `resolved_at` and `resolution` are there once the bot resolved it.

`state` is `new`, `seen`, `snoozed` (with `snoozed_until`), `answered` (with `answer: {choice?, label?, other?, message_id?, via?, at}`), `dropped`, `expired` or `withdrawn` (those with `closed_at`). `options` is always an array. Times are UTC.

**Sort.** `rank` is 0 for `money`, 1 for `people`, 2 for `deadline` or anything due within 24 hours, 3 for the rest; within a rank, oldest first. The server sorts; an app that inserts an ask from the stream sorts by `rank` then `created_at`.

**Answering.**

- `choice` must be one of the ask's option ids.
- `other` is the person's own words, at most 4000 characters, only when `allow_other`.
- `message_id` is a message the person sent in the ask's thread after the ask was made. It is how an `upload` ask is answered (send the files in the thread with the ordinary message route, then answer with that message's id), and it is allowed on any ask that takes `other`.
- `via` (`[a-z][a-z0-9_-]{0,23}`: `swipe`, `tap`, `voice`, `keyboard`, `notification`) is the gesture, which lasso prefixes with the device's platform. Any other field is ignored: who answered comes from the token.
- The same answer again is `200` with the ask as it reads (a retry after a dropped connection). Anything else on a closed ask is `409 ask_closed`: the bot moved on, the deadline passed, or another device answered first.

### Talking an ask over

`thread_id` is the person's main thread with the bot, which holds none of the ask's words, so "Open thread" goes to the ask's **own** thread instead (`clientasks_thread.go`). The first `POST /asks/{id}/thread` makes a standalone thread with the asking bot, titled from `subject` and `question`, whose first message is the bot's: the question, `why`, the attachment's label, ref, url and preview (quoted), and the option labels. Its id is stored on the ask (`app_asks.discussion`, and the first message's in `discussion_message`) and the ask is announced, so every device opens the same thread; one the person deleted is made again. That first message carries the ask as it now reads (`ask` on the message, the same object as `GET /asks/{id}`, read afresh each time messages are listed), so an app draws the live card there and keeps the text as the fallback; a thread made before `discussion_message` existed uses its first message of the bot. What the person writes there reaches the bot as any thread message, and a message of theirs there may answer the ask by `message_id` like one in `thread_id`. Upload asks keep sending their files in `thread_id`.

`PUT /me/settings` takes two more keys, both in `/me`'s `settings`:

- `time_zone`: an IANA name (`America/Los_Angeles`), or `null` for lasso's own zone.
- `ask_quiet_hours`: `{"start": "22:00", "end": "08:00"}` in that zone (the default), or `null` for none.

## Events, badge and push

**One event, `ask`**, on the person stream and the main thread's stream, with an `id:`: `{"ask": {…}}`, the whole ask as it now reads. It is sent when an ask is made, updated, seen, snoozed, back from a snooze, answered, dropped, expired or withdrawn by the bot, and a client applies it by id. It follows the main thread's rule: it reaches the person only while they hold the bot.

**Badge.** `badge` (in `/me`, `hello`, `thread_read` and every push) adds the asks waiting on the person whose bot is not muted. `/me` also has `asks_waiting`, the count regardless of mutes.

**Push** (`kind: "ask"`, APNs category `LASSO_ASK`, `lasso.ask_id`; FCM `ask_id` and `url: lasso://ask/<id>`): title the bot, subtitle `area · subject`, body the question, or "Needs your answer" alone when the person turned previews off. Its collapse id is the ask's, so it never replaces a message's notification.

- **Only `money` and `people` asks push.** `deadline` and `none` raise the badge and wait to be seen.
- **Never in the person's quiet hours.** A push held back is sent by the runner when they end, if the ask is still open and under a day old.
- One push per ask: an update does not push again. A snoozed ask and a muted bot do not push; an ask back from a snooze that never pushed does.
- A device in the foreground gets the `ask` event, not a push, as for messages.

## The operator's Inbox

lasso's own web UI lists and answers asks too. Whoever reaches `/api` is the lasso operator, not a person the client API knows, so the UI acts as **one app user**: the one named in the `asks_operator` setting, or, with none chosen, the only person who owns a bot. With several owners and none chosen it acts as nobody and asks which.

| route | |
| --- | --- |
| `GET /api/asks/operator` | `{operator: {id, name} \| null, chosen, reason?, candidates: [{id, name}]}` |
| `PUT /api/asks/operator` | `{"user_id": "u_…" \| null}`: choose the person, or go back to the automatic one |
| `GET /api/asks`, `GET /api/asks/{id}`, `POST /api/asks/{id}/answer\|seen\|snooze\|drop\|reopen` | [the person's routes](#the-persons-routes), as that person. With nobody to act as, the list answers `operator: null` with the reason and the candidates, and the rest `409 no_operator` |

Behind `UI_AUTH` and off machine callers, like the rest of `/api`.

- **The footer's Inbox button** shows how many asks wait and opens the Inbox dialog (`AsksDialog.tsx`): one ask per slide in a strip that scrolls sideways and snaps, so a two-finger swipe moves exactly one ask. The dialog is one block of one size: header, strip and footer. A slide fills the strip, so only the ask being read is on screen and the dialog never changes height; a long ask scrolls inside its slide. The arrow keys move, a digit picks an option, Enter answers, and answering takes the ask out of the strip, which brings the next one into its place. Waiting, Later and Finished switch what the strip holds; its last slide is always the closing one.
- **The sidebar's Inbox tab** (`AsksTab.tsx`, a built-in tab between Browser and Usage) is the same asks as a list.
- Both read one polled query (`lib/asks.ts`): lasso's SSE feed carries nothing of the phone app's. It polls every 30 s (4 s while the Inbox is on screen) and reads afresh when the window regains focus and when the dialog or tab opens.
- **An answer given here is not tied to a device**, and every process on lasso's machine can reach `/api` on the loopback listener, a bot with a shell included. So the bot is told `verified_via="operator"` (omp: `verified=operator`) where a phone's answer says `paired`, and `answered_via` reads `web-…`. That is a label, not a lock: it is the residual trust the client API already states ([client-api.md](client-api.md#security-model)).
- **A drop can be undone here.** Dropping shows a toast with Undo, and `⌘Z` while the Inbox is on screen does the same, both through `POST /api/asks/{id}/reopen` while the ask's `undo_until` stands. `⌘⇧I` opens and closes the dialog.
- **In the ask's own thread** the Threads page draws its first message as the ask's card (`AskCard` with `inThread`): the attachment's preview open, answerable in place, with no Open thread. Each card names its own radio group, since the same ask can be on screen twice. The Threads page lists archived threads under their own heading, so a resolved ask's thread stays reachable.
- **Open thread** on a card goes to the ask's own thread ([Talking an ask over](#talking-an-ask-over)) in the Bots view's Threads page ([client threads](client-threads.md#the-operators-threads)), and an `upload` ask takes dropped or chosen files there: they are uploaded to the ask's thread, sent as one message, and the ask is answered with that `message_id`.

## An agent's question

A coding agent that asks its human a question (Claude Code's `AskUserQuestion`) blocks its pane until someone answers in that terminal. The `lasso-asks` Claude Code plugin (`claude-plugin/lasso-asks`) puts the same question in the operator's Inbox, phone included, and lasso types the answer back.

- **The plugin is five hooks that run `lasso ask-hook`** (`clihook.go`), which forwards the hook's JSON to `POST /api/asks/agent?pane_id=$HERDR_PANE_ID` on the local lasso (`LASSO_LISTEN`, `UI_AUTH` as `closeme` reads them). It prints nothing and exits 0 whatever happens: a hook's output can reach the model, and a failed hook gets in the agent's way. `LASSO_ASK_HOOK_DEBUG=1` writes what happened to stderr.
- **`PreToolUse` on `AskUserQuestion` files the call.** Each question becomes one ask of the operator's with `agent: {name, host, pane_id, n, of, delivery?}`, an empty `bot` and `thread_id`, the question's header as `subject`, the folder's name as `area`, and each option's description as its `detail`. The dialog in the terminal still opens: the hook decides nothing.
- **All or none.** A call lasso could not answer whole stays in the terminal only: a session outside a herdr pane, a pane lasso cannot find, a multi-select question. The reply says `skipped` and why.
- **The answer is typed once every question of the call is answered**, with the chat view's own `chatAnswer`: it reads the pane first and types only while that question is still on screen. `agent.delivery` then reads `sent`, `waiting` (other questions of the call are open), or the reason nothing was typed, which the card shows so the person knows to answer in the terminal. A dropped or expired question of a call keeps the rest out of the pane.
- **Whoever answers first wins.** `PostToolUse` on `AskUserQuestion`, `UserPromptSubmit`, `Stop` and `SessionEnd` all mean the session moved on, so the session's open asks are withdrawn (`answered_via: "terminal"`). A new call withdraws the session's older one. A session that was killed runs no hook: its asks expire after 24 hours.
- **No bot hears of an agent's ask** and it never pushes. It is listed by the person's routes like any other; `message_id` cannot answer it.
- **Limits**: 40 open agent questions per person; agents on lasso's own host only, since a machine lasso drives is never a client of lasso.
- **Trust**: the route is `/api`'s, so anything on lasso's machine can file a question in the operator's Inbox, as it can already notify them. What an answer can do is bounded by `chatAnswer`: keys go only to a pane whose screen shows that question.

Install it from a checkout: `/plugin marketplace add <repo>/claude-plugin`, then `/plugin install lasso-asks@lasso`.

## The owner's side

`GET /api/app-users/<id>/asks` (behind `UI_AUTH`, off machine callers) lists what a person's bots have asked them, newest first, each `{ask, dedupe_key}`. It is read-only: an ask is answered by its person, in their app.

## Limits

| | |
| --- | --- |
| open asks | 20 per bot and person |
| new asks | 30 an hour per bot; updates by `dedupe_key` 120 an hour |
| the person's actions | 120 a minute per user |
| deadline, snooze | at most 30 days out |
| without a deadline | expires after 30 days |
| closed asks | listed 30 days |

## Security

- **The asker is proven, never claimed**: the channel token (or the spool's host) names a claude bot; on `/mcp` the caller's credential must reach the bot's host, and an open `/mcp` is the local trust the rest of lasso already extends ([client-api.md](client-api.md#security-model), residual trust).
- **The addressee is bounded** as [above](#who-a-bot-may-ask), by lasso's grants and tokens, never by anything in the question.
- **An ask is untrusted text.** The app renders `question`, `why`, labels and `preview` as plain text, never markdown or HTML. `attachment.url` is https only and opens in the browser; `ref` is never fetched or resolved by lasso.
- **The answer is the person's**, from the device token. `is_owner` is the grant's at delivery. An answer is one decision: the instructions say it authorizes that action only.
- **Scoping** is the client API's: the ask's person is compared with the token's on every call, the grant is re-checked, and the three misses read as one `404`.
- **Bounded**: the caps above, one card per `dedupe_key`, and a person can drop any ask.

## Client work: iPhone

- **Capability gate**: show the Inbox only when `GET /me.features` lists `asks`.
- **Bottom bar** Inbox · Threads · Bots, Inbox first, its badge `asks_waiting`.
- **Load** `GET /asks?state=all`, keep `events_cursor`, then apply `ask` events from the person stream by id (insert, replace, or move between sections). `resync` means read the list again.
- **Sections**: Asks you (waiting asks with options or free text), Blocked (waiting asks whose `attachment.kind` is `upload`), then snoozed and closed behind a disclosure.
- **Card**: bot · area · subject, age (red once past `deadline`), question, `why`, the attachment's label, and the options as buttons with the default first. Swipe right answers with the default option (`via: "swipe"`), swipe left opens Later.
- **An agent's question** has `agent` and no `bot` or `thread_id`: head it with `agent.name`, show each option's `detail`, offer no Open thread, and on a closed one show `agent.delivery`.
- **Detail**: the preview in full, options as a single choice, "Other" as a text field with dictation (`other`, `via: "voice"` or `"keyboard"`), Answer & next, Drop, and Open thread (`POST /asks/{id}/thread`, then that thread).
- **Upload asks**: Add photos sends the files in `thread_id` with the ordinary message route, then `POST /asks/{id}/answer {"message_id": …}`.
- **On opening a card** `POST /asks/{id}/seen`. On `409 ask_closed` re-read the ask and show what happened.
- **Push**: register the `LASSO_ASK` category; a tap opens `lasso://ask/<id>`. Send `time_zone` from the device after pairing and when it changes, and offer quiet hours in Settings.

## Gaps and seams

- The Mac app does not list asks.
- An agent's multi-select question, and one from an agent on another host, stay in the terminal.
- A bot cannot list its own open asks; it tracks them by `dedupe_key`.
- `ask_resolve` is the only way a bot writes into an ask's thread without a reply token, and only its one closing note.
- No outcome carries a reply token, so a bot cannot follow an answer with a message in the thread unless the person also wrote there (its `ask_resolve` note excepted).
- An answer to a bot stopped for more than a day is not delivered to it.
- A remote claude bot gets no `ask_id` back from the tool, and an ask it sends that lasso refuses is only logged.
- The rate windows are per process, as the client API's are.
