package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// -tailnet: lasso bound to this node's tailnet addresses, with tailscaled's
// certificate, identifying every caller by tailscaled's whois of the socket
// peer.
//
// Only the real peer counts. Forwarded headers (X-Forwarded-For,
// Tailscale-User-Login) are never read here: anything on the path could write
// them. The peer must be a tailnet address; the Host must be one lasso answers
// to (the local guard), which stops DNS rebinding from a tailnet person's own
// browser. A tagged node is its tags, never its owner's login.
//
// tailscaled is asked over its LocalAPI unix socket with plain net/http (no
// Tailscale Go module), else through the `tailscale` CLI, which is how the
// macOS app is reached. Answers are cached per peer address for a minute,
// failures for five seconds.

const (
	tailnetWhoisTTL     = time.Minute
	tailnetWhoisFailTTL = 5 * time.Second
	tailnetWhoisMax     = 4096
)

type tsStatus struct {
	DNSName    string
	IPs        []string
	OwnerLogin string // "" when this node is tagged
	Tags       []string
}

type tsWhois struct {
	Login string
	Node  string
	Tags  []string
}

type tailscaleAPI interface {
	status(ctx context.Context) (tsStatus, error)
	whois(ctx context.Context, addr string) (tsWhois, error)
	certPair(ctx context.Context, domain string) (certPEM, keyPEM []byte, err error)
}

// The JSON shapes the LocalAPI and `tailscale ... --json` share.
type tsStatusJSON struct {
	Self struct {
		DNSName      string
		TailscaleIPs []string
		UserID       json.Number
		Tags         []string
	}
	User map[string]struct{ LoginName string }
}

type tsWhoisJSON struct {
	Node struct {
		Name string
		Tags []string
	}
	UserProfile struct{ LoginName string }
}

func (s tsStatusJSON) status() tsStatus {
	st := tsStatus{
		DNSName: strings.TrimSuffix(s.Self.DNSName, "."),
		IPs:     s.Self.TailscaleIPs,
		Tags:    s.Self.Tags,
	}
	if len(st.Tags) == 0 {
		if u, ok := s.User[s.Self.UserID.String()]; ok {
			st.OwnerLogin = u.LoginName
		}
	}
	return st
}

func (w tsWhoisJSON) whois() tsWhois {
	return tsWhois{Login: w.UserProfile.LoginName, Node: strings.TrimSuffix(w.Node.Name, "."), Tags: w.Node.Tags}
}

// localAPI talks to tailscaled's LocalAPI over its unix socket.
type localAPI struct {
	hc *http.Client
}

func newLocalAPI(sock string) *localAPI {
	return &localAPI{hc: &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}},
	}}
}

func (l *localAPI) get(ctx context.Context, path string) ([]byte, error) {
	// The LocalAPI answers only this Host (its own rebinding guard).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tailscaled %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (l *localAPI) status(ctx context.Context) (tsStatus, error) {
	b, err := l.get(ctx, "/localapi/v0/status?peers=false")
	if err != nil {
		return tsStatus{}, err
	}
	var s tsStatusJSON
	if err := json.Unmarshal(b, &s); err != nil {
		return tsStatus{}, err
	}
	return s.status(), nil
}

func (l *localAPI) whois(ctx context.Context, addr string) (tsWhois, error) {
	b, err := l.get(ctx, "/localapi/v0/whois?addr="+url.QueryEscape(addr))
	if err != nil {
		return tsWhois{}, err
	}
	var w tsWhoisJSON
	if err := json.Unmarshal(b, &w); err != nil {
		return tsWhois{}, err
	}
	return w.whois(), nil
}

func (l *localAPI) certPair(ctx context.Context, domain string) ([]byte, []byte, error) {
	b, err := l.get(ctx, "/localapi/v0/cert/"+url.PathEscape(domain)+"?type=pair")
	if err != nil {
		return nil, nil, err
	}
	return splitPEMPair(b)
}

// splitPEMPair separates the LocalAPI's "pair" answer: the key block and the
// certificate chain, in whichever order they come.
func splitPEMPair(b []byte) (certPEM, keyPEM []byte, err error) {
	for len(b) > 0 {
		i := bytes.Index(b, []byte("-----BEGIN "))
		if i < 0 {
			break
		}
		b = b[i:]
		end := bytes.Index(b, []byte("-----END "))
		if end < 0 {
			break
		}
		nl := bytes.IndexByte(b[end:], '\n')
		if nl < 0 {
			nl = len(b) - end
		} else {
			nl++
		}
		block := b[:end+nl]
		if bytes.Contains(block[:bytes.IndexByte(block, '\n')+1], []byte("PRIVATE KEY")) {
			keyPEM = append(keyPEM, block...)
		} else {
			certPEM = append(certPEM, block...)
		}
		b = b[end+nl:]
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, nil, errors.New("tailscaled returned no certificate and key")
	}
	return certPEM, keyPEM, nil
}

// cliTailscale is the fallback when there is no LocalAPI socket to dial.
type cliTailscale struct {
	bin string
	dir string // where `tailscale cert` writes, 0700
}

func (c cliTailscale) run(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, c.bin, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("tailscale %s: %v: %s", args[0], err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func (c cliTailscale) status(ctx context.Context) (tsStatus, error) {
	b, err := c.run(ctx, "status", "--json", "--peers=false")
	if err != nil {
		return tsStatus{}, err
	}
	var s tsStatusJSON
	if err := json.Unmarshal(b, &s); err != nil {
		return tsStatus{}, err
	}
	return s.status(), nil
}

func (c cliTailscale) whois(ctx context.Context, addr string) (tsWhois, error) {
	b, err := c.run(ctx, "whois", "--json", addr)
	if err != nil {
		return tsWhois{}, err
	}
	var w tsWhoisJSON
	if err := json.Unmarshal(b, &w); err != nil {
		return tsWhois{}, err
	}
	return w.whois(), nil
}

func (c cliTailscale) certPair(ctx context.Context, domain string) ([]byte, []byte, error) {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return nil, nil, err
	}
	crt, key := filepath.Join(c.dir, "tailnet.crt"), filepath.Join(c.dir, "tailnet.key")
	if _, err := c.run(ctx, "cert", "--cert-file", crt, "--key-file", key, domain); err != nil {
		return nil, nil, err
	}
	cp, err := os.ReadFile(crt)
	if err != nil {
		return nil, nil, err
	}
	kp, err := os.ReadFile(key)
	if err != nil {
		return nil, nil, err
	}
	return cp, kp, nil
}

// newTailscaleAPI picks the LocalAPI socket when there is one, else the CLI.
func newTailscaleAPI(certDir string) (tailscaleAPI, error) {
	for _, s := range []string{os.Getenv("TS_SOCKET"), "/var/run/tailscale/tailscaled.sock", "/run/tailscale/tailscaled.sock"} {
		if s == "" {
			continue
		}
		if fi, err := os.Stat(s); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return newLocalAPI(s), nil
		}
	}
	bin, err := exec.LookPath("tailscale")
	if err != nil {
		for _, p := range []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale", "/opt/homebrew/bin/tailscale", "/usr/local/bin/tailscale"} {
			if _, serr := os.Stat(p); serr == nil {
				bin, err = p, nil
				break
			}
		}
	}
	if err != nil {
		return nil, errors.New("neither tailscaled's LocalAPI socket nor the tailscale CLI was found")
	}
	return cliTailscale{bin: bin, dir: certDir}, nil
}

// tailnetIdentity answers "who is behind this connection" with a cache.
type tailnetIdentity struct {
	api tailscaleAPI
	now func() time.Time

	mu    sync.Mutex
	cache map[netip.Addr]tailnetWhoisEntry
}

type tailnetWhoisEntry struct {
	at  time.Time
	who tsWhois
	err error
}

func newTailnetIdentity(api tailscaleAPI) *tailnetIdentity {
	return &tailnetIdentity{api: api, now: time.Now, cache: map[netip.Addr]tailnetWhoisEntry{}}
}

var (
	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

func isTailnetAddr(a netip.Addr) bool {
	a = a.Unmap()
	return tailnetV4.Contains(a) || tailnetV6.Contains(a)
}

// identify resolves the request's socket peer to a caller.
func (t *tailnetIdentity) identify(r *http.Request) (caller, error) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return caller{}, errors.New("no peer address")
	}
	if !isTailnetAddr(ap.Addr()) {
		return caller{}, errors.New("the peer is not a tailnet address")
	}
	w, err := t.lookup(r.Context(), ap)
	if err != nil {
		return caller{}, err
	}
	if len(w.Tags) > 0 {
		return caller{Kind: "machine", Name: strings.Join(w.Tags, ","), Source: string(roleTailnet), Tags: w.Tags}, nil
	}
	if w.Login == "" {
		return caller{}, errors.New("tailscaled named no user for the peer")
	}
	return caller{Kind: "person", Name: strings.ToLower(w.Login), Source: string(roleTailnet)}, nil
}

func (t *tailnetIdentity) lookup(ctx context.Context, ap netip.AddrPort) (tsWhois, error) {
	ip := ap.Addr().Unmap()
	now := t.now()
	t.mu.Lock()
	if e, ok := t.cache[ip]; ok {
		ttl := tailnetWhoisTTL
		if e.err != nil {
			ttl = tailnetWhoisFailTTL
		}
		if now.Sub(e.at) < ttl {
			t.mu.Unlock()
			return e.who, e.err
		}
	}
	t.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	w, err := t.api.whois(ctx, ap.String())
	t.mu.Lock()
	if len(t.cache) >= tailnetWhoisMax {
		t.cache = map[netip.Addr]tailnetWhoisEntry{}
	}
	t.cache[ip] = tailnetWhoisEntry{at: now, who: w, err: err}
	t.mu.Unlock()
	return w, err
}

// tailnetCert keeps tailscaled's certificate for this node's name, renewing it
// daily (tailscaled itself renews the underlying Let's Encrypt certificate).
type tailnetCert struct {
	api    tailscaleAPI
	domain string

	mu   sync.Mutex
	cert *tls.Certificate
	at   time.Time
}

func (c *tailnetCert) load(ctx context.Context) error {
	cp, kp, err := c.api.certPair(ctx, c.domain)
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(cp, kp)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cert, c.at = &pair, time.Now()
	c.mu.Unlock()
	return nil
}

func (c *tailnetCert) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	cert, at := c.cert, c.at
	c.mu.Unlock()
	if cert == nil || time.Since(at) > 24*time.Hour {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.load(ctx); err != nil {
			if cert != nil {
				log.Printf("tailnet:  renewing the certificate failed, serving the old one: %v", err)
				return cert, nil
			}
			return nil, err
		}
		c.mu.Lock()
		cert = c.cert
		c.mu.Unlock()
	}
	return cert, nil
}

// tailnetListeners binds every tailnet address of this node on port, serving
// HTTPS with tailscaled's certificate. It returns the listeners and the URL.
func tailnetListeners(ctx context.Context, api tailscaleAPI, st tsStatus, port string) ([]net.Listener, string, error) {
	if st.DNSName == "" {
		return nil, "", errors.New("this node has no MagicDNS name: enable MagicDNS in the tailnet")
	}
	if len(st.IPs) == 0 {
		return nil, "", errors.New("this node has no tailnet address: is tailscale up?")
	}
	cert := &tailnetCert{api: api, domain: st.DNSName}
	if err := cert.load(ctx); err != nil {
		return nil, "", fmt.Errorf("getting a certificate for %s (enable HTTPS certificates in the tailnet admin console, and on Linux run `sudo tailscale set --operator=$USER`): %w", st.DNSName, err)
	}
	cfg := &tls.Config{GetCertificate: cert.getCertificate, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}
	var lns []net.Listener
	for _, ip := range st.IPs {
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, port))
		if err != nil {
			for _, l := range lns {
				l.Close()
			}
			return nil, "", fmt.Errorf("listen on %s: %w", net.JoinHostPort(ip, port), err)
		}
		lns = append(lns, tls.NewListener(roleListener{Listener: ln, role: roleTailnet}, cfg))
	}
	return lns, "https://" + net.JoinHostPort(st.DNSName, port), nil
}
