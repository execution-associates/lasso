#!/usr/bin/env bash
# Run ON THE MAC (dev.sh parity runs it over ssh). Renders the named states at
# 2x from their fixtures, measures them against Spec/tokens.json, prints the
# table, writes dist/parity/{<state>.png,report.txt,report.json} and exits
# non-zero on any failure.
#
#   Scripts/parity-check.sh [all|state,state,...]
#
# The harness is the shipped binary in --parity mode, started by a one-shot
# LaunchAgent so it has the Aqua session's window server.
set -euo pipefail
cd "$(dirname "$0")/.."

STATES="${1:-all}"
LABEL=com.orangecountyai.LassoDesktop.parity
DOMAIN="gui/$(id -u)"
OUT="$PWD/dist/parity"
PLIST="$PWD/dist/parity-agent.plist"
LOG="$PWD/dist/parity.log"
BIN="$PWD/dist/LassoDesktop.app/Contents/MacOS/LassoDesktop"

[ -x "$BIN" ] || Scripts/build-app.sh release
rm -rf "$OUT"
mkdir -p "$OUT"
: >"$LOG"
Scripts/agent.sh plist "$LABEL" true false "$LOG" "$BIN" --parity "$STATES" --out "$OUT" --spec "$PWD/Spec" >"$PLIST"
launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
launchctl bootstrap "$DOMAIN" "$PLIST"

for _ in $(seq 1 360); do
  [ -f "$OUT/report.json" ] && break
  if ! launchctl print "$DOMAIN/$LABEL" 2>/dev/null | grep -q 'state = running'; then
    sleep 1
    [ -f "$OUT/report.json" ] && break
    if ! launchctl print "$DOMAIN/$LABEL" 2>/dev/null | grep -q 'state = running'; then
      echo "parity run exited without a report:" >&2
      cat "$LOG" >&2
      launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
      exit 2
    fi
  fi
  sleep 0.5
done
launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
[ -f "$OUT/report.json" ] || { echo "parity run timed out" >&2; cat "$LOG" >&2; exit 2; }

cat "$OUT/report.txt"
exit "$(plutil -extract exit_code raw "$OUT/report.json")"
