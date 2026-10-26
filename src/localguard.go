package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// The local guard: what keeps a web page from driving a lasso it can reach
// through the visitor's own browser.
//
// A lasso on loopback trusts whoever connects (agents on the same machine rely
// on that), so the browser is the attacker's way in, by two roads:
//
//   - DNS rebinding. A page on evil.example re-resolves its own name to
//     127.0.0.1 and then talks to lasso as a SAME-origin page: it can read
//     responses, add headers, open websockets. The only thing that differs from
//     a real lasso tab is the Host header, which still says evil.example. So
//     every request must name a Host lasso knows (hostGuard.allows).
//   - Cross-site requests. A page that stays on its own origin can still fire
//     a form POST or a text/plain fetch (no preflight) at
//     http://127.0.0.1:8090/api/file-write, and open a websocket to
//     /terminal/ — websockets ignore CORS. It cannot read the answer, but the
//     write or the shell already happened. So a state-changing request or a
//     websocket upgrade must come from lasso's own origin (sameOriginRequest).
//
// Non-browser callers (agents, the CLI, curl, the Mac app) send no Origin and
// no Sec-Fetch-Site, and a Host of 127.0.0.1 or a configured name, so neither
// check touches them.
//
// Known names are loopback names, IP literals (a rebinding page cannot make the
// browser send an IP as Host for its own origin), this machine's hostname, its
// tailnet names, and LASSO_HOSTNAMES — which is how a public hostname such as a
// Cloudflare one gets in.

// hostGuard is the set of Host names this lasso answers to.
type hostGuard struct {
	names    map[string]bool
	suffixes []string // from "*.example.com" entries, stored as ".example.com"
}

// hostnamesEnv lists extra names, comma-separated: exact names, or *.suffix.
const hostnamesEnv = "LASSO_HOSTNAMES"

func newHostGuard(names ...string) hostGuard {
	g := hostGuard{names: map[string]bool{}}
	for _, n := range names {
		g.add(n)
	}
	return g
}

func (g *hostGuard) add(n string) {
	n = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), "."))
	if n == "" {
		return
	}
	if strings.HasPrefix(n, "*.") {
		g.suffixes = append(g.suffixes, n[1:])
		return
	}
	g.names[n] = true
}

// hostOnly strips the port (and IPv6 brackets) from a Host header value.
func hostOnly(hostport string) string {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// allows reports whether a request naming this Host is meant for lasso.
func (g hostGuard) allows(hostport string) bool {
	h := hostOnly(hostport)
	if h == "" {
		// HTTP/1.0 without a Host: not something a browser sends.
		return true
	}
	if net.ParseIP(h) != nil {
		return true
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if g.names[h] {
		return true
	}
	for _, s := range g.suffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// list is the configured names, for the startup log and error messages.
func (g hostGuard) list() []string {
	out := make([]string, 0, len(g.names)+len(g.suffixes))
	for n := range g.names {
		out = append(out, n)
	}
	for _, s := range g.suffixes {
		out = append(out, "*"+s)
	}
	sort.Strings(out)
	return out
}

// defaultHostGuard collects the names this lasso can be reached by without any
// configuration: the listen host, this machine's hostname, its tailnet names,
// and LASSO_HOSTNAMES.
func defaultHostGuard(listen string) hostGuard {
	g := newHostGuard()
	if h, _, err := net.SplitHostPort(listen); err == nil {
		g.add(h)
	}
	if h, err := os.Hostname(); err == nil {
		g.add(h)
		short, _, _ := strings.Cut(h, ".")
		g.add(short)
		g.add(short + ".local")
	}
	for _, n := range tailnetSelfNames() {
		g.add(n)
	}
	for _, n := range strings.Split(os.Getenv(hostnamesEnv), ",") {
		g.add(n)
	}
	return g
}

// tailnetSelfNames asks tailscale for this node's MagicDNS name and host name,
// best effort: no tailscale, or one that does not answer within two seconds,
// just means no tailnet names (a tailnet IP still passes as an IP literal).
func tailnetSelfNames() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--self", "--peers=false", "--json").Output()
	if err != nil {
		return nil
	}
	var st struct {
		Self struct {
			DNSName  string
			HostName string
		}
	}
	if json.Unmarshal(out, &st) != nil {
		return nil
	}
	var names []string
	if st.Self.DNSName != "" {
		full := strings.TrimSuffix(st.Self.DNSName, ".")
		names = append(names, full)
		short, _, _ := strings.Cut(full, ".")
		names = append(names, short)
	}
	if st.Self.HostName != "" {
		names = append(names, st.Self.HostName)
	}
	return names
}

// crossSiteExempt are paths whose callers are legitimately cross-origin: OAuth
// clients in a browser register and redeem codes from their own origin (those
// endpoints answer CORS on purpose), and webhooks and the bot channel carry
// their own credential, as does a bot's Talk door.
var crossSiteExempt = []string{
	"/oauth/register",
	"/oauth/token",
	"/hooks/bots",
	"/bot-channel",
	"/talk",
}

func pathUnder(p string, prefixes []string) bool {
	for _, e := range prefixes {
		if p == e || strings.HasPrefix(p, e+"/") {
			return true
		}
	}
	return false
}

// unsafeRequest is a request that can change something: any method but the
// safe ones, or a websocket upgrade (a GET that becomes a shell).
func unsafeRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	}
	return true
}

// sameOriginRequest reports whether a browser request comes from the origin it
// is addressed to. Non-browser callers (no Origin, no Sec-Fetch-Site) pass.
// "null" (a sandboxed frame, a file:// page) never does: lasso's plugin frames
// talk to lasso only through postMessage.
func sameOriginRequest(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
			return true
		}
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	oh := normHostPort(u.Scheme, u.Host)
	if strings.EqualFold(oh, normHostPort(u.Scheme, r.Host)) {
		return true
	}
	// A proxy in front (tailscale serve, Vite in dev) may hand lasso its own
	// Host. A cross-site page cannot set this header: not on a simple request
	// (that needs a preflight lasso never answers) and not on a websocket.
	if xf := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); xf != "" {
		return strings.EqualFold(oh, normHostPort(u.Scheme, xf))
	}
	return false
}

// withLocalGuard refuses requests for a Host lasso does not answer to, and
// state-changing browser requests from another origin.
func withLocalGuard(next http.Handler, g hostGuard) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// On the access listener the verified Access assertion already proves
		// the request came through Cloudflare for this application, which a
		// rebinding page cannot fake; the public hostname needs no listing.
		if roleOf(r) != roleAccess && !g.allows(r.Host) {
			// 421: the request reached a server that is not configured for
			// that name. Say how to add it, since a reverse proxy or tunnel
			// lasso was not told about is the honest way to get here.
			http.Error(w, "misdirected request: lasso does not answer to the host "+hostOnly(r.Host)+
				"; if this is a hostname you publish lasso under, add it to "+hostnamesEnv, http.StatusMisdirectedRequest)
			logGuardRefusal("host", r)
			return
		}
		if unsafeRequest(r) && !pathUnder(r.URL.Path, crossSiteExempt) && !sameOriginRequest(r) {
			http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
			logGuardRefusal("cross-site", r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logGuardRefusal logs a refusal without the query string or any header but
// the two the decision read, so a log line can't carry a credential.
func logGuardRefusal(kind string, r *http.Request) {
	log.Printf("guard:    refused %s %s %s (host=%q origin=%q)", kind, r.Method, r.URL.Path, hostOnly(r.Host), r.Header.Get("Origin"))
}
