package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// startBackend is one pane's herdr for the agent-start tests: pane.list reports
// the pane with status, and agent.get carries completion_seq once done is set.
type startBackend struct {
	*memBackend
	mu     sync.Mutex
	status string
	done   bool
}

func (b *startBackend) Name() string { return "local" }

func (b *startBackend) set(status string, done bool) {
	b.mu.Lock()
	b.status, b.done = status, done
	b.mu.Unlock()
}

func (b *startBackend) HerdrCall(method string, _ any) (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch method {
	case "pane.list":
		return json.Marshal(map[string]any{"panes": []pane{
			{PaneID: "w1:p1", WorkspaceID: "w1", Agent: "claude", AgentStatus: b.status},
		}})
	case "agent.get":
		agent := map[string]any{"pane_id": "w1:p1", "agent_status": b.status, "completion_seq": nil}
		if b.done {
			agent["completion_seq"] = 7
		}
		return json.Marshal(map[string]any{"agent": agent})
	}
	return json.RawMessage(`{}`), nil
}

func startRec(id, prompt, boot string) AgentRecord {
	rec := reapRec(id, "w1:p1")
	rec.Description = prompt
	rec.BootStatus = boot
	return rec
}

func waitAgentFor(t *testing.T, to, status string, ms int) waitAgentOut {
	t.Helper()
	_, out, err := waitAgentTool(context.Background(), nil, waitAgentIn{To: to, Status: status, TimeoutMs: ms})
	if err != nil {
		t.Fatalf("wait_agent: %v", err)
	}
	return out
}

// The race: create_agent returns, the CLI comes up showing its idle screen, and a
// wait for idle matched it before the prompt's turn had even started. It must
// read booting until the boot is done and the first turn has happened.
func TestWaitAgentIdleWaitsForBootAndFirstTurn(t *testing.T) {
	openTestDB(t)
	if err := appendAgent("local", startRec("fresh", "build the thing", BootBooting)); err != nil {
		t.Fatal(err)
	}
	b := &startBackend{memBackend: newMemBackend(), status: "idle"}
	stubListBackend(t, b)

	if out := waitAgentFor(t, "fresh", "", 300); out.Matched || out.Status != statusBooting {
		t.Fatalf("mid-boot wait = %+v, want unmatched %q", out, statusBooting)
	}

	// Boot done, CLI up and idle, but the prompt's turn has not started.
	if err := updateAgentBootStatus("fresh", "local", BootReady, ""); err != nil {
		t.Fatal(err)
	}
	if out := waitAgentFor(t, "fresh", "idle", 300); out.Matched || out.Status != statusBooting {
		t.Fatalf("pre-turn wait = %+v, want unmatched %q", out, statusBooting)
	}

	// The turn runs and finishes while the wait is polling.
	go func() {
		time.Sleep(200 * time.Millisecond)
		b.set("working", false)
		time.Sleep(900 * time.Millisecond)
		b.set("idle", true)
	}()
	if out := waitAgentFor(t, "fresh", "idle", 5000); !out.Matched || out.Status != "idle" {
		t.Fatalf("post-turn wait = %+v, want matched idle", out)
	}
}

// A turn that started and finished between two polls is caught by herdr's
// completion_seq, so the wait does not sit out its timeout.
func TestWaitAgentIdleSeesTurnFinishedBetweenPolls(t *testing.T) {
	openTestDB(t)
	if err := appendAgent("local", startRec("quick", "one-liner", BootReady)); err != nil {
		t.Fatal(err)
	}
	stubListBackend(t, &startBackend{memBackend: newMemBackend(), status: "idle", done: true})
	if out := waitAgentFor(t, "quick", "", 300); !out.Matched || out.Status != "idle" {
		t.Fatalf("wait = %+v, want matched idle", out)
	}
}

// A boot that failed will never reach idle: say so at once.
func TestWaitAgentReturnsFailedBootAtOnce(t *testing.T) {
	openTestDB(t)
	if err := appendAgent("local", startRec("broken", "x", BootFailed)); err != nil {
		t.Fatal(err)
	}
	stubListBackend(t, &startBackend{memBackend: newMemBackend(), status: "idle"})
	start := time.Now()
	out := waitAgentFor(t, "broken", "", 5000)
	if out.Matched || out.Status != "failed" {
		t.Fatalf("wait = %+v, want unmatched failed", out)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("wait on a failed boot took %s, want an immediate answer", time.Since(start))
	}
}

// get_agent and list_agents report the same booting status, never idle or a
// blank, for an agent that has not started.
func TestGetAndListAgentReportBooting(t *testing.T) {
	openTestDB(t)
	resetAgentReapMiss(t)
	if err := appendAgent("local", startRec("newbie", "go", BootBooting)); err != nil {
		t.Fatal(err)
	}
	stubListBackend(t, &startBackend{memBackend: newMemBackend(), status: "idle"})
	_, got, err := getAgentTool(context.Background(), nil, getAgentIn{To: "newbie"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent.Status != statusBooting {
		t.Errorf("get_agent status = %q, want %q", got.Agent.Status, statusBooting)
	}
	_, list, err := listAgentsTool(context.Background(), nil, listAgentsIn{})
	if err != nil {
		t.Fatal(err)
	}
	if a := listAgentsIDs(list)["newbie"]; a.Status != statusBooting {
		t.Errorf("list_agents status = %q, want %q", a.Status, statusBooting)
	}
}

func TestStartGate(t *testing.T) {
	now := time.Now()
	never := func() bool { return false }
	always := func() bool { return true }
	old := now.Add(-agentBootGrace - time.Minute)
	cases := []struct {
		name    string
		rec     AgentRecord
		herdr   string
		started func() bool
		want    string
	}{
		{"booting idle", startRec("a", "p", BootBooting), "idle", always, statusBooting},
		{"booting blocked is the trust dialog", startRec("a", "p", BootBooting), "blocked", always, statusBooting},
		{"booting working is real", startRec("a", "p", BootBooting), "working", never, "working"},
		{"creating blank", startRec("a", "p", BootCreating), "", never, statusBooting},
		{"failed wins", startRec("a", "p", BootFailed), "idle", always, "failed"},
		{"ready before first turn", startRec("a", "p", BootReady), "idle", never, statusBooting},
		{"ready, CLI not up yet", startRec("a", "p", BootReady), "unknown", never, statusBooting},
		{"ready after first turn", startRec("a", "p", BootReady), "idle", always, "idle"},
		{"done is a finished turn", startRec("a", "p", BootReady), "done", never, "done"},
		{"blocked after boot is real", startRec("a", "p", BootReady), "blocked", never, "blocked"},
		{"no prompt: nothing to wait for", startRec("a", "", BootReady), "idle", never, "idle"},
		{"legacy record", startRec("a", "p", ""), "idle", always, "idle"},
	}
	for _, c := range cases {
		if got := startGate(c.rec, c.herdr, now, c.started); got != c.want {
			t.Errorf("%s: startGate = %q, want %q", c.name, got, c.want)
		}
	}
	// Bounded: a stranded boot or a prompt that never took stops reading booting.
	stranded := startRec("a", "p", BootBooting)
	stranded.CreatedAt = old
	if got := startGate(stranded, "idle", now, never); got != "idle" {
		t.Errorf("stranded boot = %q, want idle once agentBootGrace has passed", got)
	}
	unstarted := startRec("a", "p", BootReady)
	unstarted.CreatedAt = old
	if got := startGate(unstarted, "idle", now, never); got != "idle" {
		t.Errorf("never-started agent = %q, want idle once agentBootGrace has passed", got)
	}
}
