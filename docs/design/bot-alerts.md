# Bot liveness alerts

Lasso owns its bots and the herdr panes they run in, so it decides whether each bot can actually do its job and tells someone when it stops. Detection runs on the bot runner's tick from signals lasso already reads; alerts fire on transitions only and go out through configurable channels and lasso's own notifications.

Code:

- `bothealth.go`: signals, `evalBotHealth`, the hold, the alert state machine (`stepBotAlert`) and `botHealthTick`.
- `alertchannels.go`: channel storage, secrets at rest, the six dispatchers, heartbeats, delivery and its log.
- `alertsapi.go`: `/api/alerts`.
- `mcp_alerts.go`: `hold_bot`, `release_bot`, `list_bot_alerts` and the channel tools. A bot's own settings are `update_bot`'s `alerts`; the defaults are `update_settings`' `alerts`.
- Frontend: `BotAlertsSettings.tsx` (the Settings group), the Alerts and Hold blocks in `BotSettings.tsx`, the health chip in the Bots view and the simple app.

## Where it runs

`botHealthTick` runs at the end of every `botTick`, so only the holder of `bots.runner.lock` evaluates or sends anything. Two lassos on one `lasso.db` (titan's dev and production) therefore never both page. The result is stored in `bot_alert_state` (health state, reason, since, and the alert state machine's fields), so every lasso's views read the runner's verdict, and a restart or a runner handover neither forgets an outage nor pages again for it.

The tick never waits on the network: health checks and the omp probe run in the background (every 30s and 60s), and the tick reads the newest result.

## Health states and their signals

`botHealthSignalsFor` gathers; `evalBotHealth` decides, purely, in this order:

| state | meaning | signal |
| --- | --- | --- |
| `stopped` | the human's Stop | `bots.stopped` |
| `unknown` | the host did not answer | `botBackend` failed, or `HomeDir` over ssh failed (`botHostPing`) |
| `starting` | inside the start grace lasso gave a launch | `bots.starting` within `botStartGrace` (90s) |
| `gone` | not running | claude: `findBotPane` finds no pane. omp: no bridge connection and no pane |
| `shell` | a pane with no agent in it (herdr restored a plain shell) | claude: the pane's `paneAgentPresence` kind is empty and no live Claude Code session is registered for the folder. omp: a pane, but the bridge socket does not connect |
| `deaf` | up, but cannot hear its channel | any of the reasons below |
| `ok` | none of the above | |

`deaf` reasons, joined into one line:

- **No grant.** The live claude process (its pid from `~/.claude/sessions/<pid>.json`) was launched without a `--dangerously-load-development-channels server:<name>` for one of `channelGrants()` (read from `/proc/<pid>/cmdline`, over ssh on another host, cached per pid). A bare `claude --resume` comes back exactly like this.
- **Shim not polling.** lasso's channel is in the launch, but `bots.channel_seen_at` is older than `botChannelFresh` (75s), or the channel never polled. On another host the spool heartbeat feeds the same column.
- **Acked, never handled.** The newest `delivered` event was acked longer than `handle_within` (default 5 min) ago, and the transcript page the bot list already reads (`botLastLineFor`) shows no `incoming` row from `lasso-channel` with that `event_id` or a later one. A missing grant or a "skipped" channel still subscribes and acks, then hears nothing, so this is the end-to-end proof. A page that starts after the ack (a fresh session, a long turn since) cannot tell and says nothing.
- **Queued.** An event still pending or claimed longer than `handle_within` after it fired (both runtimes).
- **omp not answering.** The bridge is connected, but `get_state` timed out (10s).
- **A health check failed.** A bot's own `health_checks` (`[{name, url, json_field?}]`): an HTTP GET with a 5s timeout and no redirects, failing on a non-2xx or when the top-level `json_field` is not `true` (a channel bridge's `polling_ok`, say).

`unknown` never pages on its own until it has lasted three `down_after` windows, so a network blip to a host does not page about every bot on it.

## Hold

`bots.hold_until` (0 = not held, -1 = until released, else unix seconds) and `hold_reason`. Written only by `setBotHold` (`POST|DELETE /api/bots/<name>/hold`, `hold_bot`, `release_bot`), never by a settings save, which copies them back from the stored row.

While held:

- **Keep-running does not relaunch** (`botTick` and `botOmpTick` check `botHeld` beside `Stopped`). One switch stops both the paging and the relaunch.
- `gone` and `shell` alert nobody, and an outage already alerted for them is resolved at once (sent as `recovered`, reason "put on hold").
- **A bot that is up and deaf still alerts.** The hold says "I know it is down", not "I know it is broken".
- A heartbeat pauses its monitor (Monicron's paused state), so a deliberate outage reads as paused, not as healthy and not as down. A human Stop does the same.

A timed hold lapses by itself; the next tick evaluates the bot normally. Start clears `stopped`, never a hold. A human Stop resolves an outage the same way a hold does.

## The alert state machine

`stepBotAlert(state, health, held, config, now)` returns the next state and at most one event: `down`, `recovered` or `reminder`.

- A bad state (`gone`/`shell` unless held, `deaf`, long `unknown`) must hold for **`down_after`** (default 120s) before `down`. A keep-running relaunch (6 misses = 60s) therefore recovers without paging.
- A down bot must be good for **`up_after`** (default 60s) before `recovered`. Any bad tick in between restarts that window, so there is no flapping.
- `starting` is neutral: it neither starts nor clears a window. A crash loop (gone, starting, gone…) still pages; a slow start does not reset an outage.
- A reason change while down (shell → gone) updates the stored reason and sends nothing.
- Reminders every `remind_every` while still down (0 = off, else at least 300s), at most `remind_max` (default 6).
- A bot with no state row is a baseline: its current state starts a window now, and nothing is sent about the past.

## Channels

`alert_channels(id, name, kind, config, secret, enabled, last_test_at, last_error, …)`. `config` holds the plain fields; `secret` is a JSON object of the secret fields, AES-256-GCM sealed under a key derived (HKDF-SHA256, info `lasso alert channel secrets v1`) from lasso's own age identity, `<lassoDir>/age.txt` (`botkey.go`). That identity is never replaced, so the key is stable for the install. Secrets are decrypted only to send and never returned: a channel lists `secrets_set` (the keys that are set) and a `target` hint that is never a secret (a URL's scheme and host, a chat id, the recipients). A save that leaves a secret out keeps it; `""` clears it. Every value refuses CR, LF and NUL.

Every kind's form is data (`alertKinds`, `GET /api/alerts/kinds`), so the UI renders forms without knowing kinds.

| kind | plain | secret | sends |
| --- | --- | --- | --- |
| `webhook` | | `url`, `signing_secret`, `authorization` | the event as JSON (below) |
| `slack` | | `webhook_url` | `{text, blocks}` |
| `discord` | | `webhook_url` | `{embeds}` to `?wait=true`, mentions off |
| `telegram` | `chat_id`, `thread_id` | `bot_token` | `sendMessage`, `parse_mode: HTML`, text escaped |
| `email` | `host`, `port`, `security`, `username`, `from`, `to` | `password` | an RFC 5322 text message over SMTP |
| `heartbeat` | `bot`, `every`, `on_hold` | `url`, `ping_secret` | pings while its bot is up; pauses its monitor while the bot is stopped or held |

**Webhook signing.** Headers `X-Lasso-Event` (the type), `X-Lasso-Delivery` (a random id), `X-Lasso-Timestamp` (unix seconds) and, with a signing secret, `X-Lasso-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>`. A receiver recomputes the MAC over the raw body, compares in constant time, and refuses a timestamp more than five minutes old. The body is the event: `{id, type, bot, host, state, reason, since, at, downtime_sec, path, url}`; `url` is `path` under the `link_base` setting, when one is set.

**Email.** `security` is `starttls` (the default; refused when the server does not offer it, before any password is sent), `tls` (implicit TLS, port 465) or `none` (a relay on a trusted network). `smtp.PlainAuth` itself refuses to send a password in the clear to anything but localhost. A 20s deadline covers the whole conversation. Addresses go through `net/mail`, the subject is Q-encoded, and since no config value can hold a CR or LF, nothing can add a header.

**Heartbeat** (Monicron, healthchecks.io, any ping URL). One per bot (`config.bot`), never routed. Every request carries the channel's `ping_secret` as `Authorization: Bearer`: once a Monicron monitor has a ping secret, its URL alone can no longer ping, report, pause or resume it. While the bot is `ok` and not alerted down, lasso POSTs `{status:"up", bot, state, since}` to the URL every `every` seconds (30-86400, default 60). It goes silent when the bot is unhealthy, so the monitor's own period and grace decide when it pages, and **it also pages when lasso or titan itself is down**, which no push from lasso can. On a `down` transition it POSTs the reason to `<url>/report` (Monicron stores a report without counting it as a ping); `recovered` pings at once.

A deliberate outage (the bot is stopped, or held and not alerted down) **pauses the monitor** (`on_hold: pause`, the default): `<url>/pause` with the reason as its report, and no pings until it ends. Monicron then shows the monitor paused, never green and never down, and hides it from the public page. When the outage ends lasso pings, resumes (`<url>/resume`) and pings again: a ping to a paused Monicron monitor refreshes its window without resuming it, so the resumed monitor never meets a stale last ping. A held bot that is alerted down (deaf) is resumed without a ping, so its monitor goes down with it. Pausing needs the ping secret, so `on_hold: pause` refuses to save without one; `on_hold: silent` (for a ping service without pause) just stops pinging, and the monitor goes down. lasso remembers what it set only in memory: after a restart it re-asserts it once (Monicron's pause and resume are idempotent). A failed pause is retried on the next tick with nothing pinged meanwhile; a failed resume is not, so pings go on and the error stays on the channel. Only a change in a heartbeat's success is logged or stored, so a dead monitor does not log every minute.

**Outbound.** One client (`alertHTTP`): 10s timeout, no redirects (a redirecting webhook is misconfigured, and following it would carry the body somewhere nobody chose), answers read to 4 KiB. There is deliberately no private-address refusal: channels are configured by the human behind UI_AUTH, and tailnet or loopback receivers are legitimate. **Errors are scrubbed before they are logged or stored** (`scrubAlertErr`): a `*url.Error` is reduced to its operation and cause, because a Slack, Discord, Monicron or Telegram URL is itself the credential, and any secret's value is replaced.

## Routing and delivery

The global defaults are the `alerts` settings key: `{enabled, channels, down_after, up_after, remind_every, remind_max, handle_within, link_base}`. A bot's own is `bots.alerts` (absent = inherit): `mode` `""` (inherit the default channels), `off` (none) or `custom` (`channels` instead), threshold overrides, and `health_checks`. Channel ids are checked when saved; deleting a channel removes it from the defaults and every bot's list.

A transition goes to the bot's routed channels (none when off or globally disabled) plus every enabled heartbeat bound to the bot, and to lasso's own pipeline as `bot_down` / `bot_recovered` (catalog kinds, default on, kept as Inbox notices) through `publishNotification`. Not a notification transport: an external transport that is "active" whenever a channel exists would wake the fleet watcher's poll and make `notify` report `sent` for everything.

`dispatchAlert` sends each channel in its own goroutine, with up to three attempts (immediately, after 10s, after 60s), and records each outcome in `alert_deliveries` (the newest 1000). "Send test" goes through the same `send` once, synchronously, for a saved channel or an unsaved draft (with `?id=`, the saved channel's secrets fill what the draft left blank).
