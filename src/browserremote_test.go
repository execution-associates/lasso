package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateCDPURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "",
		"  ":                         "",
		"http://100.79.171.47:9222":  "http://100.79.171.47:9222",
		"http://100.79.171.47:9222/": "http://100.79.171.47:9222",
		"https://browser.example":    "https://browser.example",
		" http://localhost:9223 ":    "http://localhost:9223",
	} {
		got, err := validateCDPURL(in)
		if err != nil || got != want {
			t.Errorf("validateCDPURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"100.79.171.47:9222",                           // no scheme
		"ws://100.79.171.47:9222/devtools/browser/abc", // a websocket, not the HTTP base
		"http://u:p@host:9222",                         // credentials
		"http://host:9222/json/version",                // a path
		"http://host:9222?token=x",                     // a query
		"socks5://host:1080",                           // not CDP at all
		"http://:9222",                                 // no host
	} {
		if _, err := validateCDPURL(bad); err == nil {
			t.Errorf("validateCDPURL(%q) accepted", bad)
		}
	}
}

func TestCDPHostHeader(t *testing.T) {
	for _, c := range []struct{ scheme, addr, want string }{
		{"http", "100.79.171.47:9222", "100.79.171.47:9222"},
		{"http", "localhost:9222", "localhost:9222"},
		{"http", "[::1]:9222", "[::1]:9222"},
		// Chromium refuses a Host that is a name: plain http to one goes out as localhost.
		{"http", "minime.tail9dd8e.ts.net:9222", "localhost:9222"},
		// Over https something else answers, and routes by the real name.
		{"https", "browser.example:443", "browser.example:443"},
	} {
		if got := cdpHostHeader(c.scheme, c.addr); got != c.want {
			t.Errorf("cdpHostHeader(%s, %s) = %s, want %s", c.scheme, c.addr, got, c.want)
		}
	}
}

// remoteChromium is a DevTools endpoint whose browser id can change, the way a
// remote browser's does when it restarts, and which can stop answering.
type remoteChromium struct {
	srv  *httptest.Server
	id   atomic.Value // string
	down atomic.Bool
	mu   sync.Mutex
	host []string // Host headers seen
}

func newRemoteChromium(t *testing.T) *remoteChromium {
	t.Helper()
	rc := &remoteChromium{}
	rc.id.Store("one")
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc.mu.Lock()
		rc.host = append(rc.host, r.Host)
		rc.mu.Unlock()
		if rc.down.Load() {
			http.Error(w, "gone", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/json/version":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Browser":"Chrome/153.0.8010.53","webSocketDebuggerUrl":"ws://%s/devtools/browser/%s"}`, r.Host, rc.id.Load())
		case "/json/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `[{"id":"R1","type":"page","title":"Remote","url":"https://example.com/","webSocketDebuggerUrl":"ws://%s/devtools/page/R1"}]`, r.Host)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func TestRemoteBrowserProfile(t *testing.T) {
	f := testFleet(t)
	rc := newRemoteChromium(t)
	var stops []string
	f.onStop = func(profile, why string) { stops = append(stops, profile+": "+why) }

	if _, err := f.create("Mini", "", "socks5://127.0.0.1:1080", rc.srv.URL); !errors.Is(err, errRemoteProxy) {
		t.Fatalf("remote with a proxy: %v", err)
	}
	p, err := f.create("Mini", "", "", rc.srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if p.CDPURL != rc.srv.URL {
		t.Errorf("stored cdp_url %q, want it normalized to %q", p.CDPURL, rc.srv.URL)
	}
	m, err := browserFor("mini")
	if err != nil {
		t.Fatal(err)
	}
	proc, err := m.ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !proc.remote() || proc.wsPath != "/devtools/browser/one" || proc.bin != "Chrome/153.0.8010.53" {
		t.Errorf("proc = %+v", proc)
	}
	st, _ := f.profileStatus("mini")
	if !st.Running || st.CDPURL != rc.srv.URL || len(st.Pages) != 1 || st.Pages[0].ID != "R1" {
		t.Errorf("status = %+v", st)
	}

	// A proxy is refused on a remote browser, as caller input.
	if err := m.applyProxy(context.Background(), "socks5://127.0.0.1:1080"); !errors.As(err, new(errBadProxy)) {
		t.Errorf("applyProxy on a remote browser: %v", err)
	}
	// The default profile stays lasso's own browser.
	u := rc.srv.URL
	if _, err := f.update(defaultBrowserProfile, nil, nil, &u); err == nil {
		t.Error("the default profile took a cdp_url")
	}

	// Within remoteCheckEvery the same proc is answered without a round trip.
	if again, _ := m.ensure(context.Background()); again != proc {
		t.Error("a fresh remote browser was re-dialed")
	}

	// It restarts: a new browser id. The next look after the check interval
	// attaches to it and ends the old one's sessions.
	rc.id.Store("two")
	m.mu.Lock()
	m.remoteChecked = time.Time{}
	m.mu.Unlock()
	proc2, err := m.ensure(context.Background())
	if err != nil || proc2.wsPath != "/devtools/browser/two" {
		t.Fatalf("after restart: %+v, %v", proc2, err)
	}
	if len(stops) != 1 || !strings.Contains(stops[0], "restarted") {
		t.Errorf("stops = %q", stops)
	}

	// It goes away: the next look fails, says why, and lets go of it.
	rc.down.Store(true)
	m.mu.Lock()
	m.remoteChecked = time.Time{}
	m.mu.Unlock()
	if _, err := m.ensure(context.Background()); err == nil {
		t.Fatal("ensure succeeded against a browser that is down")
	}
	if m.current() != nil {
		t.Error("still attached to a browser that stopped answering")
	}
	if st, _ := f.profileStatus("mini"); st.Running || !strings.Contains(st.Reason, "no browser answering") {
		t.Errorf("status after it went away: %+v", st)
	}
	if len(stops) != 2 || !strings.Contains(stops[1], "stopped answering") {
		t.Errorf("stops = %q", stops)
	}

	// Stopping a remote browser only detaches; the browser itself is not lasso's.
	rc.down.Store(false)
	if _, err := m.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.stop(context.Background(), "test"); err != nil || m.current() != nil {
		t.Errorf("stop: %v, still attached %v", err, m.current() != nil)
	}
}

func TestRemoteBrowserEditSwitchesKinds(t *testing.T) {
	f := testFleet(t)
	rc := newRemoteChromium(t)
	if _, err := f.create("Mini", "", "socks5://127.0.0.1:1080", ""); err != nil {
		t.Fatal(err)
	}
	// Launched with a proxy → remote: refused unless the proxy is cleared in
	// the same change, which is then one valid edit.
	u := rc.srv.URL
	if _, err := f.edit(context.Background(), "mini", nil, nil, &u); !errors.Is(err, errRemoteProxy) {
		t.Errorf("remote while a proxy is still set: %v", err)
	}
	none := ""
	st, err := f.edit(context.Background(), "mini", nil, &none, &u)
	if err != nil || st.CDPURL != u || st.Proxy != "" {
		t.Fatalf("switch to remote: %+v, %v", st, err)
	}
	// And back, with a proxy again.
	proxy := "socks5://127.0.0.1:1080"
	st, err = f.edit(context.Background(), "mini", nil, &proxy, &none)
	if err != nil || st.CDPURL != "" || st.Proxy != proxy {
		t.Fatalf("switch back: %+v, %v", st, err)
	}
}

func TestCDPProxyServesARemoteBrowser(t *testing.T) {
	f := testFleet(t)
	rc := newRemoteChromium(t)
	if _, err := f.create("Mini", "", "", rc.srv.URL); err != nil {
		t.Fatal(err)
	}
	lasso := httptest.NewServer(http.HandlerFunc(serveCDPRouted))
	defer lasso.Close()
	lu, _ := url.Parse(lasso.URL)

	resp, err := http.Get(lasso.URL + "/cdp/p/mini/json/version")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	// The remote browser's own address never reaches the client: its
	// websocket comes back as the profile's stable lasso address, which
	// survives the remote browser restarting under a new id.
	want := `"ws://` + lu.Host + `/cdp/p/mini"`
	if !strings.Contains(string(body), want) {
		t.Errorf("version %s lacks %s", body, want)
	}
	ru, _ := url.Parse(rc.srv.URL)
	if strings.Contains(string(body), ru.Host) {
		t.Errorf("version %s leaks the remote address", body)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for _, h := range rc.host {
		if h != ru.Host {
			t.Errorf("remote browser saw Host %q, want %q", h, ru.Host)
		}
	}
}
