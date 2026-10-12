package main

import "strings"

// LASSO_ALLOW: who, among the callers a network front door proved, may in.
//
// Comma-separated entries:
//
//	you@example.com        a person: an Access email or a tailnet login
//	tag:agents             a tagged tailnet node carrying that tag
//	service:<client id>    an Access service token
//	ui:tag:agents          a machine entry that also reaches the UI and /api
//
// Unset, the front door's own policy is the rule: on the access listener every
// identity Access admitted (people get everything, service tokens the MCP
// surfaces); on the tailnet and serve listeners only the tailnet login that
// owns this node, because "anyone on the tailnet" includes every shared-in user
// and tagged device. The local listener never consults it.
const allowEnv = "LASSO_ALLOW"

type allowList struct {
	set      bool
	people   map[string]bool
	machines map[string]bool // "tag:x", "service:id"
	ui       map[string]bool // machine entries that also reach the UI
}

func parseAllowList(entries []string) allowList {
	a := allowList{people: map[string]bool{}, machines: map[string]bool{}, ui: map[string]bool{}}
	for _, e := range entries {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		a.set = true
		ui := false
		if rest, ok := strings.CutPrefix(e, "ui:"); ok {
			e, ui = rest, true
		}
		if strings.HasPrefix(e, "tag:") || strings.HasPrefix(e, "service:") {
			a.machines[e] = true
			if ui {
				a.ui[e] = true
			}
			continue
		}
		a.people[e] = true
	}
	return a
}

// admits reports whether a proven caller may in, and whether a machine also
// reaches the UI. People always reach the UI.
func (a allowList) admits(c caller, role listenerRole, owner string) (ok, ui bool) {
	if c.Kind == "person" {
		if a.set {
			return a.people[c.Name], true
		}
		if role == roleAccess {
			return true, true
		}
		return owner != "" && c.Name == strings.ToLower(owner), true
	}
	if c.Kind != "machine" {
		return false, false
	}
	if !a.set {
		// Access admitted the service token under a Service Auth policy, which
		// is an operator's grant; a tagged tailnet node is nobody's until named.
		return role == roleAccess, false
	}
	for _, m := range c.machineNames() {
		if a.machines[m] {
			return true, a.ui[m]
		}
	}
	return false, false
}

// machineNames are the LASSO_ALLOW spellings a machine caller matches.
func (c caller) machineNames() []string {
	if len(c.Tags) > 0 {
		out := make([]string, 0, len(c.Tags))
		for _, t := range c.Tags {
			out = append(out, strings.ToLower(t))
		}
		return out
	}
	return []string{strings.ToLower(c.Name)}
}
