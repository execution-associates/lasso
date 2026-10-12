package main

import (
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Lasso's own host, as MCP clients see it.
//
// Inside lasso the box it runs on is "local": the key in lasso.db (agent
// records, ui_state pins, groups, OAuth reach), in /api/* and in per-tab hosts.
// To an MCP client that word means the client's OWN machine. claude.ai asked to
// "work on titan" would read `host: "local"` as somewhere else entirely, or as
// itself. So at the MCP boundary (/mcp and /herdr-mcp) lasso's host is called by
// the name /api/hosts already shows humans — localHostname(), "titan" — and
// nowhere else changes:
//
//   - outputs: every host a tool result, error or description names goes
//     through mcpHostName, so "local" reads as the hostname.
//   - inputs: every host (or machine) argument goes through mcpHostArg, which
//     maps the hostname back to "local", case-insensitively. "local" itself keeps
//     working, so existing clients and saved prompts are unaffected. callerscope's
//     hostOr/searchHost apply it for nearly every tool; the few that read a host
//     argument outside them call it directly.
//
// The collision rule: an ssh alias always keeps the meaning it has. When an
// alias in lasso's ssh config equals the hostname (ignoring case) — a loopback
// alias for another account on the same box, say — the hostname is NOT an alias
// of local: input "titan" still means the ssh alias, and output names lasso's
// host "local", the one spelling that round-trips. Aliases that merely contain
// the hostname (minime-mirror on minime) never collide and mean what they did.

// mcpLocalName is the name MCP clients use for lasso's own host, and false when
// there is none to use: the hostname could not be read (localHostname falls
// back to "local"), or an ssh alias already answers to it.
func mcpLocalName() (string, bool) {
	name := localHostname()
	if name == "" || strings.EqualFold(name, "local") {
		return "", false
	}
	for _, alias := range sshConfigHostsFn() {
		if strings.EqualFold(alias, name) {
			return "", false
		}
	}
	return name, true
}

// mcpHostArg resolves a host argument an MCP client passed to lasso's own key:
// the hostname (any case) and "local" (any case) become "local"; an ssh alias,
// the empty host and anything unknown pass through for the scope checks to
// judge. An exact ssh alias wins over both, so no alias changes meaning.
func mcpHostArg(host string) string {
	if host == "" || host == "local" {
		return host
	}
	for _, alias := range sshConfigHostsFn() {
		if alias == host {
			return host
		}
	}
	if strings.EqualFold(host, "local") {
		return "local"
	}
	if name, ok := mcpLocalName(); ok && strings.EqualFold(host, name) {
		return "local"
	}
	return host
}

// mcpHostName is the presentation half: the name an MCP client sees for a host
// key. Only lasso's own host changes; an ssh alias is already its own name.
func mcpHostName(host string) string {
	if !isLocalHost(host) {
		return host
	}
	if name, ok := mcpLocalName(); ok {
		return name
	}
	return "local"
}

// mcpLocalDesc names lasso's own host for a description: the value a host
// argument takes, and the alias that also works.
func mcpLocalDesc() string {
	if name, ok := mcpLocalName(); ok {
		return fmt.Sprintf("%q (the box lasso runs on; \"local\" also works)", name)
	}
	return `"local" (the box lasso runs on)`
}

// mcpHostArgNote is appended to every `host` argument's description.
func mcpHostArgNote() string {
	return "Lasso's own host is " + mcpLocalDesc() + "."
}

// mcpHostsInstruction is the server instructions' paragraph on hosts.
func mcpHostsInstruction() string {
	return "Hosts: lasso runs on " + mcpLocalDesc() + ". Tools that take a `host` name it that way, every other host by its ssh alias, and list_hosts shows the hosts you may address. Results name hosts the same way, so a returned host can be passed straight back."
}

// addHostTool is mcp.AddTool for a tool that takes a host: it builds the input
// schema the SDK would have inferred and appends mcpHostArgNote to every
// `host` property, nested ones included (update_settings' theme_sync), so the
// hostname reaches the argument descriptions at registration.
func addHostTool[In, Out any](s *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	schema, err := jsonschema.For[In](&jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("addHostTool: tool %q: %v", t.Name, err))
	}
	noteHostArgs(schema)
	t.InputSchema = schema
	mcp.AddTool(s, t, h)
}

func noteHostArgs(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	for name, p := range s.Properties {
		if name == "host" && p != nil {
			p.Description = strings.TrimSpace(p.Description + " " + mcpHostArgNote())
		}
		noteHostArgs(p)
	}
}
