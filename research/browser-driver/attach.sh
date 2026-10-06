#!/bin/sh
# attach.sh <label> <headless|headful> <run_pw mode>: launch lasso-style chrome, attach a Playwright-API client over CDP.
label=$1; mode=$2; pw=$3
rm -f /tmp/cdp_url
if [ "$mode" = headful ]; then X="xvfb-run -a -s '-screen 0 1920x1080x24'"; else X=""; fi
sh -c "CDP_ONLY=1 $X uv run /work/scripts/run_cdmcp.py $label-chrome $mode $EXTRA" &
bg=$!
for i in $(seq 100); do [ -f /tmp/cdp_url ] && break; sleep 0.2; done
CDP_URL=$(cat /tmp/cdp_url) uv run /work/scripts/run_pw.py $label $pw
pkill -f "lasso-profile-$label-chrome"; kill $bg 2>/dev/null; wait
