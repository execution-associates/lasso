package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"strings"
)

// Who is calling, for every route.
//
// lasso runs no login of its own. Each listener has a ROLE that says which
// front door is in front of it, and the caller is whatever that front door
// proved, checked by lasso itself:
//
//	local   — 127.0.0.1 (the -listen address). Processes on this machine and
//	          whatever forwards onto it. Trusted as today: UI_AUTH, MCP_OAUTH
//	          and the Access header gate apply exactly as they always did.
//	access  — -listen-access, the target of a cloudflared ingress. Every
//	          request must carry a Cf-Access-Jwt-Assertion that verifies
//	          against the team's keys (accessjwt.go), or it is refused.
//	tailnet — -tailnet, lasso bound to this node's tailnet addresses with
//	          tailscaled's certificate. The caller is tailscaled's whois of the
//	          SOCKET PEER (tailnet.go); no forwarded header is ever read.
//	serve   — -listen-serve, the loopback target of `tailscale serve`, which
//	          strips client copies of Tailscale-User-Login and sets its own.
//	          Only meaningful because the port is dedicated to serve.
//
// A request on a network listener (access, tailnet, serve) that does not carry
// a valid proof is refused: a missing Access app or a stray proxy fails
// closed. LASSO_ALLOW (allow.go) then decides who among the proven may in.
//
// A verified edge identity satisfies UI_AUTH, MCP_OAUTH and the Access header
// gate, so moving to a network listener needs no shared password. A lasso
// bearer token still narrows a caller's MCP reach (callerscope.go).

type listenerRole string

const (
	roleLocal   listenerRole = "local"
	roleAccess  listenerRole = "access"
	roleTailnet listenerRole = "tailnet"
	roleServe   listenerRole = "serve"
)

// roleListener stamps its role on every connection it accepts; the server's
// ConnContext reads it back (connRole) into the request context.
type roleListener struct {
	net.Listener
	role listenerRole
}

func (l roleListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return roleConn{Conn: c, role: l.role}, nil
}

type roleConn struct {
	net.Conn
	role listenerRole
}

// connRole finds the role of a connection, looking through TLS.
func connRole(c net.Conn) listenerRole {
	for {
		switch v := c.(type) {
		case roleConn:
			return v.role
		case *tls.Conn:
			c = v.NetConn()
		default:
			return roleLocal
		}
	}
}

type ctxKey int

const (
	ctxRoleKey ctxKey = iota
	ctxCallerKey
)

// withConnRole is the server's ConnContext.
func withConnRole(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, ctxRoleKey, connRole(c))
}

func roleOf(r *http.Request) listenerRole {
	if v, ok := r.Context().Value(ctxRoleKey).(listenerRole); ok {
		return v
	}
	return roleLocal
}

// caller is who a front door proved is calling.
type caller struct {
	Kind   string // "person", "machine" or "local"
	Name   string // email or tailnet login; "service:<id>"; "tag:<a>,tag:<b>"; "" for local
	Source string // the role that proved it
	Tags   []string
	UI     bool // a machine LASSO_ALLOW lets into the UI as well
}

func callerOf(r *http.Request) (caller, bool) {
	c, ok := r.Context().Value(ctxCallerKey).(caller)
	return c, ok
}

// edgeVerified reports whether a network front door proved who is calling. It
// is what lets a request past UI_AUTH, MCP_OAUTH and the Access header gate
// without their own credentials.
func edgeVerified(r *http.Request) bool {
	c, ok := callerOf(r)
	return ok && c.Source != string(roleLocal)
}

// lassoTokenHeader carries a lasso bearer token when Authorization is taken,
// as it is behind Access's Managed OAuth.
const lassoTokenHeader = "X-Lasso-Token"

// normalizeLassoToken moves an X-Lasso-Token into Authorization, which is
// where every gate and the MCP SDK look, and on a network listener drops a
// bearer lasso did not issue (an Access OAuth token, say) so it is not
// mistaken for a bad lasso one.
func normalizeLassoToken(r *http.Request, edge bool) {
	if t := strings.TrimSpace(r.Header.Get(lassoTokenHeader)); t != "" {
		r.Header.Set("Authorization", "Bearer "+t)
		r.Header.Del(lassoTokenHeader)
		return
	}
	if !edge {
		return
	}
	if tok, ok := bearerToken(r); ok {
		if _, _, valid := verifyAccessToken(tok); !valid {
			r.Header.Del("Authorization")
		}
	}
}

func bearerToken(r *http.Request) (string, bool) {
	f := strings.Fields(r.Header.Get("Authorization"))
	if len(f) == 2 && strings.EqualFold(f[0], "bearer") {
		return f[1], true
	}
	return "", false
}

// uiTokenOK reports whether the request carries a lasso token granted the ui
// scope (a device token from the consent screen).
func uiTokenOK(r *http.Request) bool {
	tok, ok := bearerToken(r)
	if !ok {
		return false
	}
	return tokenHasScope(tok, scopeUI)
}

// machinePaths are what a machine caller may reach without a UI grant: the
// MCP surfaces and the credential-less OAuth endpoints.
var machinePaths = []string{
	"/mcp",
	"/herdr-mcp",
	"/cdp",
	"/hooks/bots",
	"/bot-channel",
	"/oauth/token",
	"/oauth/register",
	"/.well-known/oauth-protected-resource",
	"/.well-known/oauth-authorization-server",
}

// edgeConfig is what the network listeners verify against.
type edgeConfig struct {
	access  *accessVerifier
	tailnet *tailnetIdentity
	allow   allowList
	// owner is the tailnet login that owns this node: who -tailnet and
	// -listen-serve admit when LASSO_ALLOW is unset.
	owner string
}

// withCaller resolves the caller for every request and refuses one a network
// listener cannot vouch for.
func withCaller(next http.Handler, cfg *edgeConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := roleOf(r)
		c, why := resolveCaller(r, role, cfg)
		if why != "" {
			http.Error(w, "forbidden: "+why, http.StatusForbidden)
			return
		}
		normalizeLassoToken(r, role != roleLocal)
		if role != roleLocal {
			ok, ui := cfg.allow.admits(c, role, cfg.owner)
			if !ok {
				http.Error(w, "forbidden: "+c.describe()+" is not allowed into this lasso (LASSO_ALLOW)", http.StatusForbidden)
				return
			}
			c.UI = ui
			if c.Kind == "machine" && !c.UI && !pathUnder(r.URL.Path, machinePaths) && !uiTokenOK(r) {
				http.Error(w, "forbidden: a machine credential reaches /mcp, /herdr-mcp and /cdp only", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxCallerKey, c)))
	})
}

func (c caller) describe() string {
	if c.Name == "" {
		return "this caller"
	}
	return c.Name
}

// resolveCaller is the per-role proof check. A non-empty reason refuses.
func resolveCaller(r *http.Request, role listenerRole, cfg *edgeConfig) (caller, string) {
	switch role {
	case roleAccess:
		if cfg == nil || cfg.access == nil {
			return caller{}, "this listener has no Cloudflare Access configuration"
		}
		id, err := cfg.access.verifyRequest(r)
		if err != nil {
			return caller{}, "this listener takes only requests through Cloudflare Access (" + err.Error() + ")"
		}
		if id.Email != "" {
			return caller{Kind: "person", Name: strings.ToLower(id.Email), Source: string(roleAccess)}, ""
		}
		return caller{Kind: "machine", Name: "service:" + id.CommonName, Source: string(roleAccess)}, ""
	case roleTailnet:
		if cfg == nil || cfg.tailnet == nil {
			return caller{}, "this listener has no tailnet configuration"
		}
		who, err := cfg.tailnet.identify(r)
		if err != nil {
			return caller{}, "the tailnet could not say who is calling (" + err.Error() + ")"
		}
		return who, ""
	case roleServe:
		login := strings.TrimSpace(r.Header.Get("Tailscale-User-Login"))
		if login == "" {
			return caller{}, "tailscale serve did not identify a person (a tagged device, or Funnel traffic)"
		}
		return caller{Kind: "person", Name: strings.ToLower(login), Source: string(roleServe)}, ""
	}
	return caller{Kind: "local", Source: string(roleLocal)}, ""
}

// envList splits a comma-separated environment variable.
func envList(name string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(name), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
