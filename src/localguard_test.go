package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostGuardAllows(t *testing.T) {
	g := newHostGuard("titan", "titan.tail9dd8e.ts.net.", "Lasso.Example.com", "*.dev.example.com")
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:8090", true},
		{"[::1]:8090", true},
		{"100.86.22.100:8090", true},
		{"localhost:5173", true},
		{"app.localhost", true},
		{"titan:8090", true},
		{"titan.tail9dd8e.ts.net:8443", true},
		{"lasso.example.com", true},
		{"LASSO.EXAMPLE.COM.", true},
		{"a.dev.example.com", true},
		{"", true},
		// DNS rebinding: the attacker's own name, now resolving to loopback.
		{"evil.example:8090", false},
		{"lasso.example.com.evil.example", false},
		{"dev.example.com", false},
		{"titan.evil.example", false},
	}
	for _, c := range cases {
		if got := g.allows(c.host); got != c.want {
			t.Errorf("allows(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestDefaultHostGuardReadsEnv(t *testing.T) {
	t.Setenv(hostnamesEnv, " lasso.example.com , *.corp.example ")
	g := defaultHostGuard("127.0.0.1:8090")
	if !g.allows("lasso.example.com") || !g.allows("x.corp.example:443") {
		t.Fatalf("names from %s not allowed: %v", hostnamesEnv, g.list())
	}
	if g.allows("other.example") {
		t.Fatal("an unconfigured name was allowed")
	}
}

func guardedRecorder(t *testing.T) (http.Handler, *bool) {
	t.Helper()
	reached := new(bool)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusNoContent)
	})
	return withLocalGuard(next, newHostGuard("lasso.example.com")), reached
}

func TestLocalGuard(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		target  string
		host    string
		headers map[string]string
		status  int
	}{
		{name: "agent on loopback, no Origin", method: "POST", target: "/mcp", host: "127.0.0.1:8090", status: 204},
		{name: "CLI write, no Origin", method: "POST", target: "/api/file-write", host: "127.0.0.1:8090", status: 204},
		{name: "own tab write", method: "POST", target: "/api/file-write", host: "lasso.example.com",
			headers: map[string]string{"Origin": "https://lasso.example.com", "Sec-Fetch-Site": "same-origin"}, status: 204},
		{name: "own tab on loopback", method: "POST", target: "/api/close", host: "127.0.0.1:8090",
			headers: map[string]string{"Origin": "http://127.0.0.1:8090"}, status: 204},
		{name: "behind a proxy that rewrote Host", method: "POST", target: "/api/close", host: "127.0.0.1:8190",
			headers: map[string]string{"Origin": "https://titan.tail9dd8e.ts.net:8443", "X-Forwarded-Host": "titan.tail9dd8e.ts.net:8443"}, status: 204},
		{name: "rebinding: foreign Host", method: "GET", target: "/api/file?path=/etc/passwd", host: "evil.example:8090",
			headers: map[string]string{"Origin": "http://evil.example:8090"}, status: http.StatusMisdirectedRequest},
		{name: "rebinding: foreign Host, no Origin", method: "GET", target: "/api/version", host: "evil.example:8090", status: http.StatusMisdirectedRequest},
		{name: "cross-site form POST", method: "POST", target: "/api/file-write", host: "127.0.0.1:8090",
			headers: map[string]string{"Origin": "https://evil.example", "Content-Type": "text/plain"}, status: 403},
		{name: "cross-site POST without Origin", method: "POST", target: "/api/create-agent", host: "127.0.0.1:8090",
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, status: 403},
		{name: "same-site sibling POST without Origin", method: "POST", target: "/api/create-agent", host: "127.0.0.1:8090",
			headers: map[string]string{"Sec-Fetch-Site": "same-site"}, status: 403},
		{name: "cross-site terminal websocket", method: "GET", target: "/terminal/ws", host: "127.0.0.1:8090",
			headers: map[string]string{"Origin": "https://evil.example", "Upgrade": "websocket", "Connection": "Upgrade"}, status: 403},
		{name: "own terminal websocket", method: "GET", target: "/terminal/ws", host: "lasso.example.com",
			headers: map[string]string{"Origin": "https://lasso.example.com", "Upgrade": "websocket", "Connection": "Upgrade"}, status: 204},
		{name: "sandboxed frame (null origin) POST", method: "POST", target: "/api/plugins/x/call", host: "lasso.example.com",
			headers: map[string]string{"Origin": "null"}, status: 403},
		{name: "cross-site MCP call", method: "POST", target: "/mcp", host: "127.0.0.1:8090",
			headers: map[string]string{"Origin": "https://evil.example"}, status: 403},
		{name: "cross-site GET navigation still opens lasso", method: "GET", target: "/", host: "lasso.example.com",
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, status: 204},
		{name: "browser OAuth client redeems a code", method: "POST", target: "/oauth/token", host: "lasso.example.com",
			headers: map[string]string{"Origin": "https://client.example"}, status: 204},
		{name: "webhook from a service", method: "POST", target: "/hooks/bots/b/j", host: "lasso.example.com",
			headers: map[string]string{"Origin": "https://sender.example"}, status: 204},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, reached := guardedRecorder(t)
			r := httptest.NewRequest(c.method, c.target, strings.NewReader(""))
			r.Host = c.host
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, c.status, strings.TrimSpace(w.Body.String()))
			}
			if *reached != (c.status == 204) {
				t.Fatalf("handler reached = %v, want %v", *reached, c.status == 204)
			}
		})
	}
}
