package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// whoamiBackend stubs the two herdr RPCs whoami uses. pane.get mirrors how real
// herdr canonicalizes pane ids: it accepts both the raw $HERDR_PANE_ID form
// ("p_82") and the public form ("w<ws>-<n>") and echoes back the public id.
// pane.list backs the status fallback. failGet forces pane.get to error, to
// exercise the "herdr can't resolve, fall back to the raw id" path.
type whoamiBackend struct {
	*memBackend
	resolve map[string]string // raw pane id -> public pane id
	failGet bool
}

func (b *whoamiBackend) HerdrCall(method string, params any) (json.RawMessage, error) {
	p, _ := params.(map[string]any)
	switch method {
	case "pane.get":
		if b.failGet {
			return nil, &herdrError{Code: "internal", Message: "herdr down"}
		}
		id, _ := p["pane_id"].(string)
		pub, ok := b.resolve[id]
		if !ok {
			for _, v := range b.resolve { // public form passed directly: echo it
				if v == id {
					pub, ok = id, true
					break
				}
			}
		}
		if !ok {
			return nil, &herdrError{Code: "pane_not_found", Message: "pane not found"}
		}
		return json.RawMessage(fmt.Sprintf(`{"type":"pane_info","pane":{"pane_id":%q,"agent_status":"working"}}`, pub)), nil
	case "pane.list":
		return json.RawMessage(`{"panes":[]}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func whoamiRecs() []AgentRecord {
	return []AgentRecord{
		{ID: "other", RootPane: "w0000000000000-1", CreatedAt: time.Now()},
		{ID: "self", Title: "whoami", Type: "scratch", RootPane: "w6535ed1dd256243-1", CreatedAt: time.Now()},
	}
}

// The headline case: an agent passes its raw $HERDR_PANE_ID and whoami resolves
// it — through herdr's pane.get translation — to its own lasso record.
func TestResolveWhoamiMapsEnvPaneToAgent(t *testing.T) {
	b := &whoamiBackend{
		memBackend: newMemBackend(),
		resolve:    map[string]string{"p_82": "w6535ed1dd256243-1"},
	}
	out := resolveWhoami(b, "local", whoamiRecs(), "p_82")
	if !out.Found || out.Agent == nil {
		t.Fatalf("expected found, got %+v", out)
	}
	if out.Agent.ID != "self" {
		t.Errorf("resolved to wrong agent: %q", out.Agent.ID)
	}
	if out.Agent.Status != "working" {
		t.Errorf("status = %q, want working (carried from pane.get)", out.Agent.Status)
	}
}

// The public form ("w<ws>-<n>") is accepted too — herdr echoes it back.
func TestResolveWhoamiAcceptsPublicPaneID(t *testing.T) {
	b := &whoamiBackend{
		memBackend: newMemBackend(),
		resolve:    map[string]string{"p_82": "w6535ed1dd256243-1"},
	}
	out := resolveWhoami(b, "local", whoamiRecs(), "w6535ed1dd256243-1")
	if !out.Found || out.Agent == nil || out.Agent.ID != "self" {
		t.Fatalf("expected to resolve public pane id, got %+v", out)
	}
}

// If herdr can't canonicalize the id, whoami still matches a public id passed
// directly against root_pane (status just comes back empty).
func TestResolveWhoamiFallsBackWhenHerdrUnavailable(t *testing.T) {
	b := &whoamiBackend{memBackend: newMemBackend(), failGet: true}
	out := resolveWhoami(b, "local", whoamiRecs(), "w6535ed1dd256243-1")
	if !out.Found || out.Agent == nil || out.Agent.ID != "self" {
		t.Fatalf("expected fallback match, got %+v", out)
	}
}

// No pane_id: a structured answer that tells the caller to pass $HERDR_PANE_ID,
// not an opaque error.
func TestResolveWhoamiEmptyPaneID(t *testing.T) {
	b := &whoamiBackend{memBackend: newMemBackend()}
	out := resolveWhoami(b, "local", whoamiRecs(), "  ")
	if out.Found || out.Agent != nil {
		t.Fatalf("expected not found, got %+v", out)
	}
	if out.Detail == "" {
		t.Error("expected a detail explaining HERDR_PANE_ID is required")
	}
}

// A pane lasso doesn't manage: found:false with an explanation, no error.
func TestResolveWhoamiUnknownPane(t *testing.T) {
	b := &whoamiBackend{
		memBackend: newMemBackend(),
		resolve:    map[string]string{"p_99": "w9999999999999-1"},
	}
	out := resolveWhoami(b, "local", whoamiRecs(), "p_99")
	if out.Found || out.Agent != nil {
		t.Fatalf("expected not found, got %+v", out)
	}
	if out.Detail == "" {
		t.Error("expected a detail for an unmanaged pane")
	}
}

// ---------------------------------------------------------------------------
// whoami with no host — cross-host resolution through whoamiTool
// ---------------------------------------------------------------------------

// The field bug this guards against: the MCP server ran on one box while the
// caller's pane lived on another, and BOTH hosts had a pane "w1F:p1" mapped to
// a lasso agent. whoami defaulted host to "local" and identified the caller as
// the other host's agent — which the caller would then close. Without a host,
// a cross-host pane-id collision must be refused, naming the candidate hosts.
func TestWhoamiOmittedHostRefusesCrossHostCollision(t *testing.T) {
	openTestDB(t)
	if err := appendAgent("local", AgentRecord{ID: "djexrfh3p79z", Type: "git",
		RootPane: "w1F:p1", WorkDir: "/w/citadel", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := appendAgent("gigachad", AgentRecord{ID: "dk3n97h1oxig", Type: "git",
		RootPane: "w1F:p1", WorkDir: "/w/gigachad", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	stubCloseBackends(t, map[string]Backend{
		"local":    newCloseBackend("local", map[string]string{"w1F:p1": "w1F:p1"}),
		"gigachad": newCloseBackend("gigachad", map[string]string{"w1F:p1": "w1F:p1"}),
	})

	_, out, err := whoamiTool(context.Background(), nil, whoamiIn{PaneID: "w1F:p1"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Found || out.Agent != nil {
		t.Fatalf("expected refusal on a cross-host pane collision, got %+v", out)
	}
	for _, h := range []string{testHostname, "gigachad"} {
		if !strings.Contains(out.Detail, h) {
			t.Errorf("detail %q should name candidate host %q", out.Detail, h)
		}
	}
}

// The same collision with an explicit host resolves to THAT host's own agent.
func TestWhoamiExplicitHostResolvesCollision(t *testing.T) {
	openTestDB(t)
	if err := appendAgent("local", AgentRecord{ID: "djexrfh3p79z", Type: "git",
		RootPane: "w1F:p1", WorkDir: "/w/citadel", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := appendAgent("gigachad", AgentRecord{ID: "dk3n97h1oxig", Type: "git",
		RootPane: "w1F:p1", WorkDir: "/w/gigachad", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	stubCloseBackends(t, map[string]Backend{
		"local":    newCloseBackend("local", map[string]string{"w1F:p1": "w1F:p1"}),
		"gigachad": newCloseBackend("gigachad", map[string]string{"w1F:p1": "w1F:p1"}),
	})

	_, out, err := whoamiTool(context.Background(), nil, whoamiIn{Host: "gigachad", PaneID: "w1F:p1"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Found || out.Agent == nil {
		t.Fatalf("expected found with an explicit host, got %+v", out)
	}
	if out.Agent.ID != "dk3n97h1oxig" || out.Agent.Host != "gigachad" {
		t.Errorf("resolved to %s@%s, want dk3n97h1oxig@gigachad", out.Agent.ID, out.Agent.Host)
	}
}

// A pane id that exists on exactly one host resolves without a host hint —
// even when that host is NOT the box the MCP server runs on — and the returned
// record carries the owning host so close_agent can be pointed at it.
func TestWhoamiOmittedHostResolvesUniqueRemotePane(t *testing.T) {
	openTestDB(t)
	if err := appendAgent("local", AgentRecord{ID: "loc1", Type: "git",
		RootPane: "wA:p1", WorkDir: "/w/loc1", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := appendAgent("gigachad", AgentRecord{ID: "dk3n97h1oxig", Type: "git",
		RootPane: "w1F:p1", WorkDir: "/w/gigachad", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	stubCloseBackends(t, map[string]Backend{
		"local":    newCloseBackend("local", map[string]string{"wA:p1": "wA:p1"}),
		"gigachad": newCloseBackend("gigachad", map[string]string{"w1F:p1": "w1F:p1"}),
	})

	_, out, err := whoamiTool(context.Background(), nil, whoamiIn{PaneID: "w1F:p1"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Found || out.Agent == nil {
		t.Fatalf("expected a unique remote pane to resolve, got %+v", out)
	}
	if out.Agent.ID != "dk3n97h1oxig" || out.Agent.Host != "gigachad" {
		t.Errorf("resolved to %s@%s, want dk3n97h1oxig@gigachad", out.Agent.ID, out.Agent.Host)
	}
}

// Without a host, a raw $HERDR_PANE_ID is still canonicalized — through the
// LOCAL herdr only — so a local caller keeps resolving as before.
func TestWhoamiOmittedHostCanonicalizesLocalRawID(t *testing.T) {
	openTestDB(t)
	if err := appendAgent("local", AgentRecord{ID: "self", Type: "scratch",
		RootPane: "w55-1", WorkDir: "/w/self", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	stubCloseBackends(t, map[string]Backend{
		"local": newCloseBackend("local", map[string]string{"p_82": "w55-1"}),
	})

	_, out, err := whoamiTool(context.Background(), nil, whoamiIn{PaneID: "p_82"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Found || out.Agent == nil || out.Agent.ID != "self" {
		t.Fatalf("expected the raw id to resolve to the local agent, got %+v", out)
	}
}

// A local pane no record of ours claims falls back to peer adoption (the
// closeme topology: the agent was spawned here by another machine's lasso), so
// whoami still answers with the adopted record.
func TestWhoamiOmittedHostAdoptsPeerRecord(t *testing.T) {
	openTestDB(t) // no local records at all
	local := newCloseBackend("local", map[string]string{"p_82": "w55-1"})
	_ = local.MkdirAll("/w/peer-agent", 0o755)
	stubCloseBackends(t, map[string]Backend{"local": local})
	stubPeers(t, []string{"citadel"}, func(peer, rootPane string) ([]AgentRecord, error) {
		if peer == "citadel" && rootPane == "w55-1" {
			return []AgentRecord{{ID: "dk33", Type: "git", RootPane: "w55-1",
				WorkspaceID: "w55", WorkDir: "/w/peer-agent"}}, nil
		}
		return nil, nil
	})

	_, out, err := whoamiTool(context.Background(), nil, whoamiIn{PaneID: "p_82"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Found || out.Agent == nil || out.Agent.ID != "dk33" {
		t.Fatalf("expected the peer's record to be adopted, got %+v", out)
	}
	if out.Agent.Host != testHostname {
		t.Errorf("adopted agent host = %q, want %s, lasso's own host (the pane lives here)", out.Agent.Host, testHostname)
	}
}
