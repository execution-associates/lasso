package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GET /api/browser/proxies: the SOCKS5 proxies the shared browser could use
// right now, for the profile dialog's proxy dropdown. It probes port 1080 on
// loopback, on this machine's own tailnet address and on every ONLINE tailnet
// peer, from lasso's machine, because that is where Chromium runs: a hit means
// the browser can actually reach it. A host counts only when it answers the
// SOCKS5 greeting, not merely when the port is open. Answers are cached for
// proxyScanTTL; ?refresh=1 rescans.

const (
	proxyScanPort     = 1080
	proxyScanTTL      = 30 * time.Second
	proxyProbeTimeout = time.Second
)

type proxyCandidate struct {
	Name   string `json:"name"`   // "this machine", or the tailnet host name
	Addr   string `json:"addr"`   // host:port
	Source string `json:"source"` // "local" or "tailnet"
	URL    string `json:"url"`    // socks5://host:port, what the input gets
}

type proxyScanResult struct {
	Port      int              `json:"port"`
	Proxies   []proxyCandidate `json:"proxies"`
	Scanned   int              `json:"scanned"`
	ScannedAt time.Time        `json:"scanned_at"`
}

type proxyTarget struct{ name, host, source string }

// Seams for tests: the targets to probe and the port they are probed on.
var (
	proxyScanTargets = defaultProxyScanTargets
	proxyScanPortFn  = func() int { return proxyScanPort }
)

var proxyScanCache struct {
	sync.Mutex
	res *proxyScanResult
}

func defaultProxyScanTargets(ctx context.Context) []proxyTarget {
	out := []proxyTarget{{name: "this machine", host: "127.0.0.1", source: "local"}}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return out
	}
	var st struct {
		Self *tsPeer
		Peer map[string]*tsPeer
	}
	if json.Unmarshal(raw, &st) != nil {
		return out
	}
	if st.Self != nil {
		if ip := st.Self.ipv4(); ip != "" {
			out = append(out, proxyTarget{name: st.Self.name(), host: ip, source: "local"})
		}
	}
	for _, p := range st.Peer {
		if p == nil || !p.Online {
			continue
		}
		if ip := p.ipv4(); ip != "" {
			out = append(out, proxyTarget{name: p.name(), host: ip, source: "tailnet"})
		}
	}
	return out
}

type tsPeer struct {
	HostName     string
	DNSName      string
	TailscaleIPs []string
	Online       bool
}

func (p *tsPeer) name() string {
	if d := strings.SplitN(p.DNSName, ".", 2)[0]; d != "" {
		return d
	}
	return p.HostName
}

func (p *tsPeer) ipv4() string {
	for _, ip := range p.TailscaleIPs {
		if a := net.ParseIP(ip); a != nil && a.To4() != nil {
			return ip
		}
	}
	return ""
}

// probeSOCKS5 sends the SOCKS5 greeting offering "no auth" and "user/pass" and
// accepts any reply whose version byte is 5. A server that needs a method we
// did not offer still answers 0x05 0xFF, and it is still a SOCKS5 proxy.
func probeSOCKS5(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: proxyProbeTimeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(proxyProbeTimeout))
	if _, err := c.Write([]byte{0x05, 0x02, 0x00, 0x02}); err != nil {
		return false
	}
	var buf [2]byte
	if _, err := c.Read(buf[:]); err != nil {
		return false
	}
	return buf[0] == 0x05
}

func scanSOCKSProxies(ctx context.Context) *proxyScanResult {
	port := proxyScanPortFn()
	targets := proxyScanTargets(ctx)
	var (
		mu    sync.Mutex
		found []proxyCandidate
		wg    sync.WaitGroup
	)
	for _, t := range targets {
		wg.Add(1)
		go func(t proxyTarget) {
			defer wg.Done()
			addr := net.JoinHostPort(t.host, strconv.Itoa(port))
			if !probeSOCKS5(ctx, addr) {
				return
			}
			mu.Lock()
			found = append(found, proxyCandidate{Name: t.name, Addr: addr, Source: t.source, URL: "socks5://" + addr})
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	// Local first, then by name, so the list does not reshuffle between scans.
	sort.Slice(found, func(i, j int) bool {
		if found[i].Source != found[j].Source {
			return found[i].Source == "local"
		}
		if found[i].Name != found[j].Name {
			return found[i].Name < found[j].Name
		}
		return found[i].Addr < found[j].Addr
	})
	if found == nil {
		found = []proxyCandidate{}
	}
	return &proxyScanResult{Port: port, Proxies: found, Scanned: len(targets), ScannedAt: time.Now().UTC()}
}

func serveBrowserProxyScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Held across the scan so concurrent openers share one probe round.
	proxyScanCache.Lock()
	defer proxyScanCache.Unlock()
	res := proxyScanCache.res
	if res == nil || r.URL.Query().Get("refresh") != "" || time.Since(res.ScannedAt) > proxyScanTTL {
		res = scanSOCKSProxies(r.Context())
		proxyScanCache.res = res
	}
	writeJSON(w, res)
}
