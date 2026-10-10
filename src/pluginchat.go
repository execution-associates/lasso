package main

// A plugin's agent grant: its pages may read named herdr agents' chat and type
// into them, through the bridge's chat.* methods (lib/plugins.ts), which land
// here as /api/plugins/<name>/chat[/send|/answer|/stop].
//
// The trust model is the bridge's. The page is an opaque origin and never
// reaches this endpoint itself: the PARENT calls it, with the plugin name taken
// from which iframe spoke, so the name in the path is trusted the way /call
// trusts it. What the grant adds is checked here, server-side, on every call:
// the plugin is enabled under the fingerprint that listed these agents, and the
// agent asked for is one of them. The pane is resolved from the agent's herdr
// NAME on the granted host — a page never names a pane, so it cannot point a
// grant for one agent at another's pane, and a relaunched agent (new pane id)
// keeps working without a re-approval.
//
// Everything after resolution is the tab chat's own code (buildChatPayload,
// chatSubmit, chatAnswer), so a plugin reads and types exactly as the human's
// chat view does, with the same draft and question-still-on-screen guards.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// pluginAgentSpec is one manifest `agents` entry. Host "" means "local".
type pluginAgentSpec struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

// pluginAgentPerm is the canonical form: host always spelled out, so `{name}`
// and `{name, host: "local"}` approve the same thing.
type pluginAgentPerm struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

// pluginAgentNameRE bounds a herdr agent name (what `herdr agent rename` sets)
// and a host (local or an ssh alias).
var pluginAgentNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const pluginAgentsMax = 8

func validatePluginAgents(agents []pluginAgentSpec) error {
	if len(agents) > pluginAgentsMax {
		return fmt.Errorf("at most %d agents", pluginAgentsMax)
	}
	for _, a := range agents {
		if !pluginAgentNameRE.MatchString(a.Name) {
			return fmt.Errorf("agents: name %q must match %s", a.Name, pluginAgentNameRE)
		}
		if a.Host != "" && !pluginAgentNameRE.MatchString(a.Host) {
			return fmt.Errorf("agents: host %q must match %s", a.Host, pluginAgentNameRE)
		}
	}
	return nil
}

// agentPerms is the manifest's agents, canonicalized: host defaulted, sorted,
// deduplicated — reordering the list is not a permission change.
func (m *pluginManifest) agentPerms() []pluginAgentPerm {
	out := []pluginAgentPerm{}
	for _, a := range m.Agents {
		h := a.Host
		if h == "" {
			h = "local"
		}
		out = append(out, pluginAgentPerm{Name: a.Name, Host: h})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Name < out[j].Name
	})
	return compactAgentPerms(out)
}

func compactAgentPerms(in []pluginAgentPerm) []pluginAgentPerm {
	out := in[:0]
	for i, a := range in {
		if i > 0 && a == in[i-1] {
			continue
		}
		out = append(out, a)
	}
	return out
}

var (
	errPluginAgentNotGranted = errors.New("not granted")
	errPluginAgentNotRunning = errors.New("no running agent by that name")
)

// pluginChatResolve maps an agent name (and optional host) to its live pane,
// after checking the enabled plugin's approved grant covers it. Seams for
// tests: pluginChatBackend and pluginAgentPane.
func (m *pluginManager) pluginChatResolve(name, agent, host string) (Backend, pane, error) {
	if host == "" {
		host = "local"
	}
	e := m.entry(name)
	if e == nil || e.Man == nil || e.state(loadPluginGrants()[name]) != pluginStateEnabled {
		// Disabled, needs_approval (the manifest changed since its grant) and
		// unknown all read the same: the grant is not in force.
		return nil, pane{}, errPluginNotFound
	}
	granted := false
	for _, a := range e.Man.agentPerms() {
		if a.Name == agent && a.Host == host {
			granted = true
			break
		}
	}
	if !granted {
		return nil, pane{}, errPluginAgentNotGranted
	}
	be, err := pluginChatBackend(host)
	if err != nil {
		return nil, pane{}, err
	}
	p, err := pluginAgentPane(be, agent)
	if err != nil {
		return nil, pane{}, err
	}
	return be, p, nil
}

var pluginChatBackend = resolveBackend

// pluginAgentPane finds the pane herdr's agent NAMED `agent` runs in. herdr's
// agent.get also accepts a pane id as its target, so the answer's own name is
// compared too: a grant names an agent, never a pane.
var pluginAgentPane = func(be Backend, agent string) (pane, error) {
	res, err := be.HerdrCall("agent.get", map[string]any{"target": agent})
	if err != nil {
		return pane{}, errPluginAgentNotRunning
	}
	var out struct {
		Agent struct {
			Name   string `json:"name"`
			PaneID string `json:"pane_id"`
		} `json:"agent"`
	}
	if json.Unmarshal(res, &out) != nil || out.Agent.Name != agent || out.Agent.PaneID == "" {
		return pane{}, errPluginAgentNotRunning
	}
	panes, err := panesRaw(be)
	if err != nil {
		return pane{}, err
	}
	for _, p := range panes {
		if p.PaneID == out.Agent.PaneID {
			return p, nil
		}
	}
	return pane{}, errPluginAgentNotRunning
}

// serveChatAPI answers /api/plugins/<name>/chat… and reports whether rest was
// one of its routes. GET reads; the rest are POST.
func (m *pluginManager) serveChatAPI(w http.ResponseWriter, r *http.Request, rest string) bool {
	name, action, ok := strings.Cut(rest, "/")
	if !ok || !pluginNameRE.MatchString(name) {
		return false
	}
	op, isChat := strings.CutPrefix(action, "chat")
	if !isChat || (op != "" && !strings.HasPrefix(op, "/")) {
		return false
	}
	op = strings.TrimPrefix(op, "/")
	if op == "" {
		if r.Method != http.MethodGet {
			http.Error(w, "GET", http.StatusMethodNotAllowed)
			return true
		}
		q := r.URL.Query()
		be, p, err := m.pluginChatResolve(name, q.Get("agent"), q.Get("host"))
		if !writePluginChatErr(w, name, q.Get("agent"), err) {
			// With the channel messages: for an assistant they are most of
			// what it is answering.
			writeChat(w, buildChatPayload(be, p, q.Get("before"), true))
		}
		return true
	}
	if op != "send" && op != "answer" && op != "stop" {
		return false
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return true
	}
	var in struct {
		Agent   string        `json:"agent"`
		Host    string        `json:"host"`
		Text    string        `json:"text"`
		Expect  string        `json:"expect"`
		Labels  []string      `json:"labels"`
		Answers []chatAskPick `json:"answers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return true
	}
	// Validate the request before touching herdr.
	switch op {
	case "send":
		if strings.TrimSpace(in.Text) == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return true
		}
	case "answer":
		if len(in.Answers) == 0 {
			http.Error(w, "answers are required", http.StatusBadRequest)
			return true
		}
		if err := checkAskAnswers(in.Answers); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
	}
	be, p, err := m.pluginChatResolve(name, in.Agent, in.Host)
	if writePluginChatErr(w, name, in.Agent, err) {
		return true
	}
	kind, status := paneAgentPresence(p)
	switch op {
	case "send":
		outcome, detail := chatSubmit(be, p.PaneID, kind, in.Text)
		writeJSON(w, map[string]any{"outcome": outcome, "detail": detail})
	case "answer":
		outcome, detail := chatAnswer(be, p.PaneID, kind, in.Expect, in.Labels, in.Answers)
		writeJSON(w, map[string]any{"outcome": outcome, "detail": detail})
	case "stop":
		// Escape interrupts a working Claude Code turn. On an idle one it would
		// throw away whatever a human has half-typed in the terminal, so it is
		// sent only while the agent is working by herdr's or the transcript's
		// account — the same two sources the chat's `running` combines.
		if status != "working" && !buildChatPayload(be, p, "", false).Running {
			writeJSON(w, map[string]any{"outcome": "refused", "detail": "the agent is not working"})
			return true
		}
		if _, err := be.HerdrCall("pane.send_keys", map[string]any{
			"pane_id": p.PaneID,
			"keys":    []string{"Escape"},
		}); err != nil {
			writeJSON(w, map[string]any{"outcome": "uncertain", "detail": "the pane stopped answering: " + err.Error()})
			return true
		}
		writeJSON(w, map[string]any{"outcome": "sent"})
	}
	return true
}

// writePluginChatErr answers a resolution failure; false when there was none.
func writePluginChatErr(w http.ResponseWriter, name, agent string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errPluginNotFound):
		http.Error(w, fmt.Sprintf("plugin %q is not enabled", name), http.StatusNotFound)
	case errors.Is(err, errPluginAgentNotGranted):
		http.Error(w, fmt.Sprintf("plugin %q was not granted agent %q on that host", name, agent), http.StatusForbidden)
	case errors.Is(err, errPluginAgentNotRunning):
		http.Error(w, fmt.Sprintf("agent %q is not running", agent), http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
	return true
}
