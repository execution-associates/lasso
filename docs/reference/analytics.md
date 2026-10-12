---
title: Usage analytics
description: Exactly what anonymous usage analytics lasso and the Mac app send, and every way to turn them off.
order: 102
nav_title: Analytics
---

lasso and Lasso, the Mac app, send a few anonymous usage events so we can see which parts get used. They are on by default, and you can turn them off when you install, during onboarding, in Settings, or from the environment. When they are off, nothing is sent at all: no event, and no "opted out" signal either.

The events go to a PostHog instance we run ourselves at `https://posthog.execution.associates`. The [privacy policy](https://www.executionassociates.com/privacy/) covers how we keep them.

## What is sent

Each install has a random id (a UUIDv4). The lasso server stores its id in `lasso.db`, and the Mac app keeps its own in its preferences. That id is the only identity attached to an event. Each event carries these properties:

| property | value |
| --- | --- |
| `app` | `server`, `web`, `phone` or `mac` |
| `app_version` | the lasso or Lasso version |
| `os` | `linux` or `darwin` |
| `arch` | `amd64`, `arm64` or `x86_64` |
| `$lib` | `lasso` |
| `$geoip_disable`, `$process_person_profile` | always `true` and `false`. These tell PostHog not to look up a location and not to build a person profile. |

These are all the events, each with every property it can carry:

| event | when | properties |
| --- | --- | --- |
| `install_active` | at most once a day per install, while the server or the Mac app is running | `hosts_count` (`1`, `2-3`, `4+`), `agents_running` and `bots_count` (each `0`, `1-5`, `6-20`, `21+`) |
| `view_opened` | a view or window is opened, at most once per view every 10 minutes | `view`: `terminal`, `chat`, `grid`, `bots`, `browser`, `files`, `scratch`, `settings`, `simple_inbox`, `simple_agents`, `inbox_card`, `agents_column`, `bots_column`, `agent_window`, `bot_window`, `terminal_window`, `browser_window` or `new_form` |
| `agent_created` | an agent is created | `harness` (`claude`, `codex`, `opencode`, `omp`, `pi`, or `other` for any other command), `kind` (`git` or `scratch`), `from` (`web`, `phone`, `mac` or `mcp`) |
| `inbox_action` | a card in an inbox is acted on | `action` (`acknowledge`, `kill`, `reply`, `answer`, `stop`, `rename`), `from` (`web`, `phone` or `mac`) |
| `feature_used` | one of a fixed list of features is used | `feature`: `shortcut_used`, `paste_upload`, `terminal_zoom`, `browser_new_tab`, `plugin_enabled`, `theme_changed`, `voice_input`, `file_saved`, `bot_created` or `agent_pinned` |
| `onboarding_completed` | the web tour or the Mac first-run panel is finished | none |

That is the whole vocabulary. Every value is one of the words above or a count bucket. Nothing you name, type, open or browse is sent: no hostnames, IP addresses, email addresses, file or repository paths, URLs, prompts, agent output, bot names, or which key or shortcut you pressed. There is no session recording, no autocapture, no heatmaps and no surveys. lasso sets no cookie for analytics.

## How it travels

- **The web and phone apps** never contact PostHog. They post their events to their own lasso at `POST /api/telemetry`, which uses the same auth as the rest of `/api`. lasso accepts a body of at most 16 KiB and 20 events, and at most 60 events a minute from each client. It drops any event or property that is not on the list above, sets `from` to the app that posted, and forwards the rest in batches to PostHog's capture API. When analytics is off for that lasso, it drops the events without forwarding them.
- **The lasso server** sends its own `install_active`, plus `agent_created` with `from: mcp` when an agent is created over MCP.
- **The Mac app** sends its events straight to PostHog with its own switch, since the lasso it talks to may be someone else's.

Sending never gets in your way. Events wait in a small queue, and when the queue is full the oldest are dropped. They go out in batches about every 30 seconds, one request at a time, and each request is cut off after 5 seconds. A failed batch is dropped, not retried. Failures are never shown and are logged only when `LASSO_TELEMETRY_DEBUG=1` is set. Quitting or stopping does not wait for a send.

## Turning it off

Any one of these turns analytics off.

| where | how |
| --- | --- |
| Installer | Answer `n` at the prompt, or install with `LASSO_TELEMETRY=0` or `--no-telemetry` for an unattended install: `curl -fsSL https://executionassociates.com/install-lasso \| LASSO_TELEMETRY=0 sh`, or `… \| sh -s -- --no-telemetry`. |
| Web tour | Untick **Anonymous usage analytics** on the tour's first or last card. |
| Web Settings | **Settings › General › Anonymous usage analytics**. The simple app has the same switch under **Settings › Privacy**. This is the setting for the whole lasso, so it covers every browser and phone using that lasso. |
| CLI | `lasso telemetry off`. `lasso telemetry on` turns it back on, and `lasso telemetry` shows the current state. |
| MCP | `update_settings` with `telemetry: false`. `get_settings` reports it under `telemetry`. |
| Environment | `DO_NOT_TRACK=1` or `LASSO_TELEMETRY=0` in lasso's environment holds it off whatever the setting says. Set it where lasso runs, which is the systemd unit when lasso runs under systemd, not your shell. |
| Mac app | Turn off **Share anonymous usage analytics** on the first-run panel, or **Anonymous usage analytics** in **Settings › Info › Privacy**. `DO_NOT_TRACK=1` or `LASSO_TELEMETRY=0` in the app's environment holds it off too. The Mac app's switch is its own, separate from the lasso it connects to. |

A build without an ingestion key sends nothing.
