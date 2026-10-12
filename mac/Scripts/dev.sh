#!/usr/bin/env bash
# Run ON TITAN, from anywhere in the repo. Drives Lasso, the Mac app, on the Mac:
#
#   mac/Scripts/dev.sh doctor          toolchain, display, tunnel, signing identity, agent state
#   mac/Scripts/dev.sh sync            rsync mac/ to the Mac
#   mac/Scripts/dev.sh build [config]  sync, then Scripts/build-app.sh on the Mac
#   mac/Scripts/dev.sh run             sync, build, install, relaunch
#   mac/Scripts/dev.sh relaunch        restart the installed app
#   mac/Scripts/dev.sh test            sync, then swift test on the Mac
#   mac/Scripts/dev.sh parity [states] sync, build, run the harness, copy results to mac/dist/parity
#   mac/Scripts/dev.sh smoke [seconds] read-only GETs (and the event stream) against the live lasso
#                                      (LASSO_BASE_URL=http://127.0.0.1:18190 for the dev lasso)
#   mac/Scripts/dev.sh use dev|prod|PORT  point the app at a lasso on titan and relaunch it:
#                                      dev = the `mise run dev` lasso (this worktree's, else the first),
#                                      prod = 8090, PORT = any of 8090 or 8190-8199
#   mac/Scripts/dev.sh logs            tail the app's log on the Mac
#   mac/Scripts/dev.sh unlock          unlock the signing keychain (password from `secret`)
#
# LASSO_MAC picks the Mac (default minime). LASSO_MAC_DIR picks the folder there,
# relative to its home (default src/lasso-desktop/mac): sync mirrors mac/ into it
# with --delete, so two worktrees building at once each need their own.
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd)"
MAC="${LASSO_MAC:-minime}"
REMOTE="${LASSO_MAC_DIR:-src/lasso-desktop/mac}"
KC='~/Library/Keychains/lasso-desktop-signing.keychain-db'

on_mac() { ssh "$MAC" "cd $REMOTE && $*"; }

sync() {
  ssh "$MAC" "mkdir -p $REMOTE"
  rsync -a --delete --exclude .build --exclude dist "$HERE/" "$MAC:$REMOTE/"
}

unlock() {
  secret LASSO_DESKTOP_SIGNING_KEYCHAIN | ssh "$MAC" "IFS= read -r pw; security unlock-keychain -p \"\$pw\" $KC"
}

# Build on the Mac; a locked signing keychain (a logout or reboot locks it)
# is unlocked from titan's secret store and the build tried once more.
build_app() {
  local out rc=0
  out=$(on_mac "Scripts/build-app.sh ${1:-release}" 2>&1) || rc=$?
  if grep -q "signing failed" <<<"$out"; then
    unlock && on_mac "Scripts/build-app.sh ${1:-release}"
    return
  fi
  printf '%s\n' "$out"
  return "$rc"
}

cmd="${1:-}"
shift || true
case "$cmd" in
  doctor)
    ssh "$MAC" bash -s <<'DOC'
echo "macOS:    $(sw_vers -productVersion)"
echo "swift:    $(swift --version 2>&1 | head -1)"
echo "display:  $(system_profiler SPDisplaysDataType 2>/dev/null | awk -F': ' '/UI Looks like/ {print $2; exit}')"
printf 'tunnel:   '; curl -s -m5 -o /dev/null -w '%{http_code} (127.0.0.1:18090)\n' http://127.0.0.1:18090/api/version || echo down
printf 'identity: '; security find-identity -p codesigning ~/Library/Keychains/lasso-desktop-signing.keychain-db 2>/dev/null | awk -F'"' '/LassoDesktop Dev/ {print $2; f=1; exit} END {if (!f) print "missing"}'
printf 'baseURL:  '; defaults read com.orangecountyai.Lasso baseURL 2>/dev/null || echo "(default http://127.0.0.1:8090)"
case "$(defaults read com.orangecountyai.Lasso baseURL 2>/dev/null)" in *:8090*|"") echo "          WARNING: 8090 on this Mac is another service's tunnel, not ours";; esac
printf 'agent:    '; launchctl print gui/$(id -u)/com.orangecountyai.Lasso 2>/dev/null | awk '/state =/ {print $3; f=1; exit} END {if (!f) print "not loaded"}'
DOC
    ;;
  sync) sync ;;
  build)
    sync
    build_app "${1:-release}"
    ;;
  run)
    sync
    build_app release >/dev/null && on_mac "Scripts/agent.sh install && Scripts/agent.sh relaunch"
    ;;
  relaunch) on_mac "Scripts/agent.sh relaunch" ;;
  test)
    sync
    on_mac "Scripts/test.sh 2>&1 | grep -E \"✔|✘|error:|Test run\""
    ;;
  parity)
    sync
    rc=0
    { build_app release >/dev/null && on_mac "Scripts/parity-check.sh ${1:-all}"; } || rc=$?
    mkdir -p "$HERE/dist"
    rsync -a --delete "$MAC:$REMOTE/dist/parity/" "$HERE/dist/parity/" || true
    echo "results: $HERE/dist/parity/"
    exit "$rc"
    ;;
  smoke)
    sync
    build_app release >/dev/null && on_mac "${LASSO_BASE_URL:+LASSO_BASE_URL=$LASSO_BASE_URL} dist/Lasso.app/Contents/MacOS/Lasso --smoke ${1:-30}"
    ;;
  use)
    case "${1:-}" in
      prod) port=8090 ;;
      dev)
        # A dev lasso is `lasso ... -dev` on 127.0.0.1:8190-8199. Prefer the one
        # running from this worktree; several worktrees can each run one.
        root="$(git -C "$HERE" rev-parse --show-toplevel)"
        port=""
        while read -r addr pid; do
          p="${addr##*:}"
          [ "$p" -ge 8190 ] && [ "$p" -le 8199 ] || continue
          [ -z "$port" ] && port="$p"
          if [ "$(readlink "/proc/$pid/cwd" 2>/dev/null)" = "$root" ]; then port="$p"; break; fi
        done < <(ss -Hltnp 2>/dev/null | awk '/"lasso"/ { match($0, /pid=[0-9]+/); print $4, substr($0, RSTART + 4, RLENGTH - 4) }')
        [ -n "$port" ] || { echo "no dev lasso on 127.0.0.1:8190-8199; start one with: mise run dev" >&2; exit 1; }
        ;;
      [0-9]*) port="$1" ;;
      *) echo "usage: dev.sh use dev|prod|PORT" >&2; exit 2 ;;
    esac
    case "$port" in 8090) mac=18090 ;; 819[0-9]) mac=$((port + 10000)) ;; *) echo "port $port is not on the tunnel (8090, 8190-8199)" >&2; exit 2 ;; esac
    ssh "$MAC" "curl -s -m5 -o /dev/null -w '%{http_code}' http://127.0.0.1:$mac/api/version" | grep -q 200 \
      || { echo "lasso on titan:$port is not answering at minime:$mac (is lasso-desktop-tunnel up, with the 8190-8199 forwards?)" >&2; exit 1; }
    ssh "$MAC" "defaults write com.orangecountyai.Lasso baseURL http://127.0.0.1:$mac"
    on_mac "Scripts/agent.sh relaunch"
    echo "the Mac app now talks to titan:$port (minime:$mac)"
    ;;
  logs) ssh "$MAC" "tail -n ${1:-100} ~/Library/Logs/Lasso.log" ;;
  unlock) unlock ;;
  *)
    sed -n '2,20p' "$0"
    exit 2
    ;;
esac
