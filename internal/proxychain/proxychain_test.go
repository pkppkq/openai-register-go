package proxychain

import (
	"net"
	"strings"
	"testing"
)

// ProxyChainServer.__init__ (app.py:5923) normalizes both upstreams. Skipping that
// is not cosmetic — see the comment on New for the two ways it breaks.
func TestNewNormalizesUpstreams(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"scheme-less host:port gets the default scheme", "1.2.3.4:8080", "http://1.2.3.4:8080"},
		// The important one: socks5h is what makes socks5AddressBytes resolve the
		// target AT the proxy instead of on this machine.
		{"socks5 is rewritten to socks5h", "socks5://1.2.3.4:1080", "socks5h://1.2.3.4:1080"},
		{"socks5h is left alone", "socks5h://1.2.3.4:1080", "socks5h://1.2.3.4:1080"},
		{"http passes through", "http://1.2.3.4:8080", "http://1.2.3.4:8080"},
		{"surrounding whitespace is stripped", "  http://1.2.3.4:8080  ", "http://1.2.3.4:8080"},
		{"blank stays blank", "   ", ""},
	} {
		s := New(tc.in, tc.in, nil)
		if s.localProxy != tc.want || s.dynamicProxy != tc.want {
			t.Errorf("%s: New(%q) -> local=%q dynamic=%q, want %q",
				tc.name, tc.in, s.localProxy, s.dynamicProxy, tc.want)
		}
		s.SetDynamicProxy(tc.in)
		if s.dynamicProxy != tc.want {
			t.Errorf("%s: SetDynamicProxy(%q) = %q, want %q", tc.name, tc.in, s.dynamicProxy, tc.want)
		}
	}
}

// __enter__ (app.py:5937) returns without binding when both upstreams are empty, so
// url stays "" and callers fall through to a direct connection. The check must run
// on the NORMALIZED values, or input that normalizes away still starts a listener
// that can never dial.
func TestStartIsNoOpWithoutUpstreams(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		s := New(in, in, nil)
		if err := s.Start(); err != nil {
			t.Fatalf("Start(%q): %v", in, err)
		}
		defer s.Close()
		if got := s.URL(); got != "" {
			t.Errorf("Start(%q) bound %q, want no listener", in, got)
		}
	}
}

func TestStartBindsLoopbackAndCloses(t *testing.T) {
	s := New("http://127.0.0.1:1", "", nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	url := s.URL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("URL = %q, want a loopback http URL", url)
	}
	// Reachable while open...
	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatalf("dial while open: %v", err)
	}
	_ = conn.Close()

	// ...and refused after Close, so a job that ends cannot leave a live listener
	// chained to a proxy the next job did not choose.
	s.Close()
	if conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://")); err == nil {
		_ = conn.Close()
		t.Error("listener still accepting after Close")
	}
}

// dialProxy must reject what it cannot chain rather than silently connecting
// somewhere else; the message is user-facing (app.py's equivalent raise).
func TestDialProxyRejectsUnsupportedScheme(t *testing.T) {
	if _, err := dialProxy("ftp://1.2.3.4:21"); err == nil {
		t.Fatal("ftp:// should be rejected")
	} else if !strings.Contains(err.Error(), "只支持") {
		t.Errorf("error = %v, want the Chinese unsupported-scheme message", err)
	}
	if _, err := dialProxy("http://"); err == nil {
		t.Fatal("a host-less proxy URL should be rejected")
	}
}

// The socks5h scheme is the only thing that keeps the target hostname off this
// machine's resolver, so pin the decision itself.
func TestSocksSchemeDetection(t *testing.T) {
	for _, tc := range []struct {
		url   string
		socks bool
	}{
		{"socks5://1.2.3.4:1080", true},
		{"socks5h://1.2.3.4:1080", true},
		{"http://1.2.3.4:8080", false},
		{"https://1.2.3.4:8443", false},
	} {
		if got := isSocks(tc.url); got != tc.socks {
			t.Errorf("isSocks(%q) = %v, want %v", tc.url, got, tc.socks)
		}
	}
	// A literal IP needs no lookup either way, so this asserts the encoding, not DNS.
	for _, tc := range []struct {
		host string
		atyp byte
		size int
	}{
		{"1.2.3.4", 0x01, 5},
		// inet_aton rejects an IPv4-mapped literal, so app.py:6196-6199 falls
		// through to inet_pton and sends 16 bytes. A To4()-first test would
		// have sent 4 and pointed the CONNECT at a different address family.
		{"::ffff:1.2.3.4", 0x04, 17},
		{"::1", 0x04, 17},
		{"example.com", 0x03, 1 + 1 + len("example.com")},
	} {
		got, err := socks5AddressBytes(tc.host, true)
		if err != nil {
			t.Fatalf("socks5AddressBytes(%q): %v", tc.host, err)
		}
		if got[0] != tc.atyp || len(got) != tc.size {
			t.Errorf("socks5AddressBytes(%q) = atyp %#x len %d, want %#x/%d",
				tc.host, got[0], len(got), tc.atyp, tc.size)
		}
	}
	// app.py raises 目标域名过长 rather than truncating: a truncated hostname
	// still resolves somewhere, and that somewhere is not the requested origin.
	if _, err := socks5AddressBytes(strings.Repeat("a", 300)+".com", true); err == nil {
		t.Error("an over-long hostname must be an error, not a truncated name")
	}
}
