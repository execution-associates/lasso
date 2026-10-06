package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A remote browser is a profile with a cdp_url: a Chromium somewhere else —
// another machine on the tailnet, a container, a tunnel — whose DevTools HTTP
// endpoint lasso dials instead of launching a browser of its own. lasso is
// agnostic about it: how that browser is run, kept alive and secured is up to
// whoever runs it. All lasso needs is the standard remote-debugging endpoint
// (/json/version, /json/list, /json/new, the /devtools/ websockets), and
// everything above that — /cdp/p/<id>, /browser-mcp, the Browser tab — is the
// same code a launched browser goes through.

// browserDefaultCDPURLSetting holds the default profile's cdp_url. The other
// profiles keep theirs in their entry of the browser_profiles list.
const browserDefaultCDPURLSetting = "browser_default_cdp_url"

// remoteCheckEvery is how long a remote browser's identity is trusted before
// the next /cdp request re-reads /json/version. A remote browser restarts on
// its own schedule and comes back with a new /devtools/browser/<id>; without a
// re-read, bare /cdp would keep pointing at the id that is gone.
const remoteCheckEvery = 2 * time.Second

// isBrowserWSPath says a websocket path is a browser target's: Chromium's
// /devtools/browser/<id>, or a bare /devtools/browser with no id, which is
// what a stateless CDP service such as Kitesurf (kitesurf.dev) answers.
func isBrowserWSPath(p string) bool {
	return p == "/devtools/browser" || strings.HasPrefix(p, "/devtools/browser/")
}

// validateCDPURL accepts a remote browser's DevTools HTTP base,
// http(s)://host[:port], and answers it normalized. "" means "lasso launches
// this profile's browser itself". Paths, queries and credentials are refused:
// lasso appends the /json and /devtools paths itself and sends no credentials
// of its own, so anything in the URL beyond scheme://host:port would be
// silently dropped.
func validateCDPURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("cdp_url %q is not a URL", raw)
	}
	switch u.Scheme {
	case "http", "https":
	case "ws", "wss":
		return "", fmt.Errorf("cdp_url %q is a websocket URL; give the browser's DevTools HTTP base instead, e.g. http://%s", raw, u.Host)
	default:
		return "", fmt.Errorf("cdp_url %q must be http://host:port or https://host[:port]", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("cdp_url must not carry credentials; lasso sends none")
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("cdp_url %q has no host", raw)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("cdp_url %q must be just scheme://host:port (lasso adds the /json and /devtools paths)", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// remoteAddr is the host:port a cdp_url dials, the scheme's port when none is
// given.
func remoteAddr(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// cdpHostHeader is the Host lasso sends a remote browser. Chromium's DevTools
// server answers only a Host that is an IP or "localhost" (its DNS-rebinding
// guard), so plain http to a host NAME goes out as localhost:<port>. Over
// https the name is kept: what answers there is something in front of
// Chromium (a tunnel, a proxy), which routes by Host and rewrites it itself.
func cdpHostHeader(scheme, addr string) string {
	if scheme != "http" {
		return addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "localhost" || net.ParseIP(host) != nil {
		return addr
	}
	return net.JoinHostPort("localhost", port)
}

// dialRemoteBrowser reads a remote browser's /json/version and answers a proc for it.
func dialRemoteBrowser(raw string) (*browserProc, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	p := &browserProc{addr: remoteAddr(u), scheme: u.Scheme, started: time.Now(), exited: make(chan struct{})}
	var v struct {
		Browser string `json:"Browser"`
		WS      string `json:"webSocketDebuggerUrl"`
	}
	if err := devtoolsDo(p, http.MethodGet, "/json/version", &v); err != nil {
		return nil, fmt.Errorf("no browser answering at %s: %w", raw, err)
	}
	wu, err := url.Parse(v.WS)
	if err != nil || !isBrowserWSPath(wu.Path) {
		return nil, fmt.Errorf("%s answered /json/version without a browser websocket (%q)", raw, v.WS)
	}
	p.wsPath, p.bin = wu.Path, v.Browser
	return p, nil
}

// ensureRemote is ensure for a profile with a cdp_url: there is nothing to
// launch, only a browser to (re)find. A browser that has restarted since the
// last look (a new /devtools/browser/<id>) or stopped answering ends this
// profile's sessions the way a local browser's exit does, through onStop, so
// /browser-mcp children reconnect to whatever is there now. A browser with an
// id-less /devtools/browser path cannot be seen restarting, only stopping.
func (m *browserManager) ensureRemote(ctx context.Context, raw string) (*browserProc, error) {
	m.mu.Lock()
	cur, fresh := m.proc, time.Since(m.remoteChecked) < remoteCheckEvery
	m.mu.Unlock()
	if cur != nil && fresh {
		return cur, nil
	}
	if err := m.acquire(ctx); err != nil {
		return nil, err
	}
	defer m.release()
	if m.retired.Load() {
		return nil, fmt.Errorf("the browser profile %q was deleted", m.profileID())
	}
	p, err := dialRemoteBrowser(raw)
	m.mu.Lock()
	cur = m.proc
	switch {
	case err != nil:
		m.proc, m.lastErr = nil, err.Error()
	case cur != nil && cur.addr == p.addr && cur.wsPath == p.wsPath:
		p = cur // the same browser: keep its started time
		m.lastErr = ""
	default:
		m.proc, m.lastErr = p, ""
	}
	m.remoteChecked = time.Now()
	m.lastUsed = time.Now()
	m.mu.Unlock()
	if cur != nil && cur != p && m.onStop != nil {
		why := "the remote browser restarted"
		if err != nil {
			why = "the remote browser stopped answering"
		}
		m.onStop(why)
	}
	if err != nil {
		return nil, err
	}
	if cur != p {
		log.Printf("browser: profile %q attached to remote %s at %s", m.profileID(), p.bin, raw)
	}
	return p, nil
}
