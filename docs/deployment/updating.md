---
title: Updating
description: Update lasso with lasso update, what it restarts, and how to update herdr on lasso's machine and on your other hosts.
order: 64
---

lasso and herdr are updated separately. lasso is one binary on one machine and updates itself with `lasso update`. herdr runs on every host lasso drives, and the host menu can update it on remote hosts for you.

## Checking for a new lasso

```bash
lasso version      # e.g. 4.4.5 (1a2b3c4d5e6f)
lasso doctor       # ... ⚠ latest release   4.5.0 available — run `lasso update`
```

`lasso doctor` compares your version with the latest GitHub release. The host menu (the server icon in the footer) also shows the running lasso version on its last row.

## `lasso update`

```bash
lasso update                # update and restart whatever is running it
lasso update --no-restart   # swap the binary, restart nothing
```

`lasso update` works out how lasso was installed and does the matching thing.

### Release binary (the usual case)

This covers the curl installer and a mise `ubi:` install. `lasso update`:

1. Asks GitHub for the latest release of `execution-associates/lasso`. If it is not newer than the running version, it says *"already up to date"* (see below) and stops.
2. Downloads the asset for your platform (`lasso-linux-amd64`, `lasso-darwin-arm64`, and so on).
3. Verifies it against the release's `checksums.txt`. A missing entry or a mismatched SHA-256 aborts the update and leaves your binary alone.
4. Replaces the binary atomically: it writes a temporary file in the same directory and renames it over the old one. The directory must be writable by you, so a binary in a root-owned directory such as `/usr/local/bin` fails with a permission error. Running servers keep the old binary until they restart.
5. Restarts the server onto the new binary, unless you passed `--no-restart`:
   - If a `lasso start` background daemon is running (its `~/.lasso/lasso.pid` is live), it runs `lasso restart`. The daemon comes back with **default flags**, so if you started it with flags such as `-listen`, run `lasso restart <your flags>` yourself afterwards.
   - Otherwise, on Linux, it restarts every systemd service whose main process is this binary. A unit whose main process is a wrapper script is left alone. [systemd](./systemd.md#how-lasso-update-treats-the-unit) has the details.
   - If neither applies, it prints `run 'lasso restart' if a lasso server is running`.

When lasso is already up to date, `lasso update` still finishes a previous `--no-restart` update: it restarts any systemd-managed lasso that is still running a binary that has since been replaced.

A restart drains in-flight requests for up to 15 seconds first, so an agent being created at that moment completes.

### Source checkout under systemd

If the lasso binary sits in a git checkout (or `LASSO_SRC_DIR` names one) **and** a `systemd --user` unit named `lasso` is active (`LASSO_SYSTEMD_UNIT` overrides the name), `lasso update` instead runs:

```bash
git -C <checkout> pull --ff-only
systemctl --user restart lasso
```

This assumes the unit rebuilds lasso from the checkout when it starts. With `--no-restart` it pulls and stops there.

In this mode, the host menu also offers an **update** button beside the lasso version. It compares the running build's commit with the tip of `main` in the checkout (locally, without fetching), shows *up to date* when they match, and otherwise runs the same pull and restart in a transient systemd unit so it survives lasso restarting. Release-binary installs have no in-app button; use `lasso update`.

### Turning the in-app update off

`-disable-self-update` (or `LASSO_DISABLE_SELF_UPDATE=1`) removes the in-app update button and makes `POST /api/self-update` answer 403. Use it where an agent working through lasso's UI must not be able to rebuild and restart lasso. It does not stop someone with a shell on the machine from running `lasso update`.

## Updating herdr

lasso targets one herdr socket protocol (see [herdr version](../getting-started/index.md#herdr-version)), and remote hosts must speak the same protocol as the herdr on lasso's own machine. Keep herdr on lasso's machine and on its remote hosts at the same release.

### On lasso's machine

Run `herdr update` yourself. The **Terminal** tab in lasso's sidebar is a convenient place: it runs outside herdr, and lasso strips herdr's session variables from it, so `herdr update` (which refuses to run inside a herdr session) works there.

`lasso doctor` reports the installed herdr binary, the running server's version and protocol, and whether they match what lasso expects. A herdr update can install a new binary while leaving a compatible server running on the old one; doctor says so, and the running server picks up the new binary on its next restart (`herdr update --handoff` moves live panes across).

Upgrading herdr across a protocol change can end the running server's pane processes. Checkpoint active agents before you do it.

### On remote hosts, from the host menu

The host menu lists every host in your `~/.ssh/config` with the herdr version it runs. A remote host gets an **update** button when:

- its protocol matches but its herdr is older than the one on lasso's machine (the update keeps the protocol and is live), or
- its protocol is older than lasso's machine's, which makes it unusable until updated. The tooltip warns that this stops its running sessions.

Pressing **update** runs `herdr update --handoff` on that host over SSH. `--handoff` moves the running server onto the new binary in place. If herdr asks whether to stop the old server (it does when the protocol changed), lasso answers yes, which ends that host's running pane processes.

If the update fails because herdr can't write its own install directory (a system-wide install in `/usr/local/bin`, for example), lasso retries once with `sudo -n`. That works only with passwordless sudo; otherwise the row shows **failed** with herdr's own message in its tooltip. The whole update is limited to six minutes.

Two other states you may see on a row:

- **restart needed**: the host has a newer herdr installed than the server it is running. Another update won't help, since herdr reports it is already up to date. Restart herdr on that host to pick up the new binary. lasso won't do that for you, because a server it stops may have nothing to bring it back.
- **set up**: the host is reachable but no herdr server is running. This button installs herdr if it is missing, writes and starts a `herdr.service` systemd user unit, enables lingering, and installs herdr's agent integrations. It needs a Linux host with systemd.
