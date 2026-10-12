package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- Access JWT -------------------------------------------------------------

type testAccess struct {
	key     *rsa.PrivateKey
	kid     string
	fetches int
	v       *accessVerifier
}

func newTestAccess(t *testing.T) *testAccess {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ta := &testAccess{key: k, kid: "kid-1"}
	v, err := newAccessVerifier("team", []string{"aud-lasso"})
	if err != nil {
		t.Fatal(err)
	}
	v.fetch = func(url string) ([]byte, error) {
		ta.fetches++
		if url != "https://team.cloudflareaccess.com/cdn-cgi/access/certs" {
			return nil, errors.New("unexpected url " + url)
		}
		return ta.jwks(), nil
	}
	ta.v = v
	return ta
}

func (ta *testAccess) jwks() []byte {
	b, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kid": ta.kid, "kty": "RSA", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(ta.key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(ta.key.E)).Bytes()),
	}}})
	return b
}

func (ta *testAccess) sign(t *testing.T, header, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, ta.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (ta *testAccess) personToken(t *testing.T, email string) string {
	return ta.sign(t, map[string]any{"alg": "RS256", "kid": ta.kid}, map[string]any{
		"aud": []string{"aud-lasso"}, "iss": "https://team.cloudflareaccess.com",
		"exp": time.Now().Add(time.Hour).Unix(), "email": email, "sub": "u1",
	})
}

func TestAccessVerify(t *testing.T) {
	ta := newTestAccess(t)
	now := time.Now()
	good := map[string]any{"aud": []string{"aud-lasso"}, "iss": "https://team.cloudflareaccess.com", "exp": now.Add(time.Hour).Unix(), "email": "You@Example.com"}
	hdr := map[string]any{"alg": "RS256", "kid": ta.kid}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range good {
			m[a] = b
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	id, err := ta.v.verify(ta.sign(t, hdr, good))
	if err != nil || id.Email != "You@Example.com" {
		t.Fatalf("good token: %v %+v", err, id)
	}
	svc, err := ta.v.verify(ta.sign(t, hdr, with("email", nil)))
	if err == nil {
		t.Fatalf("a token naming nobody passed: %+v", svc)
	}
	svc, err = ta.v.verify(ta.sign(t, hdr, func() map[string]any {
		m := with("email", "")
		m["common_name"] = "abc.access"
		m["aud"] = "aud-lasso"
		return m
	}()))
	if err != nil || svc.CommonName != "abc.access" {
		t.Fatalf("service token (string aud): %v %+v", err, svc)
	}
	bad := map[string]map[string]any{
		"wrong aud":    with("aud", []string{"other"}),
		"wrong issuer": with("iss", "https://evil.cloudflareaccess.com"),
		"expired":      with("exp", now.Add(-time.Hour).Unix()),
		"no exp":       with("exp", nil),
		"not yet":      with("nbf", now.Add(time.Hour).Unix()),
	}
	for name, claims := range bad {
		if _, err := ta.v.verify(ta.sign(t, hdr, claims)); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
	// alg none / HS256 refused, tampered payload refused.
	if _, err := ta.v.verify(ta.sign(t, map[string]any{"alg": "HS256", "kid": ta.kid}, good)); err == nil {
		t.Error("HS256 verified")
	}
	tok := ta.sign(t, hdr, good)
	parts := strings.Split(tok, ".")
	forged, _ := json.Marshal(with("email", "attacker@example.com"))
	parts[1] = base64.RawURLEncoding.EncodeToString(forged)
	if _, err := ta.v.verify(strings.Join(parts, ".")); err == nil {
		t.Error("tampered claims verified")
	}
	// Another key, same kid: signature fails.
	other := newTestAccess(t)
	if _, err := ta.v.verify(other.sign(t, hdr, good)); err == nil {
		t.Error("token signed by another key verified")
	}
}

func TestAccessKeyRefetchIsBounded(t *testing.T) {
	ta := newTestAccess(t)
	now := time.Now()
	ta.v.now = func() time.Time { return now }
	if _, err := ta.v.verify(ta.personToken(t, "a@b.c")); err != nil {
		t.Fatal(err)
	}
	unknown := map[string]any{"alg": "RS256", "kid": "made-up"}
	claims := map[string]any{"aud": "aud-lasso", "iss": "https://team.cloudflareaccess.com", "exp": now.Add(time.Hour).Unix(), "email": "a@b.c"}
	for i := 0; i < 20; i++ {
		_, _ = ta.v.verify(ta.sign(t, unknown, claims))
	}
	if ta.fetches != 1 {
		t.Fatalf("fetched %d times within a minute, want 1", ta.fetches)
	}
	now = now.Add(2 * time.Minute)
	_, _ = ta.v.verify(ta.sign(t, unknown, claims))
	if ta.fetches != 2 {
		t.Fatalf("fetches = %d after the refetch interval, want 2", ta.fetches)
	}
}

func TestNormalizeAccessTeam(t *testing.T) {
	for in, want := range map[string]string{
		"knowsuchagency":                     "knowsuchagency.cloudflareaccess.com",
		"https://Team.cloudflareaccess.com/": "team.cloudflareaccess.com",
		"auth.example.com":                   "auth.example.com",
	} {
		if got, err := normalizeAccessTeam(in); err != nil || got != want {
			t.Errorf("normalizeAccessTeam(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := normalizeAccessTeam("evil.com/x?"); err == nil {
		t.Error("a path was accepted as a team domain")
	}
}

// --- allow list ------------------------------------------------------------

func TestAllowList(t *testing.T) {
	person := caller{Kind: "person", Name: "you@example.com"}
	other := caller{Kind: "person", Name: "michael@example.com"}
	svc := caller{Kind: "machine", Name: "service:abc.access"}
	tagged := caller{Kind: "machine", Name: "tag:agents", Tags: []string{"tag:agents"}}

	unset := parseAllowList(nil)
	check := func(a allowList, c caller, role listenerRole, owner string, wantOK, wantUI bool) {
		t.Helper()
		ok, ui := a.admits(c, role, owner)
		if ok != wantOK || ui != wantUI {
			t.Errorf("admits(%s on %s) = %v,%v want %v,%v", c.Name, role, ok, ui, wantOK, wantUI)
		}
	}
	check(unset, person, roleAccess, "", true, true)
	check(unset, svc, roleAccess, "", true, false)
	check(unset, person, roleTailnet, "you@example.com", true, true)
	check(unset, other, roleTailnet, "you@example.com", false, true)
	check(unset, person, roleTailnet, "", false, true)
	check(unset, tagged, roleTailnet, "you@example.com", false, false)

	set := parseAllowList([]string{"You@Example.com", "tag:agents", "ui:service:abc.access"})
	check(set, person, roleAccess, "", true, true)
	check(set, other, roleAccess, "", false, true)
	check(set, other, roleTailnet, "michael@example.com", false, true)
	check(set, tagged, roleTailnet, "", true, false)
	check(set, svc, roleAccess, "", true, true)
	check(set, caller{Kind: "machine", Name: "service:zzz"}, roleAccess, "", false, false)
}

// --- the caller middleware -------------------------------------------------

type fakeTS struct {
	whoisFn func(addr string) (tsWhois, error)
	calls   int
}

func (f *fakeTS) status(context.Context) (tsStatus, error) { return tsStatus{}, nil }
func (f *fakeTS) whois(_ context.Context, addr string) (tsWhois, error) {
	f.calls++
	return f.whoisFn(addr)
}
func (f *fakeTS) certPair(context.Context, string) ([]byte, []byte, error) {
	return nil, nil, errors.New("no")
}

func serveAs(t *testing.T, h http.Handler, role listenerRole, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	r = r.WithContext(context.WithValue(r.Context(), ctxRoleKey, role))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestWithCaller(t *testing.T) {
	ta := newTestAccess(t)
	ts := &fakeTS{whoisFn: func(addr string) (tsWhois, error) {
		switch {
		case strings.HasPrefix(addr, "100.79.171.47:"):
			return tsWhois{Login: "You@Example.com", Node: "minime"}, nil
		case strings.HasPrefix(addr, "100.85.94.15:"):
			return tsWhois{Login: "michael@example.com", Node: "michaels-mac-mini"}, nil
		case strings.HasPrefix(addr, "100.99.0.1:"):
			return tsWhois{Login: "tagged-devices", Tags: []string{"tag:agents"}}, nil
		}
		return tsWhois{}, errors.New("peer not found")
	}}
	cfg := &edgeConfig{access: ta.v, tailnet: newTailnetIdentity(ts), owner: "you@example.com"}
	var got caller
	h := withCaller(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = callerOf(r)
		w.WriteHeader(http.StatusNoContent)
	}), cfg)

	req := func(path, remote string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}

	// local: no proof needed, caller is local.
	if w := serveAs(t, h, roleLocal, req("/api/version", "127.0.0.1:5555", nil)); w.Code != 204 || got.Kind != "local" {
		t.Fatalf("local: %d %+v", w.Code, got)
	}
	// access: a verified person.
	if w := serveAs(t, h, roleAccess, req("/api/version", "127.0.0.1:5555", map[string]string{accessJWTHdr: ta.personToken(t, "You@Example.com")})); w.Code != 204 || got.Name != "you@example.com" || got.Kind != "person" {
		t.Fatalf("access person: %d %+v", w.Code, got)
	}
	// access: no assertion, or a forged email header only → refused.
	if w := serveAs(t, h, roleAccess, req("/api/version", "127.0.0.1:5555", map[string]string{accessEmailHeader: "you@example.com"})); w.Code != 403 {
		t.Fatalf("access without assertion: %d", w.Code)
	}
	// access: a service token reaches /mcp but not /api.
	svc := ta.sign(t, map[string]any{"alg": "RS256", "kid": ta.kid}, map[string]any{"aud": "aud-lasso", "iss": "https://team.cloudflareaccess.com", "exp": time.Now().Add(time.Hour).Unix(), "common_name": "abc.access"})
	if w := serveAs(t, h, roleAccess, req("/mcp", "127.0.0.1:5555", map[string]string{accessJWTHdr: svc})); w.Code != 204 || got.Kind != "machine" {
		t.Fatalf("service token on /mcp: %d %+v", w.Code, got)
	}
	if w := serveAs(t, h, roleAccess, req("/api/file", "127.0.0.1:5555", map[string]string{accessJWTHdr: svc})); w.Code != 403 {
		t.Fatalf("service token on /api: %d", w.Code)
	}
	// tailnet: the owner gets in; Michael does not; a tagged node does not
	// without LASSO_ALLOW; a non-tailnet peer is refused.
	if w := serveAs(t, h, roleTailnet, req("/api/version", "100.79.171.47:40000", nil)); w.Code != 204 || got.Name != "you@example.com" {
		t.Fatalf("tailnet owner: %d %+v", w.Code, got)
	}
	if w := serveAs(t, h, roleTailnet, req("/api/version", "100.85.94.15:40000", nil)); w.Code != 403 {
		t.Fatalf("tailnet other user: %d", w.Code)
	}
	if w := serveAs(t, h, roleTailnet, req("/mcp", "100.99.0.1:40000", nil)); w.Code != 403 {
		t.Fatalf("tailnet tagged node without allow: %d", w.Code)
	}
	if w := serveAs(t, h, roleTailnet, req("/api/version", "192.168.1.5:40000", nil)); w.Code != 403 {
		t.Fatalf("tailnet non-tailnet peer: %d", w.Code)
	}
	// A forwarded identity header means nothing on the tailnet listener.
	if w := serveAs(t, h, roleTailnet, req("/api/version", "100.85.94.15:40000", map[string]string{"Tailscale-User-Login": "you@example.com"})); w.Code != 403 {
		t.Fatalf("tailnet trusted a forwarded header: %d", w.Code)
	}
	// whois answers are cached per address.
	before := ts.calls
	serveAs(t, h, roleTailnet, req("/api/version", "100.79.171.47:40001", nil))
	if ts.calls != before {
		t.Fatalf("whois not cached: %d calls", ts.calls-before)
	}
	// serve: the header names the person; none (tagged, Funnel) is refused.
	if w := serveAs(t, h, roleServe, req("/api/version", "127.0.0.1:5555", map[string]string{"Tailscale-User-Login": "you@example.com"})); w.Code != 204 || got.Source != "serve" {
		t.Fatalf("serve person: %d %+v", w.Code, got)
	}
	if w := serveAs(t, h, roleServe, req("/api/version", "127.0.0.1:5555", nil)); w.Code != 403 {
		t.Fatalf("serve without header: %d", w.Code)
	}
	// Webhooks and the bot channel carry their own key: no proof needed, and
	// a key sent as a bearer survives.
	hook := req("/hooks/bots/b/j", "127.0.0.1:5555", map[string]string{"Authorization": "Bearer job-key"})
	var sawAuth string
	hh := withCaller(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}), cfg)
	if w := serveAs(t, hh, roleAccess, hook); w.Code != 204 || sawAuth != "Bearer job-key" {
		t.Fatalf("webhook on the access listener: %d, Authorization %q", w.Code, sawAuth)
	}
	// A listener with no configuration fails closed.
	if w := serveAs(t, withCaller(http.NotFoundHandler(), &edgeConfig{}), roleAccess, req("/", "127.0.0.1:1", nil)); w.Code != 403 {
		t.Fatalf("unconfigured access listener: %d", w.Code)
	}
}

func TestLassoTokenComposition(t *testing.T) {
	openTestDB(t)
	addHostClient(t, "comp-host", "box", scopeSelf)
	tok, err := issueToken("access", "comp-host", oauthScope, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// X-Lasso-Token moves into Authorization on any listener.
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer access-oauth-opaque")
	r.Header.Set(lassoTokenHeader, tok)
	normalizeLassoToken(r, true)
	if got, _ := bearerToken(r); got != tok || r.Header.Get(lassoTokenHeader) != "" {
		t.Fatalf("X-Lasso-Token not moved: %q", got)
	}
	// Behind an edge, a bearer lasso did not issue is dropped…
	r = httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer oauth:from-access")
	normalizeLassoToken(r, true)
	if r.Header.Get("Authorization") != "" {
		t.Fatal("a foreign bearer survived behind the edge")
	}
	// …and kept on the local listener, where it is today's behaviour.
	r = httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer oauth:from-access")
	normalizeLassoToken(r, false)
	if r.Header.Get("Authorization") == "" {
		t.Fatal("a bearer was dropped on the local listener")
	}
}

func TestUIScopeTokens(t *testing.T) {
	openTestDB(t)
	addHostClient(t, "ui-dev", "", scopeFleet)
	mcpTok, _ := issueToken("access", "ui-dev", oauthScope, time.Hour)
	uiTok, _ := issueToken("access", "ui-dev", grantedScope("mcp ui"), time.Hour)
	expired, _ := issueToken("access", "ui-dev", grantedScope("ui"), -time.Hour)
	_ = expired
	if !tokenHasScope(uiTok, scopeUI) || tokenHasScope(mcpTok, scopeUI) {
		t.Fatal("ui scope not recorded or leaked to an mcp token")
	}
	h := withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), "u", "p", true)
	for tok, want := range map[string]int{uiTok: 204, mcpTok: 401, "made-up": 401} {
		r := httptest.NewRequest("GET", "/api/version", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("token %.6s…: %d, want %d", tok, w.Code, want)
		}
	}
	if grantedScope("") != oauthScope || grantedScope("mcp") != oauthScope || grantedScope("ui mcp") != "mcp ui" {
		t.Fatal("grantedScope")
	}
}

func TestEdgeCallerSatisfiesSharedSecrets(t *testing.T) {
	openTestDB(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	edgeReq := func(path string) *http.Request {
		r := httptest.NewRequest("GET", path, nil)
		return r.WithContext(context.WithValue(r.Context(), ctxCallerKey, caller{Kind: "person", Name: "you@example.com", Source: string(roleTailnet)}))
	}
	localReq := func(path string) *http.Request {
		r := httptest.NewRequest("GET", path, nil)
		return r.WithContext(context.WithValue(r.Context(), ctxCallerKey, caller{Kind: "local", Source: string(roleLocal)}))
	}
	w := httptest.NewRecorder()
	withAuth(ok, "u", "p", true).ServeHTTP(w, edgeReq("/api/version"))
	if w.Code != 204 {
		t.Fatalf("UI_AUTH refused an edge-verified person: %d", w.Code)
	}
	w = httptest.NewRecorder()
	withAuth(ok, "u", "p", true).ServeHTTP(w, localReq("/api/version"))
	if w.Code != 401 {
		t.Fatalf("UI_AUTH let a local caller through without the password: %d", w.Code)
	}
	prev := oauthCfg
	oauthCfg = oauthConf{Enabled: true, ClientID: "c", ClientSecret: "s"}
	t.Cleanup(func() { oauthCfg = prev })
	w = httptest.NewRecorder()
	withMCPAuth(ok, "", "", false).ServeHTTP(w, edgeReq("/mcp"))
	if w.Code != 204 {
		t.Fatalf("MCP_OAUTH refused an edge-verified person: %d", w.Code)
	}
	w = httptest.NewRecorder()
	withMCPAuth(ok, "", "", false).ServeHTTP(w, localReq("/mcp"))
	if w.Code != 401 {
		t.Fatalf("MCP_OAUTH let a local caller through without a token: %d", w.Code)
	}
	g := newAccessGate(true, "")
	w = httptest.NewRecorder()
	g.wrap(ok).ServeHTTP(w, edgeReq("/api/version"))
	if w.Code != 204 {
		t.Fatalf("the Access header gate refused an edge-verified caller: %d", w.Code)
	}
}

func TestAccessGateVerifiesWhenConfigured(t *testing.T) {
	ta := newTestAccess(t)
	g := newAccessGate(true, "you@example.com")
	g.verifier = ta.v
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	cases := map[string]struct {
		hdr  map[string]string
		want int
	}{
		"forged email header": {map[string]string{accessEmailHeader: "you@example.com"}, 403},
		"verified, allowed":   {map[string]string{accessJWTHdr: ta.personToken(t, "you@example.com")}, 204},
		"verified, other":     {map[string]string{accessJWTHdr: ta.personToken(t, "michael@example.com")}, 403},
	}
	for name, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		for k, v := range c.hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		g.wrap(ok).ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", name, w.Code, c.want)
		}
	}
}

func TestSplitPEMPair(t *testing.T) {
	in := "-----BEGIN EC PRIVATE KEY-----\nAAA\n-----END EC PRIVATE KEY-----\n-----BEGIN CERTIFICATE-----\nBBB\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\nCCC\n-----END CERTIFICATE-----\n"
	c, k, err := splitPEMPair([]byte(in))
	if err != nil || !strings.Contains(string(k), "AAA") || strings.Contains(string(k), "BBB") || !strings.Contains(string(c), "BBB") || !strings.Contains(string(c), "CCC") {
		t.Fatalf("split: %v\ncert=%q\nkey=%q", err, c, k)
	}
}

func TestConnRoleThroughTLS(t *testing.T) {
	if connRole(roleConn{role: roleTailnet}) != roleTailnet {
		t.Fatal("roleConn")
	}
	if connRole(nil) != roleLocal {
		t.Fatal("default role")
	}
}
