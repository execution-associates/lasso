#!/usr/bin/env bash
# Run as ubuntu inside the readme-shots VM, after seed.sh. Records the
# terminal-only cell: an omp session that uses lasso's MCP tools to look over
# the fleet and start another agent. tmux holds it at a fixed size and
# `capture-pane -e` keeps its colours; ansi2html.py renders the capture as a
# terminal window for shoot.py. Done once: a rerun keeps the capture rather
# than starting yet another agent.
set -euo pipefail

term=~/readme-shots/term
mkdir -p "$term"
backdrop=http://127.0.0.1:8090/wallpapers/execution-associates/01-golden-hour.webp

render() { uv run -q /in/readme-shots/ansi2html.py "$term/$1.ansi" "$term/$1.html" "$2" "$backdrop"; }

if [ ! -s "$term/mcp.ansi" ]; then
  s=readme-mcp
  tmux kill-session -t "$s" 2>/dev/null || true
  tmux new-session -d -s "$s" -x 118 -y 34 -c ~/projects
  # A login shell takes a few seconds to come up.
  sleep 4
  tmux send-keys -t "$s" "omp --model deepseek/deepseek-flash 'Using lasso: list the agents on every host, then start an omp scratch agent on gpubox that adds a \"Run the benchmark\" section to ~/projects/wavecast/README.md. Do not wait for it. Finish with a two-line summary of who is waiting on me.'" Enter
  # Wait for the turn to start (a spinner in the status line), then for it to
  # end: no spinner in three captures in a row. Five minutes at most.
  spin='[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]'
  started=0 quiet=0
  for _ in $(seq 1 150); do
    sleep 2
    screen=$(tmux capture-pane -t "$s" -p)
    if printf '%s' "$screen" | grep -E "$spin" >/dev/null; then
      started=1 quiet=0
    elif [ "$started" = 1 ]; then
      quiet=$((quiet + 1))
      [ "$quiet" -ge 3 ] && break
    fi
  done
  [ "$quiet" -ge 3 ] || echo "terminals: the omp session did not finish in time; capturing it as it is" >&2
  tmux capture-pane -t "$s" -e -p >"$term/mcp.ansi"
  tmux kill-session -t "$s"
fi
render mcp "~/projects — omp"
echo "terminals: captured"
