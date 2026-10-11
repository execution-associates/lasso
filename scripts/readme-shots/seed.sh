#!/usr/bin/env bash
# Run as ubuntu inside the readme-shots VM, after provision.sh. Builds the demo
# the screenshots show, through the same paths a user's lasso takes: three
# small repos on three hosts, real omp agents working in them (on DeepSeek),
# bots, and a plugin waiting for approval. Everything is fictional and nothing
# is mocked. Safe to run again: it skips what already exists, so a kept VM
# keeps its agents' history rather than growing a second set.
set -euo pipefail

MODEL=deepseek/deepseek-flash
# The bot is Claude Code on DeepSeek's Anthropic-compatible endpoint.
BOT_MODEL=deepseek-v4-flash
state=~/readme-shots
mkdir -p "$state"

# ---- the fleet's ssh config ---------------------------------------------------
cat >~/.ssh/config <<'EOF'
Host buildbox
  HostName buildbox
  User ci
Host gpubox
  HostName gpubox
  User ml
Host laptop
  HostName laptop
  User ubuntu
  ConnectTimeout 4
EOF
chmod 600 ~/.ssh/config
for h in buildbox gpubox; do
  ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new "$h" true
done

# on HOST SCRIPT: run SCRIPT (stdin) as that host's user, locally for "local".
on() {
  if [ "$1" = local ]; then bash -s; else ssh -o BatchMode=yes "$1" bash -s; fi
}

# ---- the agents' CLIs, on every host --------------------------------------------
# omp on DeepSeek V4.1 Flash for every role; Claude Code (the bot, and lasso's
# auto-titler when it gets to it) on DeepSeek's Anthropic endpoint. The keys
# are isb's placeholders, from the environment each service and shell gets.
for h in local buildbox gpubox; do
  on "$h" <<EOF
set -euo pipefail
git config --global user.name "Demo Dev"
git config --global user.email dev@example.com
git config --global init.defaultBranch main
mkdir -p ~/.omp/agent ~/.claude ~/projects
cat >~/.omp/agent/config.yml <<'C'
# Past omp's first-run setup wizard, which would otherwise hold every pane.
setupVersion: 2
modelRoles:
  default: $MODEL
  smol: $MODEL
  slow: $MODEL
  task: $MODEL
  plan: $MODEL
  vision: $MODEL
  advisor: $MODEL
C
# Merged, not written over: herdr's integration keeps its hooks in this file.
f=~/.claude/settings.json
[ -s "\$f" ] || echo '{}' >"\$f"
jq '.permissions.allow = ["Bash", "Read", "Glob", "Grep"] | .env += {
  "ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
  "ANTHROPIC_MODEL": "$BOT_MODEL",
  "ANTHROPIC_SMALL_FAST_MODEL": "$BOT_MODEL",
  "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
  "DISABLE_AUTOUPDATER": "1"
}' "\$f" >"\$f.new" && mv "\$f.new" "\$f"
[ -e ~/.claude.json ] || echo '{"hasCompletedOnboarding": true, "theme": "dark"}' >~/.claude.json
EOF
done

# The bots' folders are trusted up front. lasso's launch task answers Claude
# Code's trust prompt itself, but on this endpoint a long unknown-model notice
# comes first and the answer can land on "No, exit".
jq '.projects += ([ "harbormaster", "tidewatch", "inbox" ]
  | map({key: ("/home/ubuntu/bots/" + .), value: {hasTrustDialogAccepted: true}}) | from_entries)' \
  ~/.claude.json >~/.claude.json.new && mv ~/.claude.json.new ~/.claude.json

# ---- the repos -------------------------------------------------------------------
# commit MESSAGE: commit everything, each commit 9 days after the one before,
# counting down from n*9 days ago.
commit_fn='commit() { d=$(date -R -d "$((n * 9)) days ago"); n=$((n - 1)); git add -A && GIT_AUTHOR_DATE=$d GIT_COMMITTER_DATE=$d git commit -qm "$1"; }'

on local <<EOF
set -euo pipefail
$commit_fn
# A repo with a history is done; a half-made one (an interrupted run) is redone.
git -C ~/projects/tidepool rev-parse -q --verify HEAD >/dev/null 2>&1 && exit 0
rm -rf ~/projects/tidepool && mkdir -p ~/projects/tidepool && cd ~/projects/tidepool && git init -q
n=5
cat >README.md <<'R'
# tidepool

A tide chart for the Newport Beach pier: tomorrow's highs and lows at a glance,
as one static page with no build step.

## Run it

\`\`\`sh
python3 -m http.server 5173
\`\`\`

## How it fits together

\`\`\`mermaid
flowchart LR
  data[data.js<br>hourly heights] --> chart[chart.js<br>SVG curve]
  chart --> page[index.html]
  css[style.css<br>colours] --> page
\`\`\`

| File | What it does |
| --- | --- |
| \`data.js\` | 48 hourly predictions, in feet above MLLW |
| \`chart.js\` | draws the curve and marks each high and low |
| \`style.css\` | the page's colours and type |

## Roadmap

- [x] Highs and lows marked on the curve
- [ ] Dark mode
- [ ] A second station (Dana Point)
R
cat >data.js <<'R'
// Hourly tide predictions for Newport Beach pier, feet above MLLW.
const TIDES = Array.from({ length: 48 }, (_, h) =>
  +(2.7 + 2.1 * Math.sin((h - 3) * (2 * Math.PI / 12.42)) + 0.6 * Math.sin(h * (2 * Math.PI / 24.8))).toFixed(2)
);
R
commit "Hourly predictions for the pier"
cat >style.css <<'R'
body { margin: 0; font: 16px/1.5 system-ui, sans-serif; background: #f6f1e7; color: #13294b; }
header { padding: 28px 40px 0; }
h1 { margin: 0; font-size: 28px; }
.sub { color: #5b6b82; }
svg { display: block; width: calc(100% - 80px); max-width: 1100px; margin: 12px 40px; }
.legend { margin: 0 40px; color: #5b6b82; font-size: 14px; }
R
cat >index.html <<'R'
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Tidepool · Newport Beach</title>
  <link rel="stylesheet" href="style.css">
</head>
<body>
  <header>
    <h1>Tidepool</h1>
    <div class="sub">Newport Beach pier · next 48 hours</div>
  </header>
  <svg id="chart" viewBox="0 0 1100 420"></svg>
  <p class="legend">Feet above mean lower low water. Highs and lows are marked.</p>
  <script src="data.js"></script>
  <script src="chart.js"></script>
</body>
</html>
R
commit "A page for the chart"
cat >chart.js <<'R'
// Draws TIDES as a filled curve, marking each local high and low.
const svg = document.getElementById("chart");
const W = 1100, H = 420, PAD = 40;
const x = (i) => PAD + (i / (TIDES.length - 1)) * (W - 2 * PAD);
const y = (v) => H - PAD - (v / 6) * (H - 2 * PAD);

const line = TIDES.map((v, i) => \`\${i ? "L" : "M"}\${x(i)},\${y(v)}\`).join(" ");
svg.innerHTML = \`
  <path d="\${line} L\${x(TIDES.length - 1)},\${H - PAD} L\${x(0)},\${H - PAD} Z" fill="#1f4e79" opacity="0.25"/>
  <path d="\${line}" fill="none" stroke="#1f4e79" stroke-width="3"/>\`;

TIDES.forEach((v, i) => {
  const prev = TIDES[i - 1], next = TIDES[i + 1];
  if (prev === undefined || next === undefined) return;
  const high = v > prev && v >= next, low = v < prev && v <= next;
  if (!high && !low) return;
  svg.insertAdjacentHTML("beforeend",
    \`<circle cx="\${x(i)}" cy="\${y(v)}" r="5" fill="\${high ? "#e07a2f" : "#2f9e8f"}"/>
     <text x="\${x(i)}" y="\${y(v) + (high ? -12 : 22)}" text-anchor="middle" font-size="13" fill="#13294b">\${v} ft</text>\`);
});
R
commit "Draw the curve and mark highs and lows"
sed -i 's/next 48 hours/next 48 hours · updated hourly/' index.html
commit "Say how fresh the data is"
EOF

on buildbox <<EOF
set -euo pipefail
$commit_fn
# A repo with a history is done; a half-made one (an interrupted run) is redone.
git -C ~/projects/ledger-api rev-parse -q --verify HEAD >/dev/null 2>&1 && exit 0
rm -rf ~/projects/ledger-api && mkdir -p ~/projects/ledger-api && cd ~/projects/ledger-api && git init -q
n=4
cat >go.mod <<'R'
module example.com/ledger-api

go 1.22
R
cat >main.go <<'R'
// ledger-api settles the day's card transactions into ledger entries.
package main

import (
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/settle", handleSettle)
	log.Println("ledger-api on :8080")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", nil))
}
R
commit "An HTTP entry point"
cat >settle.go <<'R'
package main

import (
	"encoding/json"
	"net/http"
)

// batchSize is how many transactions one settlement pass writes at a time.
const batchSize = 500

type Txn struct {
	ID     string \`json:"id"\`
	Amount int64  \`json:"amount_cents"\`
}

// Settle groups txns into batches of batchSize and returns the batch totals.
func Settle(txns []Txn) []int64 {
	var totals []int64
	for start := 0; start < len(txns); start += batchSize {
		end := min(start+batchSize, len(txns))
		var sum int64
		for _, t := range txns[start:end] {
			sum += t.Amount
		}
		totals = append(totals, sum)
	}
	return totals
}

func handleSettle(w http.ResponseWriter, r *http.Request) {
	var txns []Txn
	if err := json.NewDecoder(r.Body).Decode(&txns); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(Settle(txns))
}
R
cat >settle_test.go <<'R'
package main

import "testing"

func TestSettleBatches(t *testing.T) {
	txns := make([]Txn, 1201)
	for i := range txns {
		txns[i].Amount = 100
	}
	got := Settle(txns)
	if len(got) != 3 || got[2] != 100*201 {
		t.Fatalf("got %v", got)
	}
}
R
commit "Settle transactions in batches"
EOF

on gpubox <<EOF
set -euo pipefail
$commit_fn
# A repo with a history is done; a half-made one (an interrupted run) is redone.
git -C ~/projects/wavecast rev-parse -q --verify HEAD >/dev/null 2>&1 && exit 0
rm -rf ~/projects/wavecast && mkdir -p ~/projects/wavecast && cd ~/projects/wavecast && git init -q
n=3
cat >README.md <<'R'
# wavecast

Swell forecasts from buoy readings: a rolling-window model over the last
week of significant wave height and dominant period.

    python3 bench.py      # time the forecaster on the bundled sample
R
python3 - <<'P'
import math, random
random.seed(7)
with open("sample.csv", "w") as f:
    f.write("hour,height_m,period_s\n")
    for h in range(24 * 120):
        f.write(f"{h},{1.4 + 0.6 * math.sin(h / 19) + random.random() * 0.3:.3f},{11 + 3 * math.sin(h / 41):.2f}\n")
P
commit "120 days of buoy readings"
cat >forecast.py <<'R'
"""Rolling-window swell forecast over buoy readings."""
import csv


def load(path="sample.csv"):
    with open(path) as f:
        return [(float(r["height_m"]), float(r["period_s"])) for r in csv.DictReader(f)]


def rolling_mean(values, window):
    out = []
    for i in range(len(values)):
        lo = max(0, i - window + 1)
        out.append(sum(values[lo:i + 1]) / (i + 1 - lo))
    return out


def energy(readings):
    # Wave energy flux is roughly proportional to H^2 * T.
    return [h * h * t for h, t in readings]


def forecast(readings, window=168):
    return rolling_mean(energy(readings), window)
R
cat >bench.py <<'R'
import time
from forecast import forecast, load

readings = load()
start = time.perf_counter()
for _ in range(5):
    forecast(readings)
print(f"5 forecasts: {time.perf_counter() - start:.2f}s")
R
commit "A rolling-window forecaster and its benchmark"
EOF

# ---- lasso ---------------------------------------------------------------------------
until curl -fsS 127.0.0.1:8090/api/hosts >/dev/null 2>&1; do sleep 1; done
# Register lasso's MCP servers with omp (and Claude Code) on every host, so the
# agents can open pages in lasso's browser and reach each other.
lasso connect >/dev/null
for h in buildbox gpubox; do
  ssh -o BatchMode=yes "$h" 'lasso connect -url http://127.0.0.1:8090' >/dev/null
done
# lasso found the ssh config above on its own; wait (two minutes at most)
# until it has probed both remote hosts and can drive them.
for _ in $(seq 1 40); do
  lasso mcp list-hosts -refresh |
    jq -e '[.hosts[] | select((.host == "buildbox" or .host == "gpubox") and .compatible)] | length == 2' >/dev/null && break
  sleep 3
done
# The first-run tour is for people, not screenshots; dark is the default theme's
# own mode, pinned so the shots don't follow the shooter's colour scheme.
lasso mcp update-settings -ui '{"onboarding_done": true, "appearance_mode": "dark"}' >/dev/null

# ---- agents ----------------------------------------------------------------------------
# agent KEY HOST TYPE REPO PROMPT: a real omp agent, created once. Its lasso id
# is kept in $state/agents/KEY, so a rerun finds it instead of making another.
mkdir -p "$state/agents"
agent() {
  local key=$1 host=$2 type=$3 repo=$4 prompt=$5 f=$state/agents/$1
  [ -s "$f" ] && return 0
  local args=(-host "$host" -type "$type" -agent omp -model "$MODEL" -prompt "$prompt")
  [ -z "$repo" ] || args+=(-repo "$repo")
  lasso mcp create-agent "${args[@]}" | jq -r '.id + " " + .host + " " + .work_dir' >"$f"
  echo "seed: agent $key: $(cut -d' ' -f1 "$f") on $host"
}
# wait_for TARGET HOST STATUS: until that agent reaches STATUS, ten minutes at
# most. A booting agent reads idle before its CLI is up, so it first waits (up
# to two minutes) for the agent to start working; one already done skips that.
wait_for() {
  if ! lasso mcp get-agent -to "$1" -host "$2" 2>/dev/null | jq -e '.agent.status == "done" or .agent.status == "blocked"' >/dev/null; then
    lasso mcp wait-agent -to "$1" -host "$2" -status working -timeout-ms 120000 >/dev/null 2>&1 || true
  fi
  lasso mcp wait-agent -to "$1" -host "$2" -status "$3" -timeout-ms 600000 >/dev/null ||
    echo "seed: $1 did not reach $3 in time" >&2
}
# wait_agent KEY STATUS: wait_for one of the agents above.
wait_agent() {
  local id host
  read -r id host _ <"$state/agents/$1"
  wait_for "$id" "$host" "$2"
}

# tidepool's change, served from its worktree, gives the Diff, Files and
# Browser cells their content; the agent opens the page in lasso's browser.
agent tide local git /home/ubuntu/projects/tidepool \
  "Add a dark-mode toggle to the tide chart: a small button in the header that switches the page and the chart to dark colours. Keep the change small, and don't open a browser to check it yourself. This worktree is already served at http://127.0.0.1:5173: when you are done, open that page with lasso's open_browser_tab tool (it is fine if it says nobody is watching yet), then finish with a three-line summary of the change."
read -r _ _ tide_dir <"$state/agents/tide"
sudo systemd-run --unit=tidepool-web --uid=ubuntu --working-directory="$tide_dir" \
  python3 -m http.server 5173 --bind 127.0.0.1 >/dev/null 2>&1 || true

# ledger-api's agent asks before it acts: the Grid's blocked card, and the
# question the Chat and phone cells answer with a tap.
agent ledger buildbox git /home/ci/projects/ledger-api \
  "The settlement batch size is hard-coded in settle.go. Before you change anything, use your ask tool to ask me how it should be configured, offering exactly three options: an environment variable, a command-line flag, or a config file. Then wait for my answer."
agent wave gpubox git /home/ml/projects/wavecast \
  "bench.py is slow. Profile forecast.py on the bundled sample, make the slowest function fast without changing its output, and report bench.py's time before and after."
agent notes local scratch "" \
  "Write release notes for tidepool's next version from the git log of ~/projects/tidepool, as RELEASE_NOTES.md in this directory. Group them under Added and Changed."

wait_agent tide idle
wait_agent ledger blocked
wait_agent notes idle

# The agent was asked to open its page; make sure the Browser cell has it.
if ! lasso mcp list-browser-tabs | jq -e '[.browsers[].tabs[]? | select(.url | contains(":5173"))] | length > 0' >/dev/null; then
  lasso mcp open-browser-tab -url http://127.0.0.1:5173/ -pane-id "$(lasso mcp get-agent -to "$(cut -d' ' -f1 "$state/agents/tide")" | jq -r .agent.root_pane)" >/dev/null
fi

# ---- bots ---------------------------------------------------------------------------------
# bot NAME START: a Claude Code bot on DeepSeek, created once; START false
# leaves it stopped. acceptEdits, not Claude Code's default auto mode: auto
# mode's classifier is billed through Anthropic, and on another endpoint it
# stops the session with a notice. The VM's Claude settings allow its tools.
bot() {
  curl -fsS 127.0.0.1:8090/api/bots | jq -e --arg n "$1" '.bots[] | select(.name == $n)' >/dev/null && return 0
  jq -n --arg n "$1" --arg m "$BOT_MODEL" --argjson s "$2" \
    '{name: $n, host: "local", model: $m, permission_mode: "acceptEdits", start: $s}' |
    curl -fsS -X POST -H 'content-type: application/json' --data-binary @- 127.0.0.1:8090/api/bots |
    jq -r '"seed: bot " + .bot.name + (if .error then " (" + .error + ")" else "" end)'
}
bot harbormaster true
bot tidewatch true
bot inbox false
# say BOT MESSAGE: one message to a running bot, as its Bots composer would
# send it, once the bot is up and idle; then wait for its answer. Sent once:
# the marker keeps a rerun from asking again.
say() {
  [ -e "$state/said-$1" ] && return 0
  # A bot whose launch failed reads stopped and is not relaunched by itself.
  if lasso mcp list-bots | jq -e --arg b "$1" '.bots[] | select(.name == $b and .state == "stopped")' >/dev/null; then
    lasso mcp start-bot -name "$1" >/dev/null
  fi
  # The bot claims its herdr name once Claude Code is up, a moment before its
  # prompt takes input: until then a prompt finds no agent of that name, or is
  # lost. So it is sent until the bot starts working on it, three minutes at most.
  local sent=0
  for _ in $(seq 1 30); do
    if lasso mcp list-bots | jq -e --arg b "$1" '.bots[] | select(.name == $b and .state == "idle")' >/dev/null &&
      herdr agent prompt "$1" "$2" >/dev/null 2>&1 &&
      herdr agent wait "$1" --until working --timeout 20000 >/dev/null 2>&1; then
      sent=1
      break
    fi
    sleep 3
  done
  [ "$sent" = 1 ] || { echo "seed: $1 never came up to take a message" >&2; return 1; }
  touch "$state/said-$1"
  herdr agent wait "$1" --until idle --until "done" --timeout 300000 >/dev/null 2>&1 || echo "seed: $1 is still answering" >&2
}
say harbormaster \
  "Tide check: ~/projects/tidepool/data.js holds 48 hourly heights starting at midnight. Which are the two highest tides in it, and at what hours? Two sentences."
say tidewatch \
  "Read ~/projects/tidepool/README.md and list what is still open on its roadmap, as a short checklist."

# ---- a plugin waiting for approval ---------------------------------------------------
# A new plugin is disabled until a human approves exactly what it asks for.
mkdir -p ~/.lasso/plugins
[ -d ~/.lasso/plugins/hello ] || cp -r /in/plugins/hello ~/.lasso/plugins/hello

date +%s >"$state/seeded"
echo "seed: done"
