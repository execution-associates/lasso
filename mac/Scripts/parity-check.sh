#!/usr/bin/env bash
# Run ON THE MAC (dev.sh parity runs it over ssh). Renders the named states at
# 2x from their fixtures, measures them against Spec/tokens.json, prints the
# table, writes dist/parity/{<state>.png,report.txt,report.json} and exits
# non-zero on any failure.
#
#   Scripts/parity-check.sh [all|state,state,...]
#
# The harness is the shipped binary in --parity mode, started by a one-shot
# LaunchAgent so it has the Aqua session's window server. The agent's label
# carries a digest of this folder, so a run from another folder (dev.sh's
# LASSO_MAC_DIR) never boots this one's out.
set -euo pipefail
cd "$(dirname "$0")/.."

STATES="${1:-all}"
LABEL="com.orangecountyai.Lasso.parity.$(printf '%s' "$PWD" | shasum | cut -c1-8)"
DOMAIN="gui/$(id -u)"
OUT="$PWD/dist/parity"
# launchd neither loads a plist nor opens a log on an external volume
# (bootstrap: 5 Input/output error; spawn: 78 EX_CONFIG), and the checkout may
# live on one (minime's does), so both go in TMPDIR. The binary may run from
# there and the results are the app's own writes.
PLIST="${TMPDIR:-/tmp}/$LABEL.plist"
LOG="${TMPDIR:-/tmp}/$LABEL.log"
BIN="$PWD/dist/Lasso.app/Contents/MacOS/Lasso"

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
exit "$(cat "$OUT/exit_code" 2>/dev/null || echo 2)"
