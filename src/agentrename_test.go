package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

// agentLabelFake answers agent.list with one labelled pane and records renames.
type agentLabelFake struct {
	*memBackend
	labels  map[string]string
	renames []map[string]any
}

func (b *agentLabelFake) HerdrCall(method string, params any) (json.RawMessage, error) {
	switch method {
	case "agent.list":
		type a struct {
			PaneID string `json:"pane_id"`
			Name   string `json:"name,omitempty"`
		}
		var out struct {
			Agents []a `json:"agents"`
		}
		for p, n := range b.labels {
			out.Agents = append(out.Agents, a{p, n})
		}
		return json.Marshal(out)
	case "agent.rename":
		p := params.(map[string]any)
		b.renames = append(b.renames, p)
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(`{}`), nil
}

// A custom label is set through herdr's agent.rename, an empty name clears
// it (name: null), and a bot's pane is refused: lasso finds a bot by that name.
func TestRenameAgentLabelsClearsAndSparesBots(t *testing.T) {
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(closeTestDB)
	if err := insertBot(&botRecord{Host: "local", Name: "stub", Runtime: "claude"}); err != nil {
		t.Fatalf("insertBot: %v", err)
	}
	b := &agentLabelFake{memBackend: newMemBackend(), labels: map[string]string{"w1:p2": "stub"}}

	if code, err := renameAgent(b, "local", "w1:p1", "HN digest"); err != nil || code != http.StatusOK {
		t.Fatalf("rename = %d %v", code, err)
	}
	if code, err := renameAgent(b, "local", "w1:p1", ""); err != nil || code != http.StatusOK {
		t.Fatalf("clear = %d %v", code, err)
	}
	if len(b.renames) != 2 || b.renames[0]["name"] != "HN digest" || b.renames[1]["name"] != nil || b.renames[0]["target"] != "w1:p1" {
		t.Fatalf("renames = %v", b.renames)
	}
	if code, err := renameAgent(b, "local", "w1:p2", "other"); err == nil || code != http.StatusConflict {
		t.Fatalf("a bot's pane = %d %v, want 409", code, err)
	}
	// The same name on another host is not that bot.
	if code, err := renameAgent(b, "norm", "w1:p2", "other"); err != nil || code != http.StatusOK {
		t.Fatalf("another host's pane = %d %v", code, err)
	}
}
