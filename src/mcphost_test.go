package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testHostname is the machine name TestMain gives the suite (osHostname).
const testHostname = "testbox"

// stubResolveBackend keeps the tools' backend resolution on fakes: the real
// one dials the herdr socket of whoever runs the suite.
func stubResolveBackend(t *testing.T, fn func(string) (Backend, error)) {
	t.Helper()
	prev := resolveBackend
	resolveBackend = fn
	t.Cleanup(func() { resolveBackend = prev })
}

func TestMCPHostArgAcceptsTheHostname(t *testing.T) {
	stubSSHHosts(t, "gigachad", "minime-mirror")
	for in, want := range map[string]string{
		"":              "",
		"local":         "local",
		"LOCAL":         "local",
		"Local":         "local",
		testHostname:    "local",
		"TESTBOX":       "local",
		"TestBox":       "local",
		"gigachad":      "gigachad",
		"minime-mirror": "minime-mirror", // contains no hostname; means what it always did
		"nowhere":       "nowhere",       // unknown hosts pass through for the scope check to refuse
	} {
		if got := mcpHostArg(in); got != want {
			t.Errorf("mcpHostArg(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"local": testHostname, "": testHostname, "gigachad": "gigachad"} {
		if got := mcpHostName(in); got != want {
			t.Errorf("mcpHostName(%q) = %q, want %q", in, got, want)
		}
	}
}

// An ssh alias that answers to the hostname keeps its meaning, in every case,
// and lasso's own host is then shown as "local" — the one name that still
// round-trips.
func TestMCPHostnameCollidingWithAnSSHAliasStaysTheAlias(t *testing.T) {
	stubSSHHosts(t, "gigachad", "TestBox")
	for in, want := range map[string]string{
		"TestBox":    "TestBox",
		testHostname: testHostname,
		"TESTBOX":    "TESTBOX",
		"local":      "local",
		"LOCAL":      "local",
	} {
		if got := mcpHostArg(in); got != want {
			t.Errorf("collision: mcpHostArg(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mcpHostName("local"); got != "local" {
		t.Errorf("collision: mcpHostName(local) = %q, want local", got)
	}
	if d := mcpLocalDesc(); strings.Contains(d, testHostname) || !strings.Contains(d, `"local"`) {
		t.Errorf("collision: mcpLocalDesc() = %q, want it to name \"local\" only", d)
	}
	// The scope check sees the alias, not lasso's box.
	if err := anyCaller().requireHost(mcpHostArg("TestBox")); err != nil {
		t.Errorf("the colliding alias itself should stay addressable: %v", err)
	}
}

func TestMCPHostUnresolvableHostnameIsLocal(t *testing.T) {
	stubSSHHosts(t)
	prev := osHostname
	osHostname = func() (string, error) { return "", errors.New("no uname") }
	t.Cleanup(func() { osHostname = prev })
	if got := mcpHostName("local"); got != "local" {
		t.Errorf("mcpHostName(local) = %q, want local when the hostname is unknown", got)
	}
	if got := mcpHostArg("local"); got != "local" {
		t.Errorf("mcpHostArg(local) = %q, want local", got)
	}
}

func TestListHostsNamesLassosHostByItsHostname(t *testing.T) {
	// No aliases: list_hosts would otherwise probe them, and a probed host stays
	// in the process-wide store for every later test.
	stubSSHHosts(t)
	_, hosts, err := listHostsTool(context.Background(), nil, listHostsIn{})
	if err != nil {
		t.Fatal(err)
	}
	if hosts.Active != testHostname || len(hosts.Hosts) != 1 || hosts.Hosts[0].Host != testHostname || hosts.Hosts[0].Label != testHostname {
		t.Errorf("list_hosts = %+v, want active and the one row named %s", hosts, testHostname)
	}
}

// The tools take the hostname wherever they take "local", and answer with it.
func TestMCPToolsNameLassosHostByItsHostname(t *testing.T) {
	scoped := containedFleet(t) // a gigachad-only caller; the nil req below is the fleet
	stubResolveBackend(t, agentBackendResolver)
	ctx := context.Background()

	for _, host := range []string{"local", testHostname, "TESTBOX"} {
		_, who, err := whoamiTool(ctx, nil, whoamiIn{Host: host, PaneID: "wR:p1"})
		if err != nil || !who.Found || who.Agent.ID != "loc1" {
			t.Fatalf("whoami(host %q) = %+v, %v; want loc1", host, who, err)
		}
		if who.Agent.Host != testHostname {
			t.Errorf("whoami(host %q).agent.host = %q, want %s", host, who.Agent.Host, testHostname)
		}

		_, list, err := listAgentsTool(ctx, nil, listAgentsIn{Host: host})
		if err != nil {
			t.Fatalf("list_agents(host %q): %v", host, err)
		}
		if list.Host != testHostname {
			t.Errorf("list_agents(host %q).host = %q, want %s", host, list.Host, testHostname)
		}
		if len(list.Agents) != 1 || list.Agents[0].ID != "loc1" || list.Agents[0].Host != testHostname {
			t.Errorf("list_agents(host %q).agents = %+v, want loc1 on %s", host, list.Agents, testHostname)
		}
		if strings.Contains(list.HerdrError, `"local"`) {
			t.Errorf("list_agents herdr_error names local: %q", list.HerdrError)
		}

		_, got, err := getAgentTool(ctx, nil, getAgentIn{Host: host, AgentID: "loc1"})
		if err != nil || got.Agent.Host != testHostname {
			t.Errorf("get_agent(host %q) = %+v, %v; want loc1 on %s", host, got, err, testHostname)
		}
	}

	// Refusals name the host the way the caller named it.
	_, _, err := listAgentsTool(ctx, scoped, listAgentsIn{Host: "TestBox"})
	if err == nil || !strings.Contains(err.Error(), `may not address host "`+testHostname+`"`) {
		t.Errorf("contained list_agents on lasso's host: err = %v, want a refusal naming %s", err, testHostname)
	}

	// close_agent resolves the hostname to the record lasso keeps under local.
	if _, out, err := closeAgentTool(ctx, nil, closeAgentIn{Host: "TESTBOX", AgentID: "loc1", ClosePane: new(bool)}); err != nil || !out.AgentKilled {
		t.Errorf("close_agent(host TESTBOX, loc1) = %+v, %v", out, err)
	}
}

func TestHerdrMCPTakesAndNamesTheHostname(t *testing.T) {
	stubSSHHosts(t, "gigachad")
	for _, args := range []map[string]any{
		{"host": "TESTBOX"},
		{"machine": testHostname},
		{"host": testHostname, "machine": "local"}, // the same host, spelled two ways
	} {
		got, err := herdrTakeHost(args)
		if err != nil || got != "local" {
			t.Errorf("herdrTakeHost(%v) = %q, %v; want local", args, got, err)
		}
	}
	if got, err := herdrTakeHost(map[string]any{"host": "gigachad"}); err != nil || got != "gigachad" {
		t.Errorf("herdrTakeHost(gigachad) = %q, %v", got, err)
	}
	res := herdrErrorResult("pane.list", "local", errors.New("boom"))
	if text := herdrResultText(res); !strings.Contains(text, `"host":"`+testHostname+`"`) {
		t.Errorf("herdr-mcp error = %s, want host %s", text, testHostname)
	}
	if ins := herdrMCPInstructionsText(); !strings.Contains(ins, `"`+testHostname+`"`) || strings.Contains(ins, `"local" (the box`) {
		t.Errorf("herdr-mcp instructions do not name %s: %q", testHostname, ins)
	}
}

// What a client reads before calling anything names the host too: the server
// instructions, the descriptions that mention lasso's box, and every `host`
// argument (update_settings' nested theme_sync.host included). And the CLI,
// whose flags come from that schema, takes the hostname as -host.
func TestMCPDescriptionsAndCLINameTheHostname(t *testing.T) {
	stubSSHHosts(t, "gigachad")
	stubResolveBackend(t, func(host string) (Backend, error) {
		if isLocalHost(host) {
			return newCloseBackend("local", nil), nil
		}
		return nil, fmt.Errorf("host %q not available", host)
	})
	openTestDB(t)
	srv := httptest.NewServer(withMCPAuth(newMCPHandler(), "", "", false))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	sess, err := dialMCP(ctx, srv.URL, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	quoted := `"` + testHostname + `"`
	if ins := sess.InitializeResult().Instructions; !strings.Contains(ins, quoted) {
		t.Errorf("/mcp instructions do not name %s", testHostname)
	}
	tools, err := listMCPTools(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	hostArgs := 0
	for _, tool := range tools {
		if strings.Contains(tool.Description, "the local box") || strings.Contains(tool.Description, `"local" (the box`) {
			t.Errorf("%s description still calls lasso's host local: %q", tool.Name, tool.Description)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema map[string]any
		_ = json.Unmarshal(raw, &schema)
		walkHostProps(schema, func(desc string) {
			hostArgs++
			if !strings.Contains(desc, quoted) {
				t.Errorf("%s: host argument does not name %s: %q", tool.Name, testHostname, desc)
			}
		})
	}
	if hostArgs < 15 {
		t.Errorf("only %d host arguments found; the walk is missing tools", hostArgs)
	}
	for _, name := range []string{"list_hosts", "list_agents", "whoami", "close_agent"} {
		if tool := findMCPTool(tools, name); tool == nil || !strings.Contains(tool.Description, quoted) {
			t.Errorf("%s description does not name %s", name, testHostname)
		}
	}

	tool := findMCPTool(tools, "list-agents")
	if tool == nil {
		t.Fatal("list_agents not served")
	}
	full := false
	args, err := parseToolArgs(tool, []string{"-host", "TestBox"}, &full)
	if err != nil {
		t.Fatal(err)
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	out, isErr := renderToolResult(res, false, false)
	if isErr || !strings.Contains(out, `"host": `+quoted) && !strings.Contains(out, `"host":`+quoted) {
		t.Fatalf("lasso mcp list-agents -host TestBox = %s, want host %s", out, testHostname)
	}
}

// walkHostProps calls fn with the description of every `host` property in a
// JSON schema, at any depth.
func walkHostProps(schema map[string]any, fn func(string)) {
	props, _ := schema["properties"].(map[string]any)
	for name, p := range props {
		sub, _ := p.(map[string]any)
		if name == "host" {
			desc, _ := sub["description"].(string)
			fn(desc)
		}
		walkHostProps(sub, fn)
	}
}
