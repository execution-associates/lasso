#!/usr/bin/env bash
# Run ON TITAN, from anywhere in the repo. Drives LassoDesktop on the Mac:
#
#   mac/Scripts/dev.sh doctor          toolchain, display, tunnel, signing identity, agent state
#   mac/Scripts/dev.sh sync            rsync mac/ to the Mac
#   mac/Scripts/dev.sh build [config]  sync, then Scripts/build-app.sh on the Mac
#   mac/Scripts/dev.sh run             sync, build, install, relaunch
#   mac/Scripts/dev.sh relaunch        restart the installed app
#   mac/Scripts/dev.sh test            sync, then swift test on the Mac
#   mac/Scripts/dev.sh parity [states] sync, build, run the harness, copy results to mac/dist/parity
#   mac/Scripts/dev.sh logs            tail the app's log on the Mac
#   mac/Scripts/dev.sh unlock          unlock the signing keychain (password from `secret`)
#
# LASSO_MAC picks the Mac (default minime).
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd)"
MAC="${LASSO_MAC:-minime}"
REMOTE="src/lasso-desktop/mac"
KC='~/Library/Keychains/lasso-desktop-signing.keychain-db'

on_mac() { ssh "$MAC" "cd $REMOTE && $*"; }

sync() {
  ssh "$MAC" "mkdir -p $REMOTE"
  rsync -a --delete --exclude .build --exclude dist "$HERE/" "$MAC:$REMOTE/"
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
printf 'baseURL:  '; defaults read com.orangecountyai.LassoDesktop baseURL 2>/dev/null || echo "(default http://127.0.0.1:8090)"
case "$(defaults read com.orangecountyai.LassoDesktop baseURL 2>/dev/null)" in *:8090*|"") echo "          WARNING: 8090 on this Mac is another service's tunnel, not ours";; esac
printf 'agent:    '; launchctl print gui/$(id -u)/com.orangecountyai.LassoDesktop 2>/dev/null | awk '/state =/ {print $3; f=1; exit} END {if (!f) print "not loaded"}'
DOC
    ;;
  sync) sync ;;
  build)
    sync
    on_mac "Scripts/build-app.sh ${1:-release}"
    ;;
  run)
    sync
    on_mac "Scripts/build-app.sh release && Scripts/agent.sh install && Scripts/agent.sh relaunch"
    ;;
  relaunch) on_mac "Scripts/agent.sh relaunch" ;;
  test)
    sync
    on_mac "Scripts/test.sh 2>&1 | grep -E \"✔|✘|error:|Test run\""
    ;;
  parity)
    sync
    rc=0
    on_mac "Scripts/build-app.sh release >/dev/null && Scripts/parity-check.sh ${1:-all}" || rc=$?
    mkdir -p "$HERE/dist"
    rsync -a --delete "$MAC:$REMOTE/dist/parity/" "$HERE/dist/parity/" || true
    echo "results: $HERE/dist/parity/"
    exit "$rc"
    ;;
  logs) ssh "$MAC" "tail -n ${1:-100} ~/Library/Logs/LassoDesktop.log" ;;
  unlock)
    secret LASSO_DESKTOP_SIGNING_KEYCHAIN | ssh "$MAC" "IFS= read -r pw; security unlock-keychain -p \"\$pw\" $KC"
    ;;
  *)
    sed -n '2,15p' "$0"
    exit 2
    ;;
esac
