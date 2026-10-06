package main

import (
	"context"
	"net"
	"strconv"
	"testing"
)

// listenOn serves reply to every connection on 127.0.0.1:port (after reading
// the greeting) and returns that port.
func listenOn(t *testing.T, ip string, port int, reply []byte) {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 8)
				_, _ = c.Read(buf)
				_, _ = c.Write(reply)
			}()
		}
	}()
}

func TestScanSOCKSProxiesOnlyCountsSOCKS5(t *testing.T) {
	// One free port, used on three loopback addresses: a SOCKS5 server, an
	// HTTP-ish server on the same port, and nothing at all.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	listenOn(t, "127.0.0.1", port, []byte{0x05, 0x00})
	listenOn(t, "127.0.0.2", port, []byte("HTTP/1.1 400 Bad Request\r\n\r\n"))

	oldT, oldP := proxyScanTargets, proxyScanPortFn
	t.Cleanup(func() { proxyScanTargets, proxyScanPortFn = oldT, oldP })
	proxyScanPortFn = func() int { return port }
	proxyScanTargets = func(context.Context) []proxyTarget {
		return []proxyTarget{
			{name: "peer-http", host: "127.0.0.2", source: "tailnet"},
			{name: "this machine", host: "127.0.0.1", source: "local"},
			{name: "peer-closed", host: "127.0.0.3", source: "tailnet"},
		}
	}

	res := scanSOCKSProxies(context.Background())
	if res.Scanned != 3 || res.Port != port {
		t.Fatalf("scanned %d on %d, want 3 on %d", res.Scanned, res.Port, port)
	}
	if len(res.Proxies) != 1 {
		t.Fatalf("got %+v, want only the SOCKS5 server", res.Proxies)
	}
	want := "socks5://127.0.0.1:" + strconv.Itoa(port)
	if p := res.Proxies[0]; p.URL != want || p.Source != "local" || p.Name != "this machine" {
		t.Fatalf("got %+v, want %s from this machine", p, want)
	}
}
