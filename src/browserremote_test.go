package main

import (
	"context"
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

	p, err := f.create("Mini", "", rc.srv.URL+"/")
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
	if _, err := f.create("Mini", "", ""); err != nil {
		t.Fatal(err)
	}
	// Launched → remote → launched, each one edit.
	u := rc.srv.URL
	st, err := f.edit(context.Background(), "mini", nil, &u)
	if err != nil || st.CDPURL != u {
		t.Fatalf("switch to remote: %+v, %v", st, err)
	}
	none := ""
	st, err = f.edit(context.Background(), "mini", nil, &none)
	if err != nil || st.CDPURL != "" {
		t.Fatalf("switch back: %+v, %v", st, err)
	}
}

func TestCDPProxyServesARemoteBrowser(t *testing.T) {
	f := testFleet(t)
	rc := newRemoteChromium(t)
	if _, err := f.create("Mini", "", rc.srv.URL); err != nil {
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

func TestDefaultProfileCanBeRemote(t *testing.T) {
	f := testFleet(t)
	rc := newRemoteChromium(t)
	sharedBrowser.cdpURL = func() string { v, _ := getSetting(browserDefaultCDPURLSetting); return v }
	u := rc.srv.URL
	name := "minime"
	st, err := f.edit(context.Background(), defaultBrowserProfile, &name, &u)
	if err != nil || st.CDPURL != u || st.Name != "minime" {
		t.Fatalf("default → remote: %+v, %v", st, err)
	}
	p, err := sharedBrowser.ensure(context.Background())
	if err != nil || !p.remote() || p.wsPath != "/devtools/browser/one" {
		t.Fatalf("ensure: %+v, %v", p, err)
	}
	if bs := sharedBrowser.status(); !bs.Available || !bs.Running || bs.Binary != "Chrome/153.0.8010.53" {
		t.Errorf("status: available=%v running=%v binary=%q", bs.Available, bs.Running, bs.Binary)
	}
}

// kitesurf is a stateless CDP service in Kitesurf's (kitesurf.dev) shape: its
// browser websocket is a bare /devtools/browser with no id, it lists one page,
// and it opens no tabs over HTTP (PUT /json/new is a 404).
func newKitesurf(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/json/version":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Browser":"Kitesurf/0.0.1","Protocol-Version":"1.3","webSocketDebuggerUrl":"wss://kitesurf.dev/devtools/browser"}`)
		case "/json/list", "/json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[{"id":"kitesurf","type":"page","title":"","url":"about:blank","webSocketDebuggerUrl":"wss://kitesurf.dev/devtools/page/kitesurf"}]`)
		case "/devtools/browser":
			fmt.Fprint(w, "browser target")
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestRemoteBrowserWithoutAnID(t *testing.T) {
	f := testFleet(t)
	ks, seen := newKitesurf(t)
	var stops []string
	f.onStop = func(profile, why string) { stops = append(stops, profile+": "+why) }
	if _, err := f.create("Kite", "kite", ks.URL); err != nil {
		t.Fatal(err)
	}
	m, err := browserFor("kite")
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.ensure(context.Background())
	if err != nil {
		t.Fatalf("an id-less /devtools/browser was refused: %v", err)
	}
	if p.wsPath != "/devtools/browser" || p.bin != "Kitesurf/0.0.1" {
		t.Errorf("proc = %+v", p)
	}
	// A re-read after remoteCheckEvery finds the same path: the same browser,
	// not a restart.
	m.mu.Lock()
	m.remoteChecked = time.Time{}
	m.mu.Unlock()
	if again, err := m.ensure(context.Background()); err != nil || again != p {
		t.Errorf("re-read: %v, same proc %v", err, again == p)
	}
	if len(stops) != 0 {
		t.Errorf("a re-read of an id-less browser read as a restart: %v", stops)
	}

	lasso := httptest.NewServer(http.HandlerFunc(serveCDPRouted))
	defer lasso.Close()
	lu, _ := url.Parse(lasso.URL)
	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(lasso.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
		}
		return string(b)
	}
	// The browser websocket maps to the profile's stable address, like an
	// id'd one, and a page's keeps its path under the prefix.
	if body := get("/cdp/p/kite/json/version"); !strings.Contains(body, `"ws://`+lu.Host+`/cdp/p/kite"`) || strings.Contains(body, "kitesurf.dev") {
		t.Errorf("version = %s", body)
	}
	if body := get("/cdp/p/kite/json/list"); !strings.Contains(body, `"ws://`+lu.Host+`/cdp/p/kite/devtools/page/kitesurf"`) {
		t.Errorf("list = %s", body)
	}
	// Bare /cdp/p/kite reaches the id-less browser target.
	if body := get("/cdp/p/kite"); body != "browser target" {
		t.Errorf("bare prefix reached %q", body)
	}
	found := false
	for _, s := range *seen {
		found = found || s == "GET /devtools/browser"
	}
	if !found {
		t.Errorf("upstream never saw /devtools/browser: %v", *seen)
	}

	// It opens no tabs over HTTP: the 404 is recognizable, so open_browser_tab
	// can say what happened rather than pass on a bare "not found".
	err = devtoolsDo(p, http.MethodPut, "/json/new?about:blank", nil)
	if !devtoolsNotFound(err) {
		t.Errorf("PUT /json/new = %v, want a 404", err)
	}
}

func TestBrowserWSPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/devtools/browser":       true,
		"/devtools/browser/abc":   true,
		"/devtools/browserx":      false,
		"/devtools/page/kitesurf": false,
		"/":                       false,
	} {
		if got := isBrowserWSPath(p); got != want {
			t.Errorf("isBrowserWSPath(%q) = %v", p, got)
		}
	}
	if got := cdpPublicPathAt("/cdp/p/k", "/devtools/browser"); got != "/cdp/p/k" {
		t.Errorf("public path of an id-less browser = %q", got)
	}
}
