// Package proxychain is a faithful Go port of the Python ProxyChainServer: a
// local HTTP proxy (127.0.0.1:random) that chains client traffic through an
// optional local proxy and then an optional dynamic upstream proxy, supporting
// HTTP CONNECT (with Basic auth) and SOCKS5 (with user/pass auth) upstreams.
//
// This is the egress mechanism both the browser and the HTTP clients use to
// exit via a chosen region. Go's net stack makes this cleaner than the Python
// original; behavior (chaining order, auth, address types) is preserved.
package proxychain

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/idna"

	"github.com/pkppkq/openai-register-go/internal/proxypool"
)

const dialTimeout = 30 * time.Second

// LogFunc receives human-facing status/error lines (may be nil).
type LogFunc func(string)

type Server struct {
	mu           sync.Mutex
	localProxy   string
	dynamicProxy string
	log          LogFunc

	listener   net.Listener
	url        string
	activeConn map[net.Conn]struct{}
	closed     bool

	lastErrText string
	lastErrAt   time.Time
}

// New mirrors ProxyChainServer.__init__ (app.py:5923-5934), which runs
// normalize_proxy_url over BOTH upstreams before storing them. Normalizing here
// rather than trusting the caller is load-bearing twice over:
//
//   - a scheme-less "1.2.3.4:8080" parses as scheme="1.2.3.4" and dialProxy rejects
//     it, so the whole chain dies where Python would have dialled http://1.2.3.4:8080;
//   - normalize_proxy_url rewrites socks5:// to socks5h://, and that scheme is what
//     decides remoteDNS at line 407 — a bare socks5:// would resolve the TARGET
//     hostname on this machine instead of at the proxy, both leaking the lookup and
//     connecting to a locally-resolved address.
func New(localProxy, dynamicProxy string, log LogFunc) *Server {
	if log == nil {
		log = func(string) {}
	}
	return &Server{
		localProxy:   proxypool.NormalizeProxyURL(localProxy),
		dynamicProxy: proxypool.NormalizeProxyURL(dynamicProxy),
		log:          log,
		activeConn:   map[net.Conn]struct{}{},
	}
}

// URL returns the local proxy URL (empty if no chaining is configured).
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

// Start begins listening. If neither proxy is set it is a no-op (URL stays "").
func (s *Server) Start() error {
	s.mu.Lock()
	if s.localProxy == "" && s.dynamicProxy == "" {
		s.mu.Unlock()
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.listener = ln
	s.url = "http://" + ln.Addr().String()
	s.mu.Unlock()

	go s.serve(ln)
	return nil
}

func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	ln := s.listener
	s.listener = nil
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

// SetDynamicProxy swaps the upstream dynamic proxy and drops in-flight conns so
// new requests re-chain through the new exit.
func (s *Server) SetDynamicProxy(dynamicProxy string) {
	s.mu.Lock()
	// Python normalizes here too (app.py:5963), for the same reasons as New.
	s.dynamicProxy = proxypool.NormalizeProxyURL(dynamicProxy)
	conns := make([]net.Conn, 0, len(s.activeConn))
	for c := range s.activeConn {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *Server) track(c net.Conn) {
	s.mu.Lock()
	s.activeConn[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.activeConn, c)
	s.mu.Unlock()
}

func (s *Server) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(client net.Conn) {
	var upstream net.Conn
	tunnelEstablished := false
	s.track(client)
	defer func() {
		s.untrack(client)
		if upstream != nil {
			s.untrack(upstream)
		}
		_ = client.Close()
	}()

	_ = client.SetReadDeadline(time.Now().Add(dialTimeout))
	head, err := readHTTPHead(client)
	if err != nil || len(head) == 0 {
		return
	}
	_ = client.SetReadDeadline(time.Time{})

	firstLine, _, _ := strings.Cut(string(head), "\r\n")
	parts := strings.Fields(firstLine)
	if len(parts) < 3 {
		return
	}
	method, target, version := strings.ToUpper(parts[0]), parts[1], parts[2]

	if method == "CONNECT" {
		upstream, err = s.openChain(target)
		if err != nil {
			s.reportChainError(client, err)
			return
		}
		s.track(upstream)
		if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			_ = upstream.Close()
			return
		}
		tunnelEstablished = true
		relay(client, upstream)
		return
	}

	rewritten := rewritePlainRequest(head, method, target, version)
	upstream, err = s.openChain(targetFromPlainRequest(target, head))
	if err != nil {
		s.reportChainError(client, err)
		return
	}
	s.track(upstream)
	if _, err := upstream.Write(rewritten); err != nil {
		_ = upstream.Close()
		return
	}
	tunnelEstablished = true
	relay(client, upstream)
	_ = tunnelEstablished
}

func (s *Server) reportChainError(client net.Conn, err error) {
	text := maskProxyURL(err.Error())
	now := time.Now()
	s.mu.Lock()
	shouldLog := text != s.lastErrText || now.Sub(s.lastErrAt) >= 5*time.Second
	if shouldLog {
		s.lastErrText = text
		s.lastErrAt = now
	}
	s.mu.Unlock()
	if shouldLog {
		s.log("链式代理建立失败: " + text)
	}
	_, _ = client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
}

// openChain establishes a connection to target through local -> dynamic -> target.
func (s *Server) openChain(target string) (net.Conn, error) {
	s.mu.Lock()
	localProxy, dynamicProxy := s.localProxy, s.dynamicProxy
	s.mu.Unlock()

	if localProxy != "" {
		var conn net.Conn
		var err error
		firstHop := target
		if dynamicProxy != "" {
			firstHop, err = proxyConnectTarget(dynamicProxy)
			if err != nil {
				return nil, fmt.Errorf("本地代理连接动态代理入口失败: %w", err)
			}
		}
		if isSocks(localProxy) {
			conn, err = dialSocks5(localProxy, firstHop)
		} else {
			conn, err = dialProxy(localProxy)
			if err == nil {
				err = sendHTTPConnect(conn, firstHop, localProxy)
			}
		}
		if err != nil {
			dest := target
			if dynamicProxy != "" {
				dest = "动态代理入口"
			}
			return nil, fmt.Errorf("本地代理连接%s失败: %w", dest, err)
		}
		if dynamicProxy != "" {
			if isSocks(dynamicProxy) {
				err = sendSocks5Connect(conn, dynamicProxy, target)
			} else {
				err = sendHTTPConnect(conn, target, dynamicProxy)
			}
			if err != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("动态代理连接 %s 失败: %w", target, err)
			}
		}
		return conn, nil
	}

	if dynamicProxy != "" {
		if isSocks(dynamicProxy) {
			return dialSocks5(dynamicProxy, target)
		}
		conn, err := dialProxy(dynamicProxy)
		if err != nil {
			return nil, err
		}
		if err := sendHTTPConnect(conn, target, dynamicProxy); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}

	host, port, err := splitHostPort(target, 80)
	if err != nil {
		return nil, err
	}
	return net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), dialTimeout)
}

// ---------------------------------------------------------------------------
// upstream connection primitives
// ---------------------------------------------------------------------------

// isSocks is _is_socks_proxy (app.py:6111). urlsplit already lowercased the
// scheme, so Python's extra .lower() is a no-op.
func isSocks(proxyURL string) bool {
	s := proxypool.ParseURL(proxyURL).Scheme()
	return s == "socks5" || s == "socks5h"
}

// defaultPort is Python's `parsed.port or (443 if scheme == "https" else 80)`.
func defaultPort(scheme string) int {
	if scheme == "https" {
		return 443
	}
	return 80
}

// dialProxy is _connect_proxy (app.py:6114-6125).
func dialProxy(proxyURL string) (net.Conn, error) {
	u := proxypool.ParseURL(proxyURL)
	scheme := u.Scheme()
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("链式代理当前只支持 http/https/socks5/socks5h 代理: %s", proxyURL)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: %s", proxypool.ErrNoHost, proxyURL)
	}
	port, err := u.PortOr(defaultPort(scheme))
	if err != nil {
		return nil, err
	}
	raw, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), dialTimeout)
	if err != nil {
		return nil, err
	}
	if scheme == "https" {
		return tls.Client(raw, &tls.Config{ServerName: host}), nil
	}
	return raw, nil
}

// proxyConnectTarget is _proxy_connect_target (app.py:6127-6131).
func proxyConnectTarget(proxyURL string) (string, error) {
	u := proxypool.ParseURL(proxyURL)
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("动态代理地址缺少 host: %s", proxyURL)
	}
	port, err := u.PortOr(defaultPort(u.Scheme()))
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// dialSocks5 is _connect_socks5 (app.py:6133-6149).
func dialSocks5(proxyURL, target string) (net.Conn, error) {
	u := proxypool.ParseURL(proxyURL)
	if scheme := u.Scheme(); scheme != "socks5" && scheme != "socks5h" {
		return nil, fmt.Errorf("不是 SOCKS5 代理: %s", proxyURL)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: %s", proxypool.ErrNoHost, proxyURL)
	}
	port, err := u.PortOr(1080)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), dialTimeout)
	if err != nil {
		return nil, err
	}
	if err := sendSocks5Connect(conn, proxyURL, target); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// sendSocks5Connect is _send_socks5_connect (app.py:6151-6187).
func sendSocks5Connect(conn net.Conn, proxyURL, target string) error {
	u := proxypool.ParseURL(proxyURL)
	username, password := u.Credentials()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	defer conn.SetDeadline(time.Time{})

	methods := []byte{0x00}
	if username != "" {
		methods = []byte{0x00, 0x02}
	}
	if _, err := conn.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	sel, err := readExact(conn, 2)
	if err != nil {
		return err
	}
	if sel[0] != 0x05 || sel[1] == 0xFF {
		return errors.New("SOCKS5 代理不接受认证方式")
	}
	if sel[1] == 0x02 {
		ub, pb := []byte(username), []byte(password)
		if len(ub) > 255 || len(pb) > 255 {
			return errors.New("SOCKS5 用户名或密码过长")
		}
		auth := []byte{0x01, byte(len(ub))}
		auth = append(auth, ub...)
		auth = append(auth, byte(len(pb)))
		auth = append(auth, pb...)
		if _, err := conn.Write(auth); err != nil {
			return err
		}
		ar, err := readExact(conn, 2)
		if err != nil {
			return err
		}
		if ar[0] != 0x01 || ar[1] != 0x00 {
			return errors.New("SOCKS5 用户名或密码认证失败")
		}
	}

	host, port, err := splitHostPort(target, 443)
	if err != nil {
		return err
	}
	if port < 0 || port > 65535 {
		// int(port).to_bytes(2, "big") is an OverflowError in app.py:6180.
		return fmt.Errorf("SOCKS5 目标端口越界: %d", port)
	}
	addr, err := socks5AddressBytes(host, u.Scheme() == "socks5h")
	if err != nil {
		return err
	}
	req := append([]byte{0x05, 0x01, 0x00}, addr...)
	req = append(req, byte(port>>8), byte(port&0xFF))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	head, err := readExact(conn, 4)
	if err != nil {
		return err
	}
	if head[0] != 0x05 || head[1] != 0x00 {
		return fmt.Errorf("SOCKS5 CONNECT 失败: 0x%02x", head[1])
	}
	switch head[3] {
	case 0x01:
		_, err = readExact(conn, 4)
	case 0x03:
		n, e := readExact(conn, 1)
		if e != nil {
			return e
		}
		_, err = readExact(conn, int(n[0]))
	case 0x04:
		_, err = readExact(conn, 16)
	default:
		return fmt.Errorf("SOCKS5 返回了未知地址类型: %d", head[3])
	}
	if err != nil {
		return err
	}
	_, err = readExact(conn, 2)
	return err
}

// socks5AddressBytes is _socks5_address_bytes (app.py:6189-6203): the SOCKS5
// ATYP + address for the CONNECT request.
func socks5AddressBytes(host string, remoteDNS bool) ([]byte, error) {
	if !remoteDNS {
		// socket.gethostbyname resolves A records ONLY, and leaves the hostname
		// untouched when it fails. net.LookupIP returns AAAA too, so taking
		// ips[0] could put an IPv6 literal in a socks5:// (local-DNS) request
		// that the caller explicitly asked to resolve as IPv4.
		if ips, err := net.LookupIP(host); err == nil {
			for _, ip := range ips {
				if v4 := ip.To4(); v4 != nil {
					host = v4.String()
					break
				}
			}
		}
	}
	// inet_aton first, then inet_pton(AF_INET6) — the order matters:
	// "::ffff:1.2.3.4" fails inet_aton and goes out as a 16-byte ATYP 0x04
	// address, where a To4()-first test would have sent 4 bytes as ATYP 0x01.
	if ip, err := netip.ParseAddr(host); err == nil && ip.Zone() == "" {
		if ip.Is4() {
			v4 := ip.As4()
			return append([]byte{0x01}, v4[:]...), nil
		}
		v6 := ip.As16()
		return append([]byte{0x04}, v6[:]...), nil
	}
	hb, err := idnaHostBytes(host)
	if err != nil {
		return nil, err
	}
	if len(hb) > 255 {
		// app.py raises here. Truncating instead would send a SHORTER hostname
		// that still resolves somewhere — a silent connection to the wrong
		// origin is strictly worse than a failed chain.
		return nil, errors.New("SOCKS5 目标域名过长")
	}
	return append([]byte{0x03, byte(len(hb))}, hb...), nil
}

// idnaHostBytes is host.encode("idna").
//
// DIVERGENCE: Python's built-in "idna" codec is IDNA 2003 with nameprep and it
// also rejects an empty or >63-character label even for a pure-ASCII host.
// This passes ASCII through untouched (which is what the codec does for every
// hostname this app actually dials) and only reaches for x/net/idna when the
// host is non-ASCII, where the two differ on ß/ς/ZWJ but agree on everything a
// SOCKS5 CONNECT would plausibly carry.
func idnaHostBytes(host string) ([]byte, error) {
	ascii := true
	for i := 0; i < len(host); i++ {
		if host[i] > 0x7f {
			ascii = false
			break
		}
	}
	if ascii {
		return []byte(host), nil
	}
	encoded, err := idna.ToASCII(host)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 目标域名无法编码: %w", err)
	}
	return []byte(encoded), nil
}

func sendHTTPConnect(conn net.Conn, target, proxyURL string) error {
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	defer conn.SetDeadline(time.Time{})
	headers := []string{
		"CONNECT " + target + " HTTP/1.1",
		"Host: " + target,
		"Proxy-Connection: keep-alive",
	}
	if auth := proxyAuth(proxyURL); auth != "" {
		headers = append(headers, "Proxy-Authorization: Basic "+auth)
	}
	if _, err := conn.Write([]byte(strings.Join(headers, "\r\n") + "\r\n\r\n")); err != nil {
		return err
	}
	resp, err := readHTTPHead(conn)
	if err != nil {
		return err
	}
	status, _, _ := strings.Cut(string(resp), "\r\n")
	if !strings.Contains(" "+status+" ", " 200 ") {
		return fmt.Errorf("代理 CONNECT 失败: %s", status)
	}
	return nil
}

// proxyAuth is _proxy_auth (app.py:6229-6235). The gate is `parsed.username`
// being truthy, so "http://:pw@h:1" sends NO Proxy-Authorization at all.
func proxyAuth(proxyURL string) string {
	u := proxypool.ParseURL(proxyURL)
	if !u.HasUsername() {
		return ""
	}
	username, password := u.Credentials()
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

// ---------------------------------------------------------------------------
// HTTP head parsing / rewriting
// ---------------------------------------------------------------------------

func readHTTPHead(conn net.Conn) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for !containsCRLFCRLF(buf) && len(buf) < 65536 {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return buf, err
		}
	}
	return buf, nil
}

func containsCRLFCRLF(b []byte) bool {
	return strings.Contains(string(b), "\r\n\r\n")
}

// targetFromPlainRequest is _target_from_plain_request (app.py:6055-6064).
//
// Python builds f"{parsed.hostname}:{port}" by hand, so an IPv6 host arrives
// UNBRACKETED ("::1:8080") and _split_host_port's rsplit(":", 1) puts it back
// together correctly. net.JoinHostPort would bracket it, and the CONNECT line
// sent upstream would then differ from app.py's.
func targetFromPlainRequest(target string, head []byte) string {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		u := proxypool.ParseURL(target)
		port, err := u.PortOr(defaultPort(u.Scheme()))
		if err == nil {
			return u.Hostname() + ":" + strconv.Itoa(port)
		}
		// Python would have raised out of _handle_client here; fall back to the
		// Host: header rather than kill the connection over a malformed port.
	}
	for _, line := range strings.Split(string(head), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "host:") {
			_, v, _ := strings.Cut(line, ":")
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// rewritePlainRequest is _rewrite_plain_request (app.py:6066-6075): turn the
// absolute-form request line a proxy client sends into the origin form.
//
// The path is urlparse's RAW path — not net/url's decoded URL.Path, which would
// rewrite "GET /a%2Fb" as "GET /a/b" and ask the origin for a different
// resource — and, like urlparse, it drops a ";params" tail.
func rewritePlainRequest(head []byte, method, target, version string) []byte {
	if !(strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://")) {
		return head
	}
	u := proxypool.ParseURL(target)
	path := u.Path()
	if path == "" {
		path = "/"
	}
	if q := u.Query(); q != "" {
		path += "?" + q
	}
	lines := strings.SplitN(string(head), "\r\n", 2)
	rest := ""
	if len(lines) == 2 {
		rest = "\r\n" + lines[1]
	}
	return []byte(method + " " + path + " " + version + rest)
}

// ---------------------------------------------------------------------------
// misc
// ---------------------------------------------------------------------------

// splitHostPort is _split_host_port (app.py:6239-6247).
//
// Every failure Python raises is returned as an error rather than swallowed.
// Falling back to defaultPort on an unparsable port, which is what a
// `if err == nil` guard does, is the dangerous shape here: "[::1]:abc" would
// then CONNECT to ::1 on 443 — a real connection to an origin nobody asked
// for — and "[::1" (no closing bracket, a ValueError in Python) would dial the
// host "[:" on port 1.
func splitHostPort(target string, defaultPort int) (string, int, error) {
	if strings.HasPrefix(target, "[") {
		i := strings.Index(target, "]")
		if i < 0 {
			// target[1:].split("]", 1) with no "]" is a ValueError.
			return "", 0, fmt.Errorf("目标地址缺少 ]: %s", target)
		}
		host, rest := target[1:i], target[i+1:]
		if strings.HasPrefix(rest, ":") {
			port, err := pyInt(rest[1:])
			if err != nil {
				return "", 0, err
			}
			return host, port, nil
		}
		return host, defaultPort, nil
	}
	if i := strings.LastIndex(target, ":"); i >= 0 {
		port, err := pyInt(target[i+1:])
		if err != nil {
			return "", 0, err
		}
		return target[:i], port, nil
	}
	return target, defaultPort, nil
}

// pyInt is int(str) for the surface a CONNECT target can carry: surrounding
// whitespace is stripped, a leading sign is allowed and underscores may sit
// between digits (int("3_010") == 3010, which strconv.Atoi rejects).
//
// DIVERGENCE: Python also accepts Unicode decimal digits, so int("٨٠") is 80.
// Go has no exported Nd-to-value mapping and the port here comes from a local
// client's request line, so those are reported as an error instead. The
// direction is safe: the chain fails where app.py would have dialled.
func pyInt(text string) (int, error) {
	s := strings.TrimFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
	})
	neg := false
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" || strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") ||
		strings.Contains(s, "__") {
		return 0, fmt.Errorf("invalid literal for int(): %q", text)
	}
	digits := strings.ReplaceAll(s, "_", "")
	value, err := strconv.Atoi(digits)
	if err != nil {
		return 0, fmt.Errorf("invalid literal for int(): %q", text)
	}
	if neg {
		value = -value
	}
	return value, nil
}

func readExact(conn net.Conn, n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func relay(left, right net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(right, left)
	go cp(left, right)
	<-done
	_ = left.Close()
	_ = right.Close()
	<-done
}

// maskProxyURL is app.py:6027 — the SAME mask_proxy_url the rest of the app
// uses, applied to the raw exception text. It is not a bespoke error-string
// masker: an error message is not a URL, so urlsplit finds no userinfo and the
// regex branch runs, replacing EVERY "://user:pass@" in the line with
// "://***@". A "first match only" or "***:***@" variant would leak the second
// hop's credentials whenever a message names both proxies.
func maskProxyURL(s string) string { return proxypool.MaskProxyURL(s) }
