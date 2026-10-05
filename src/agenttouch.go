package main

import (
	"log"
	"time"
)

// The agents grid's "Recent" order is how recently the HUMAN acted on an agent
// through lasso, not when its transcript last changed: a transcript moves
// whenever the agent works, so a busy agent nobody is watching kept floating
// above the one you were just talking to.
//
// A touch is recorded only on a deliberate human action in the web UI: sending
// a message from a card or the chat view, answering a question, focusing the
// pane, and creating the agent from the New Agent flow. Agents that call lasso
// (MCP create_agent, no_focus) never record one, so another agent's activity
// cannot reorder the grid.
//
// Touches are keyed by host and herdr pane id and live in lasso.db, so the
// order survives a restart and is the same on every device. A pane id is not
// carried across a move (herdr gives the moved pane a new id), which loses the
// touch; the next interaction records it again.

const agentTouchSchema = `
CREATE TABLE IF NOT EXISTS agent_touch (
  host    TEXT NOT NULL,
  pane_id TEXT NOT NULL,
  at      INTEGER NOT NULL,  -- unix milliseconds
  PRIMARY KEY (host, pane_id)
);
`

// agentTouchKeep bounds the table: a touch older than this belongs to a pane
// long gone, or one so stale that it would sort last anyway.
const agentTouchKeep = 30 * 24 * time.Hour

// touchAgent records that the human just acted on host's pane. Best-effort: a
// failed write costs ordering, never the action it rode on.
func touchAgent(host, paneID string) {
	if db == nil || host == "" || paneID == "" {
		return
	}
	now := time.Now()
	if _, err := db.Exec(`INSERT INTO agent_touch (host, pane_id, at) VALUES (?, ?, ?)
		ON CONFLICT (host, pane_id) DO UPDATE SET at = excluded.at`,
		host, paneID, now.UnixMilli()); err != nil {
		log.Printf("agent touch %s %s: %v", host, paneID, err)
		return
	}
	_, _ = db.Exec(`DELETE FROM agent_touch WHERE at < ?`, now.Add(-agentTouchKeep).UnixMilli())
	// The grid polls the pane list through a short cache; drop it so the card
	// moves on the next poll rather than one TTL later.
	invalidatePanesCache()
}

// agentTouches is host's touches by pane id.
func agentTouches(host string) map[string]int64 {
	out := map[string]int64{}
	if db == nil {
		return out
	}
	rows, err := db.Query(`SELECT pane_id, at FROM agent_touch WHERE host = ?`, host)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var at int64
		if rows.Scan(&id, &at) == nil {
			out[id] = at
		}
	}
	return out
}
