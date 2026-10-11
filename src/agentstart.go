package main

import (
	"encoding/json"
	"sync"
	"time"
)

// A lasso agent is not "idle" until it has started.
//
// create_agent returns as soon as the workspace and pane exist; bootAgent then
// launches the CLI with the initial prompt on its command line. Between the two,
// herdr's status for the pane says nothing about the task: a bare shell reads
// "unknown", and the CLI's first screen reads "idle" until it picks the prompt
// up and starts working. A caller that created an agent and then waited for
// idle matched that boot-screen idle and acted on work that had not happened.
//
// So for a lasso-created record the surfaced status is "booting" until the
// agent has demonstrably begun:
//
//   - the boot itself (creating/booting) is still running: everything but
//     "working" reads booting. A "blocked" here is the trust dialog lasso is
//     about to answer, not a question for the caller.
//   - the record carries a prompt and no first turn has been seen yet: an at-rest
//     or unknown status reads booting. A first turn is seen when lasso observes
//     working, blocked or done, or when herdr's agent.get reports a
//     completion_seq (set on every working/blocked -> idle transition, so it
//     catches a turn that started and finished between two polls).
//
// Both are bounded by agentBootGrace from created_at: a boot stranded by a lasso
// restart, or an agent whose prompt never took, must not read booting forever.
// An agent created without a prompt has no first turn to wait for; once its boot
// is done herdr's status is the answer.
const statusBooting = "booting"

// agentStarted remembers, per host|id, the records whose first turn this
// process has seen. In memory on purpose: herdr's completion_seq covers a
// restarted lasso for the turn that already finished, and agentBootGrace bounds
// the rest.
var agentStarted sync.Map

// liveAgentStatus is the status every agent-facing tool reports for a lasso
// record, given herdr's live status for its pane (already through
// paneAgentPresence and the omp plan gate). b may be nil when herdr could not be
// reached; then completion_seq is simply not consulted.
func liveAgentStatus(b Backend, host string, rec AgentRecord, herdrStatus string) string {
	if rec.BootStatus == BootFailed {
		return "failed"
	}
	key := host + "|" + rec.ID
	switch herdrStatus {
	case "working":
		agentStarted.Store(key, true)
		return herdrStatus
	case "blocked", "done":
		if rec.BootStatus != BootCreating && rec.BootStatus != BootBooting {
			agentStarted.Store(key, true)
		}
	}
	return startGate(rec, herdrStatus, time.Now(), func() bool {
		if _, ok := agentStarted.Load(key); ok {
			return true
		}
		if b != nil && rec.RootPane != "" && paneCompletedTurn(b, rec.RootPane) {
			agentStarted.Store(key, true)
			return true
		}
		return false
	})
}

// startGate is liveAgentStatus's rule without the I/O, so it is testable on its
// own. started is asked only when the answer depends on it.
func startGate(rec AgentRecord, herdrStatus string, now time.Time, started func() bool) string {
	if rec.BootStatus == BootFailed {
		return "failed"
	}
	if herdrStatus == "working" || now.Sub(rec.CreatedAt) >= agentBootGrace {
		return herdrStatus
	}
	if rec.BootStatus == BootCreating || rec.BootStatus == BootBooting {
		return statusBooting
	}
	if agentPrompt(rec) == "" {
		return herdrStatus
	}
	switch herdrStatus {
	case "blocked", "done":
		return herdrStatus
	}
	if started() {
		return herdrStatus
	}
	return statusBooting
}

// paneCompletedTurn reports whether herdr says the agent in paneID is at rest
// after doing work: agent.get's completion_seq, which herdr sets on a
// working/blocked -> idle transition and clears when the agent starts again. A
// herdr without the field, or one that cannot identify the agent, says false.
func paneCompletedTurn(b Backend, paneID string) bool {
	res, err := b.HerdrCall("agent.get", map[string]any{"target": paneID})
	if err != nil {
		return false
	}
	var r struct {
		Agent struct {
			CompletionSeq *uint64 `json:"completion_seq"`
		} `json:"agent"`
	}
	return json.Unmarshal(res, &r) == nil && r.Agent.CompletionSeq != nil
}
