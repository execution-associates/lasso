#!/usr/bin/env bash
# Run ON THE MAC. Manages LassoDesktop's LaunchAgents, so the app (and the
# parity harness) start inside the Aqua session: a process started from ssh
# has no window server.
#
#   Scripts/agent.sh install      copy dist/LassoDesktop.app to ~/Applications, load the agent
#   Scripts/agent.sh relaunch     restart the app
#   Scripts/agent.sh stop         stop it (bootout)
#   Scripts/agent.sh plist LABEL RUNATLOAD KEEPALIVE LOG ARG...   print a LaunchAgent plist
set -euo pipefail
cd "$(dirname "$0")/.."

LABEL=com.orangecountyai.LassoDesktop
DOMAIN="gui/$(id -u)"
INSTALLED="$HOME/Applications/LassoDesktop.app"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG="$HOME/Library/Logs/LassoDesktop.log"

plist() {
  local label=$1 runatload=$2 keepalive=$3 log=$4
  shift 4
  local args="" a nl=$'\n'
  for a in "$@"; do args+="		<string>$a</string>$nl"; done
  local ka=""
  if [ "$keepalive" = true ]; then
    ka=$'\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>'
  fi
  local t
  t=$(cat Scripts/LaunchAgent.plist.in)
  t=${t//@LABEL@/$label}
  t=${t//@RUNATLOAD@/$runatload}
  t=${t//@LOG@/$log}
  t=${t//@ARGS@$nl/$args}
  t=${t//@KEEPALIVE@/$ka}
  printf '%s\n' "$t"
}

case "${1:-}" in
  install)
    [ -d dist/LassoDesktop.app ] || { echo "build first: Scripts/build-app.sh" >&2; exit 1; }
    mkdir -p "$HOME/Applications" "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
    rm -rf "$INSTALLED"
    ditto dist/LassoDesktop.app "$INSTALLED"
    # The lasso the app talks to is the user's choice (Settings, or dev.sh use);
    # a reinstall keeps it, and only sets one on a fresh Mac or when asked.
    if [ -n "${LASSO_BASE_URL:-}" ] || ! defaults read "$LABEL" baseURL >/dev/null 2>&1; then
      defaults write "$LABEL" baseURL "${LASSO_BASE_URL:-http://127.0.0.1:18090}"
    fi
    # What Settings offers: this Mac's tunnel ports (lasso-desktop-tunnel.service).
    defaults write "$LABEL" lassoCandidates -array http://127.0.0.1:18090 \
      $(for p in $(seq 18190 18199); do printf 'http://127.0.0.1:%s ' "$p"; done)
    plist "$LABEL" true true "$LOG" "$INSTALLED/Contents/MacOS/LassoDesktop" >"$PLIST.new"
    if ! cmp -s "$PLIST.new" "$PLIST" 2>/dev/null; then
      mv "$PLIST.new" "$PLIST"
      launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
    else
      rm -f "$PLIST.new"
    fi
    launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1 || launchctl bootstrap "$DOMAIN" "$PLIST"
    echo "installed $INSTALLED"
    ;;
  relaunch)
    launchctl kickstart -k "$DOMAIN/$LABEL"
    echo "relaunched $LABEL"
    ;;
  stop)
    launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
    ;;
  plist)
    shift
    plist "$@"
    ;;
  *)
    sed -n '2,10p' "$0"
    exit 2
    ;;
esac
