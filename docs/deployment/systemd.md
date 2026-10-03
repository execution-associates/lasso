---
title: Running as a service
description: Keep lasso running with a systemd user unit, or with the built-in lasso start background mode.
order: 61
nav_title: systemd
---

lasso is a single foreground process (`lasso serve`, or a bare `lasso`). Something has to keep it running across logouts, crashes and reboots. On Linux the best supervisor is a `systemd --user` unit. Anywhere else, or for a quick setup, `lasso start` runs it in the background.

## A systemd user unit

Run lasso as your own user, not root: it drives your herdr, uses your `~/.ssh/config` to reach other hosts, and opens files as you. A user unit does exactly that.

Save this as `~/.config/systemd/user/lasso.service`:

```ini
[Unit]
Description=lasso, a web UI over herdr
After=network-online.target herdr.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%h
# A user unit's PATH is minimal. lasso runs herdr, ttyd, ssh and git by name,
# so put wherever you installed them on it.
Environment=PATH=%h/.local/bin:%h/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin
# Secrets (UI_AUTH, MCP_OAUTH) come from a file, never the command line.
EnvironmentFile=-%h/.config/lasso/env
ExecStart=%h/.local/bin/lasso serve -listen 127.0.0.1:8090
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
```

Then enable it:

```bash
systemctl --user daemon-reload
systemctl --user enable --now lasso.service
loginctl enable-linger "$USER"
```

`loginctl enable-linger` lets your user manager (and so lasso) start at boot and keep running after you log out. Without it, lasso stops when your last session ends.

Read its log with:

```bash
journalctl --user -u lasso -f
```

The startup lines say what is guarding it (`auth:`, `mcp:`, and `access:` when the Access gate is on) and where it is listening (`UI:`).

### Why `Restart=always`

lasso handles `SIGTERM` gracefully: it stops taking new work, drains in-flight requests for up to 15 seconds, stops its child processes, and **exits with status 0**. systemd counts a clean exit as success, so with `Restart=on-failure` a lasso that was told to stop by anything other than `systemctl` (a stray `kill`, a script) would simply stay down. `Restart=always` brings it back regardless. `systemctl --user stop lasso` still stops it for good, because systemd does not restart a unit it stopped itself.

The 15-second drain sits well inside systemd's default 90-second `TimeoutStopSec`, so there is no need to change it.

### Secrets go in an environment file

`UI_AUTH` and `MCP_OAUTH` are read from the environment only. lasso has no flag for them, because anything on a command line is visible to every user on the machine through `ps` and `/proc`. Put them in a file only you can read:

```bash
mkdir -p ~/.config/lasso
install -m 600 /dev/null ~/.config/lasso/env
cat >> ~/.config/lasso/env <<'EOF'
UI_AUTH=you:a-long-random-password
EOF
systemctl --user restart lasso
```

The leading `-` in `EnvironmentFile=-...` makes the file optional, so the unit still starts when you haven't created it. The other settings lasso reads from the environment (`LASSO_BROWSER`, `LASSO_REQUIRE_ACCESS_HEADER`, and so on) can live in the same file; see [Configuration](../reference/configuration.md).

### Run herdr as its own unit

lasso does not need to start herdr: it connects to herdr's socket. If herdr's server ends up started from inside lasso's terminal, it lives in lasso's cgroup, and restarting `lasso.service` (which `lasso update` does) stops the whole cgroup, taking herdr and every agent pane with it. Run herdr's server under its own unit so lasso can restart freely.

This is the shape lasso itself writes when you use **set up** on a remote host in the host menu, and it works locally too (`~/.config/systemd/user/herdr.service`):

```ini
[Unit]
Description=herdr server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%h
Environment=PATH=%h/.local/bin:%h/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin
ExecStart=%h/.local/bin/herdr server
ExecStop=%h/.local/bin/herdr server stop
KillMode=mixed
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
```

### SSH keys in an agent

lasso reaches your other hosts with `ssh`, using your normal `~/.ssh/config`. If your keys are only available through an SSH agent, a systemd unit does not inherit your login shell's `SSH_AUTH_SOCK`. Set it in the unit (`Environment=SSH_AUTH_SOCK=...`) to a socket that exists at boot, or use keys the unit can read directly. Hosts lasso can't log into show as unreachable in the host menu.

### How `lasso update` treats the unit

`lasso update` (for a release binary) looks for running processes whose executable is the lasso binary it just replaced. For each one it reads the systemd service from the process's cgroup and asks systemd for that unit's main PID:

- If the lasso process **is** the unit's main process, as in the unit above, it restarts the unit: `systemctl --user restart` for a user unit, `systemctl restart` for a system unit (as root, otherwise through `sudo -n`; if that can't run, it prints the command for you to type).
- If the unit's main process is something else, such as a wrapper script that starts lasso among other things, it leaves the unit alone, since restarting it would bounce everything the wrapper runs. Restart lasso yourself in that case.
- A user unit belonging to a different user is not touched; it prints the command to run as that user.

Run from a shell inside lasso's own Terminal tab, the restart cuts that shell off; lasso queues the restart without waiting and says so first. See [Updating](./updating.md) for the rest.

`LASSO_SYSTEMD_UNIT` matters only for a lasso built from a source checkout, where it names the unit to restart after a `git pull` (default `lasso`). Release-binary installs find their unit from the cgroup and ignore it.

## `lasso start`: the built-in background mode

Without systemd (on macOS, say, or for a quick try), lasso can daemonize itself:

```bash
lasso start                       # same flags as serve: -listen, -theme, ...
lasso status                      # lasso: running (pid 12345) → http://127.0.0.1:8090
lasso restart                     # stop if running, then start (takes flags too)
lasso stop
```

`up` and `down` are aliases for `start` and `stop`.

`lasso start` launches `lasso serve` with your flags in its own session, detached from the terminal, and inherits your shell's environment (so export `UI_AUTH` first if you use it). It keeps two files under `~/.lasso/`:

| file | contents |
| --- | --- |
| `~/.lasso/lasso.pid` | the server's PID |
| `~/.lasso/lasso.log` | its stdout and stderr, truncated on every start |

These two paths are always under `~/.lasso`, even when `LASSO_DIR` points lasso's data elsewhere.

`lasso start` waits up to five seconds for the server to log its URL and prints it. If the server exits during startup (a busy port, a refused bind), look in `~/.lasso/lasso.log`.

Nothing restarts a `lasso start` daemon if it crashes or the machine reboots. For anything long-lived on Linux, use the unit above. Don't run both: `lasso start` beside a running unit would try to bind the same port.
