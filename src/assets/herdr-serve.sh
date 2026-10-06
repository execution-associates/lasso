#!/bin/bash
# herdr-serve: herdr.service's ExecStart, made to survive a live handoff.
#
# THE FAILURE IT EXISTS FOR (2026-09-29). `herdr update --handoff` asks the
# running server to spawn its successor, hand it every live pane, and exit 0.
# The successor is spawned inside this unit's cgroup, but systemd only knows
# the main PID. So with a bare `ExecStart=herdr server`, a clean main-PID exit
# reads as "the service stopped": systemd ran ExecStop (`herdr server stop`,
# which stops the SUCCESSOR through the socket) and SIGKILLed the cgroup. Every
# pane died. Restart=on-failure ignored the clean exit, and the next herdr was
# auto-started by lasso's terminal, inside lasso.service's cgroup.
#
# So this stays the main PID. After a clean exit it keeps waiting while a
# successor `herdr server` is alive in THIS unit's cgroup (re-scanned each
# time, so a second handoff is followed too), and exits non-zero when the last
# one dies, so Restart= brings herdr back under systemd rather than under
# whoever runs the next client.
#
# THIS COPY is the one lasso's host provisioning installs on remote hosts
# (src/hosts.go writes it to ~/.local/bin/herdr-serve and points herdr.service
# at it). titan-iac's tools/herdr-serve is the original; keep the logic in step.
# It finds herdr through HERDR_SERVE_BIN, which the unit sets, else on PATH.
set -u

herdr=${HERDR_SERVE_BIN:-$(command -v herdr || echo "$HOME/.local/bin/herdr")}
cgroup_procs=/sys/fs/cgroup$(cut -d: -f3- /proc/self/cgroup)/cgroup.procs
stopping=0
trap 'stopping=1; [ -n "${child:-}" ] && kill -TERM "$child" 2>/dev/null' TERM INT

"$herdr" server "$@" &
child=$!
# `wait` returns early when a trapped signal arrives, so wait until it is gone.
while :; do
  wait "$child"
  rc=$?
  kill -0 "$child" 2>/dev/null || break
done

[ "$stopping" = 1 ] && exit 0
[ "$rc" = 0 ] || exit "$rc"

# A successor is our herdr binary running `server` or `server --handoff-import`,
# in our cgroup. Panes live in this cgroup too, and one might run a nested or
# test herdr with `--session`, so match argv exactly rather than by substring.
successor() {
  local pid exe arg1 arg2
  while read -r pid; do
    [ "$pid" = $$ ] && continue
    exe=$(readlink "/proc/$pid/exe" 2>/dev/null) || continue
    [ "$exe" = "$(readlink -f "$herdr")" ] || continue
    { IFS= read -r -d '' _; IFS= read -r -d '' arg1; IFS= read -r -d '' arg2; } \
      <"/proc/$pid/cmdline" 2>/dev/null
    [ "${arg1:-}" = server ] || continue
    case ${arg2:-} in '' | --handoff-import) echo "$pid"; return 0 ;; esac
  done <"$cgroup_procs"
  return 1
}

if ! successor >/dev/null; then
  exit 0 # an ordinary clean stop: nothing took over
fi
echo "herdr-serve: server handed off; supervising successor pid $(successor)"
while [ "$stopping" = 0 ] && successor >/dev/null; do
  sleep 2
done
if [ "$stopping" = 1 ]; then
  # Stop the successor OURSELVES, through its socket, so it saves the session
  # the way a stopping server should. systemd signals only this process
  # (KillMode=mixed) and SIGKILLs the rest of the cgroup once we exit. A unit
  # with no ExecStop (the org guests) would otherwise lose the successor to a
  # SIGKILL with no save at all. Bounded well inside TimeoutStopSec.
  "$herdr" server stop >/dev/null 2>&1
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
    successor >/dev/null || break
    sleep 1
  done
  exit 0
fi
echo "herdr-serve: successor exited; failing so Restart= takes over" >&2
exit 1
