# Agent messaging and the reply inbox

`agentmsg.go`, `replyinbox.go`. The tools are `send_agent`, `read_agent`, `wait_agent`, `get_replies` and `reply_message`.

## Who it is for

The primary caller has no herdr and no shell: a person talking to Claude on the iOS app, with lasso connected to claude.ai as a custom connector. That caller can reach lasso's `/mcp` and nothing else. It must be able to find an agent on any host lasso drives, give it work, and hear back, without ever touching a terminal.

Agents in herdr use the same tools to reach agents on other machines. When both ends are Claude Code sessions that Claude Code's own inter-agent messaging can reach, that is the better path, and every tool description says so.

## Sending

`send_agent` resolves its target exactly like `get_agent` (lasso id, then pane id, then a case-insensitive name; ambiguous names are refused) on a host the caller may address. It refuses a pane whose composer holds a draft (a human typing) and an agent that is `blocked` (a permission or plan question), before anything is typed.

**Delivery is a paste followed by a typed line.** The message body (a header naming the sender, the text, and the reply instructions) goes in as a bracketed paste; then herdr's `agent.prompt` submits one short line asking the agent to handle the relayed message. This split is load-bearing. Claude Code wraps a long paste in `<pasted_content>`, and a model treats instructions inside pasted content as data unless its user's own words ask it to act. Sent as a single paste, the test agent read the message, answered on screen, and declined to pipe anything to an address it could not vouch for. The typed line is that request, made by the operator who called `send_agent`, and it says that lasso relayed it. Short pastes are inlined by Claude Code, so the split costs nothing for them.

Anyone who can call `send_agent` can instruct the agent. That is no wider than before: typing into a pane was always full control of it, and `/mcp` is gated the same way it always was (see CLAUDE.md, `MCP_OAUTH`).

## Replies

The receiving agent may have no MCP connection and no route to lasso (a sandbox, a box off the tailnet). It needs only `tailcat`:

```
{ echo 'lasso-reply <token>'; cat /tmp/reply-<id>.md; } | tailcat <addr> <port>
```

Lasso runs one `tailcat serve <port>` child that forwards that tailcat port to a loopback listener in lasso. tailcat punches through NAT over DERP, so this works wherever the agent can reach the internet.

- **Only one port is served.** `tailcat serve` proxies the ports it is given to the same port on localhost; `all` would hand every loopback service on the box to whoever holds the address.
- **The address is stable.** The key lives in `<lassoDir>/tailcat/` (its own `XDG_CONFIG_HOME`, not the user's tailcat config) and is generated with `--fixed-region`, because an auto-selected region changes the address. The loopback port is remembered in `settings` (`reply_inbox_port:inbox-<listen port>`) and re-bound when free. Together they keep the reply command in a pending message valid across restarts and self-updates.
- **Per listen port, not per lasso dir.** A dev lasso and the production one share `~/.lasso` and `lasso.db`; keying the identity on the listen port keeps two processes from serving one address.
- **Started lazily**, by the first `send_agent` that wants a reply, and at boot when a message from the last 7 days could still be answered. A dead child is restarted on the next send; a failed start is not retried for 30s, and the message then carries only the `reply_message` route.
- **`tailcat` lookup**: `LASSO_TAILCAT` (a path, or `off`), then `PATH`, then mise's shims, `~/.local/bin`, linuxbrew and `/opt/mise/shims`. `lasso.service`'s `PATH` has no linuxbrew.

**The address is a capability, the token is the authority.** Anyone holding the address can connect, so a connection proves nothing. The first line must be `lasso-reply <token>`, where the token is 128 random bits shown only inside the message the agent received and stored as a SHA-256 hash in `agent_outbox`. Anything else is answered with a one-line refusal and dropped. Bodies are capped at 1 MiB (cut and marked `truncated`), connections at 60s. The agent sees lasso's one-line answer ("delivered" or why not) on its terminal.

`reply_message(token, text)` is the same thing over MCP, for an agent that has lasso's tools.

## Collecting

`get_replies` returns unread replies to the caller's own messages, oldest first, and marks them read. Ownership is the sender's OAuth `ClientID` (empty when `/mcp` is open, so every unauthenticated caller shares one mailbox, matching the rest of the open surface). `timeout_seconds` waits on a broadcast channel that every recorded reply closes, so a voice caller can send and then wait without polling. The channel is taken before the query, so a reply landing between the two still wakes the waiter.

Messages and replies are kept 30 days, pruned on the next send. A reply is untrusted data written by another agent; the descriptions say so.

## Reading and waiting

`read_agent` is herdr's `pane.read` (default `recent_unwrapped`, 80 lines, max 1000) plus the live status. `wait_agent` polls `paneAgentStatus`; `idle` also matches herdr's `done`. Both exist because an agent that will not, or cannot, reply still answers on its screen.
