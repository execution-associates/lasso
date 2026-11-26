package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// cliAskHook is `lasso ask-hook`: the command the lasso-asks Claude Code
// plugin's hooks run (claude-plugin/lasso-asks). It forwards the hook's JSON
// on stdin to the local lasso's /api/asks/agent with this pane's id, so a
// question the agent asks in its terminal is also in the operator's Inbox
// (clientasks_agent.go). It prints nothing and always exits 0: a hook's
// output can reach the model, and a failed hook would get in the agent's way.
// LASSO_LISTEN and UI_AUTH are read as closeme reads them; LASSO_ASK_HOOK_DEBUG
// writes what happened to stderr.
func cliAskHook() {
	debug := func(format string, a ...any) {
		if os.Getenv("LASSO_ASK_HOOK_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "lasso ask-hook: "+format+"\n", a...)
		}
	}
	body, err := io.ReadAll(io.LimitReader(os.Stdin, 256<<10))
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		debug("no hook input on stdin")
		return
	}
	addr := defaultListenAddr
	if env := os.Getenv("LASSO_LISTEN"); env != "" {
		addr = env
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/api/asks/agent?pane_id="+url.QueryEscape(os.Getenv("HERDR_PANE_ID")), bytes.NewReader(body))
	if err != nil {
		debug("%v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if user, pass, ok := parseAuth(os.Getenv("UI_AUTH")); ok {
		req.SetBasicAuth(user, pass)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		debug("%v", err)
		return
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	debug("%s %s", resp.Status, bytes.TrimSpace(out))
}
