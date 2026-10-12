package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// serveAgentRename sets or clears an agent's custom label (herdr's
// agent.rename, https://herdr.dev/docs/agents/#custom-agent-labels): the name
// pane lists, the agents list and MCP callers show for it. An empty name
// clears it. A bot's pane is refused: lasso finds a bot by its herdr name, so
// relabelling it would lose the bot (rename bots in their settings instead).
func serveAgentRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		PaneID string `json:"pane_id"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PaneID == "" {
		http.Error(w, "pane_id required", http.StatusBadRequest)
		return
	}
	name := agentName(req.Name)
	if name == "" && strings.TrimSpace(req.Name) != "" {
		http.Error(w, "an agent name needs a letter (herdr names are lowercase letters, digits, - and _)", http.StatusBadRequest)
		return
	}
	be, err := reqBackend(r, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if code, err := renameAgent(be, requestHostKey(r), req.PaneID, name); err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}

// renameAgent labels the agent in paneID on host, or clears its label when
// name is "". The status is the HTTP one to answer with on an error.
func renameAgent(be Backend, host, paneID, name string) (int, error) {
	if current := agentLabelOf(be, paneID); current != "" {
		if bot := botNamed(host, current); bot != "" {
			return http.StatusConflict, fmt.Errorf("that pane runs the bot %s; rename it in the bot's settings", bot)
		}
	}
	params := map[string]any{"target": paneID, "name": nil}
	if name != "" {
		params["name"] = name
	}
	if _, err := be.HerdrCall("agent.rename", params); err != nil {
		return http.StatusBadGateway, err
	}
	return http.StatusOK, nil
}

// agentName turns what a human typed into a herdr agent name, which must
// start with a lowercase letter and hold only lowercase letters, digits, '-'
// and '_', 1-32 characters: "Check HN page" is "check-hn-page". "" when
// nothing usable is left.
func agentName(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
			dash = r == '-'
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	n := strings.TrimLeft(b.String(), "0123456789_-")
	if len(n) > 32 {
		n = n[:32]
	}
	return strings.TrimRight(n, "_-")
}

// agentLabelOf is the pane's custom agent label, "" when it has none.
func agentLabelOf(b Backend, paneID string) string {
	res, err := b.HerdrCall("agent.list", map[string]any{})
	if err != nil {
		return ""
	}
	var out struct {
		Agents []struct {
			PaneID string `json:"pane_id"`
			Name   string `json:"name"`
		} `json:"agents"`
	}
	if json.Unmarshal(res, &out) != nil {
		return ""
	}
	for _, a := range out.Agents {
		if a.PaneID == paneID {
			return a.Name
		}
	}
	return ""
}

// botNamed is the bot on host called name, "" when there is none.
func botNamed(host, name string) string {
	bots, err := listBots()
	if err != nil {
		return ""
	}
	for _, b := range bots {
		if b.Name == name && storedHost(b.Host) == host {
			return b.Name
		}
	}
	return ""
}

// requestHostKey is the request's host as lasso stores it ("local" for its own).
func requestHostKey(r *http.Request) string { return storedHost(requestHost(r)) }

func storedHost(h string) string {
	if isLocalHost(h) {
		return "local"
	}
	return h
}
