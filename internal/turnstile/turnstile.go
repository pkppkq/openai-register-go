// Package turnstile ports app.py's solve_turnstile_token (app.py:179-263): the
// client for the OPTIONAL, LOCAL turnstile_solver HTTP service the user runs
// themselves (TURNSTILE_SOLVER_DEFAULT_URL = "http://127.0.0.1:8888",
// app.py:139; the UI exposes it as the 启用 / URL pair at app.py:13414-13416).
//
// It is the concrete implementation of authproto's TurnstileSolver injection
// point. SolveToken has exactly that signature, so the whole wiring is:
//
//	authproto.Options{
//	    TurnstileSolverEnabled: s.TurnstileSolverEnabled, // app.py:15794
//	    TurnstileSolverURL:     s.TurnstileSolverURL,     // app.py:15795
//	    TurnstileSolver:        turnstile.SolveToken,
//	}
//
// # Protocol
//
// Unchanged from the Python, which targets the cloudflare-bypass upstream
// turnstile_solver_src:
//
//	POST {base}/v1/leases              -> a token inline, or a lease id
//	POST {base}/v1/solve               -> synchronous fallback when there is no lease id
//	POST {base}/v1/leases/{id}/consume -> polled once a second until the deadline
//	GET  {base}/v1/leases/{id}         -> per-iteration fallback for the above
//
// # Soft failure is the contract
//
// Every failure — solver offline, HTTP >= 400, unparseable body, dead lease,
// deadline — yields "" and nothing else. app.py:8146-8150 then logs
// "Turnstile solver 未返回 token（离线/超时）" and the auth flow continues without
// a token. Nothing in this package returns a non-nil error, because the Python
// had no error channel at all: it wrapped every call in `except: return ""`.
//
// # Why not internal/tlsclient
//
// The Python used plain `requests` here, NOT the curl_cffi Chrome-impersonating
// session it uses for auth.openai.com: the peer is a localhost service that
// does no TLS fingerprinting. net/http is the faithful equivalent.
package turnstile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pkppkq/openai-register-go/internal/openai"
)

// DefaultSolverURL is TURNSTILE_SOLVER_DEFAULT_URL (app.py:139).
const DefaultSolverURL = openai.TurnstileSolverDefaultURL

const (
	// maxCreateTimeout is the `min(30.0, timeout)` cap on POST /v1/leases
	// (app.py:208).
	maxCreateTimeout = 30 * time.Second
	// pollRequestTimeout is the hard-coded `timeout=15` of both polling calls
	// (app.py:242, app.py:253).
	pollRequestTimeout = 15 * time.Second
	// minPollWindow is the `max(5.0, ...)` floor — and the `timeout or 5`
	// fallback — of the polling deadline (app.py:238).
	minPollWindow = 5 * time.Second
	// pollInterval is `time.sleep(1.0)` (app.py:262).
	pollInterval = 1 * time.Second
)

// deadLeaseStatuses is the `{"failed", "error", "expired", "dead"}` set literal
// at app.py:250. Membership only; the iteration order is never observed.
var deadLeaseStatuses = map[string]bool{
	"failed":  true,
	"error":   true,
	"expired": true,
	"dead":    true,
}

// Doer is the slice of *http.Client this package uses, so a caller (or a test)
// can supply its own transport.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Request is solve_turnstile_token's parameter list (app.py:179-187).
//
// Action and CData are keyword-only in Python and BOTH call sites leave them at
// "" (app.py:8140-8145 passes only sitekey, page_url, solver_url and timeout),
// so authproto's TurnstileSolver callback cannot reach them either. They are
// kept because the payload builder they feed is part of the ported function.
type Request struct {
	Sitekey string
	PageURL string
	// SolverURL is `solver_url`; "" selects DefaultSolverURL (app.py:194).
	SolverURL string
	Action    string
	CData     string
	// Timeout is `timeout: float = 120.0`. It caps POST /v1/leases at 30s, is
	// the whole timeout of POST /v1/solve, and sets the polling window.
	Timeout time.Duration
}

// Client talks to one turnstile_solver instance.
type Client struct {
	http Doer
	// test seams — production values are sleepCtx and time.Now.
	sleep func(ctx context.Context, d time.Duration)
	now   func() time.Time
}

// New builds a Client over net/http.
func New() *Client {
	// requests runs with trust_env=True, i.e. it honours HTTP_PROXY /
	// HTTPS_PROXY / NO_PROXY — which is what http.DefaultTransport's
	// ProxyFromEnvironment does.
	//
	// DIVERGENCE: Go's ProxyFromEnvironment NEVER proxies a loopback host,
	// while requests will if HTTP_PROXY is set and NO_PROXY does not exclude
	// it. Since the solver is normally 127.0.0.1, Go's behaviour is the one the
	// user wants; a proxied localhost solver was never reachable in practice.
	return NewWithClient(&http.Client{})
}

// NewWithClient builds a Client over a caller-supplied HTTP doer.
func NewWithClient(doer Doer) *Client {
	return &Client{http: doer, sleep: sleepCtx, now: time.Now}
}

// defaultClient backs the package-level SolveToken.
var defaultClient = New()

// SolveToken has the exact signature of authproto.TurnstileSolver, so it can be
// assigned straight into authproto.Options.TurnstileSolver.
//
// The error is ALWAYS nil: solve_turnstile_token swallowed every failure and
// returned "". The result is the token, or "" for "no token this time".
func SolveToken(sitekey, pageURL, solverURL string, timeout time.Duration) (string, error) {
	return defaultClient.SolveToken(sitekey, pageURL, solverURL, timeout)
}

// SolveToken is the authproto.TurnstileSolver adapter for one Client. The error
// is always nil; see the package-level SolveToken.
func (c *Client) SolveToken(sitekey, pageURL, solverURL string, timeout time.Duration) (string, error) {
	return c.Solve(context.Background(), Request{
		Sitekey:   sitekey,
		PageURL:   pageURL,
		SolverURL: solverURL,
		Timeout:   timeout,
	}), nil
}

// ---------------------------------------------------------------------------
// solve_turnstile_token (app.py:179-263)
// ---------------------------------------------------------------------------

// Solve mirrors solve_turnstile_token. It returns the token string, or "" on
// soft failure (offline / timeout / bad response).
func (c *Client) Solve(ctx context.Context, req Request) string {
	if ctx == nil {
		ctx = context.Background()
	}

	// app.py:194 — `str(solver_url or DEFAULT).rstrip("/")`. rstrip strips EVERY
	// trailing slash, not just one, and an empty (not merely blank) URL falls
	// back to the default.
	base := req.SolverURL
	if base == "" {
		base = DefaultSolverURL
	}
	base = strings.TrimRight(base, "/")

	// app.py:195-198
	sitekey := pyStrip(req.Sitekey)
	pageURL := pyStrip(req.PageURL)
	if sitekey == "" || pageURL == "" {
		return ""
	}

	// app.py:199-206. The dict literal's order is sitekey, url, action, cdata
	// and the drop-nulls comprehension preserves it, so that is the wire order
	// of the JSON body. `str(x or "").strip() or None` plus the
	// `is not None and != ""` filter reduce to "omit when blank".
	payload := []kv{{"sitekey", sitekey}, {"url", pageURL}}
	if action := pyStrip(req.Action); action != "" {
		payload = append(payload, kv{"action", action})
	}
	if cdata := pyStrip(req.CData); cdata != "" {
		payload = append(payload, kv{"cdata", cdata})
	}
	body := encodePayload(payload)

	// app.py:207-213. The try covers the request AND create.json(), so a
	// malformed body is just another "" too.
	createTimeout := req.Timeout
	if createTimeout > maxCreateTimeout { // min(30.0, timeout)
		createTimeout = maxCreateTimeout
	}
	status, raw, err := c.do(ctx, http.MethodPost, base+"/v1/leases", body, createTimeout)
	if err != nil {
		return ""
	}
	if status >= 400 {
		return ""
	}
	created, err := jsonBody(raw)
	if err != nil {
		return ""
	}

	// app.py:215-223. Some solvers answer with the token immediately; otherwise
	// pick up the lease id. A non-dict body leaves lease_id "".
	leaseID := ""
	if obj, ok := asDict(created); ok {
		if token := pyStripStrOr(obj.Get("token"), obj.Get("value")); token != "" {
			return token
		}
		leaseID = pyStripStrOr(obj.Get("lease_id"), obj.Get("id"), obj.Get("leaseId"))
	}

	// app.py:225-236 — no lease id: one synchronous /v1/solve attempt whose
	// result is returned AS IS, empty token included.
	if leaseID == "" {
		status, raw, err := c.do(ctx, http.MethodPost, base+"/v1/solve", body, req.Timeout)
		if err != nil {
			return ""
		}
		if status < 400 {
			data, err := jsonBody(raw)
			if err != nil {
				return ""
			}
			if obj, ok := asDict(data); ok {
				return pyStripStrOr(obj.Get("token"), obj.Get("value"))
			}
		}
		return ""
	}

	// app.py:238 — deadline = time.time() + max(5.0, float(timeout or 5)).
	// A zero timeout means 5s (`timeout or 5`), and so does any value below 5s
	// or negative (max()). The `or 5` half is unreachable in practice: a
	// non-positive timeout already killed the create call above, in Go as in
	// requests (urllib3 refuses timeout=0).
	window := req.Timeout
	if window == 0 {
		window = minPollWindow
	}
	if window < minPollWindow {
		window = minPollWindow
	}
	deadline := c.now().Add(window)

	// app.py:239-262
	for c.now().Before(deadline) {
		// DIVERGENCE: the Python loop had no cancellation — it polled until the
		// deadline no matter what. Honouring a cancelled context here stops the
		// caller's shutdown from waiting out a two-minute window against a
		// solver that can no longer answer.
		if ctx.Err() != nil {
			return ""
		}
		if token, done := c.pollLease(ctx, base, leaseID); done {
			return token
		}
		c.sleep(ctx, pollInterval)
	}
	return ""
}

// pollLease is one iteration of the app.py:239-261 loop. done=true means the
// Python did `return` from inside the loop — with a token, or with "" for a
// lease the solver declared dead.
//
// Both calls sit in ONE try/except (app.py:240-261), so a transport failure of
// the consume POST skips the GET fallback for this iteration and goes straight
// to the sleep.
func (c *Client) pollLease(ctx context.Context, base, leaseID string) (string, bool) {
	// app.py:242-251 — the upstream solver's preferred endpoint. No body: the
	// Python passes neither json= nor data=, so there is no content-type.
	//
	// lease_id is interpolated raw, exactly as the f-string does; it is not
	// path-escaped.
	status, raw, err := c.do(ctx, http.MethodPost, base+"/v1/leases/"+leaseID+"/consume", nil, pollRequestTimeout)
	if err != nil {
		return "", false
	}
	if status < 400 {
		data, err := jsonBody(raw)
		if err != nil {
			return "", false
		}
		if obj, ok := asDict(data); ok {
			if token := pyStripStrOr(obj.Get("token"), obj.Get("value")); token != "" {
				return token, true
			}
			// `str(data.get("status") or "").lower()` — no .strip() on this one.
			// strings.ToLower is SIMPLE case mapping where CPython's str.lower()
			// is full case mapping, but every member of deadLeaseStatuses is
			// ASCII, so no input can land on a different side of the test.
			if deadLeaseStatuses[strings.ToLower(pyStrOr(obj.Get("status")))] {
				return "", true
			}
		}
	}

	// app.py:252-259 — fallback poll GET, same iteration, same try.
	status, raw, err = c.do(ctx, http.MethodGet, base+"/v1/leases/"+leaseID, nil, pollRequestTimeout)
	if err != nil {
		return "", false
	}
	if status < 400 {
		data, err := jsonBody(raw)
		if err != nil {
			return "", false
		}
		if obj, ok := asDict(data); ok {
			if token := pyStripStrOr(obj.Get("token"), obj.Get("value")); token != "" {
				return token, true
			}
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

// do issues one request with a per-request deadline and reads the whole body,
// which is what a non-streaming requests call does.
//
// DIVERGENCE: requests' timeout= is a per-socket-operation (connect / read)
// timeout, while a context deadline covers the WHOLE exchange. A solver that
// answers in dribs and drabs for longer than the timeout is tolerated by
// requests and cut off here. The failure mode is identical either way ("" and
// carry on), only the tolerance differs.
func (c *Client) do(ctx context.Context, method, rawURL string, body []byte, timeout time.Duration) (int, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, rawURL, reader)
	if err != nil {
		// requests raises InvalidURL / MissingSchema here, which the callers'
		// bare `except` turns into "" just like a transport failure.
		return 0, nil, err
	}
	if body != nil {
		// requests' json= sets exactly this one header.
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

// sleepCtx is time.sleep(d), abandoned early when the context is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// ---------------------------------------------------------------------------
// JSON
// ---------------------------------------------------------------------------

type kv struct{ key, value string }

// encodePayload is requests' `json=payload`, i.e. json.dumps(payload) with the
// library defaults: ensure_ascii=True and the DEFAULT separators (", " / ": "),
// not the compact ones.
//
// Hand-rolled rather than encoding/json because Marshal escapes <, > and & (and
// json.Marshal of a map would also reorder the keys, losing the dict order the
// Python literal fixed).
func encodePayload(pairs []kv) []byte {
	var b strings.Builder
	b.WriteByte('{')
	for i, pair := range pairs {
		if i > 0 {
			b.WriteString(", ")
		}
		writeJSONString(&b, pair.key)
		b.WriteString(": ")
		writeJSONString(&b, pair.value)
	}
	b.WriteByte('}')
	return []byte(b.String())
}

// writeJSONString is CPython's py_encode_basestring_ascii: short escapes for
// \\ " \b \f \n \r \t, \uXXXX for every other C0 control AND for every
// non-ASCII rune (ensure_ascii=True), and nothing else — in particular < > &
// are emitted literally.
func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
			continue
		case '\\':
			b.WriteString(`\\`)
			continue
		case '\b':
			b.WriteString(`\b`)
			continue
		case '\f':
			b.WriteString(`\f`)
			continue
		case '\n':
			b.WriteString(`\n`)
			continue
		case '\r':
			b.WriteString(`\r`)
			continue
		case '\t':
			b.WriteString(`\t`)
			continue
		}
		switch {
		case r < 0x20 || r >= 0x7f:
			if r > 0xFFFF {
				r -= 0x10000
				b.WriteString(`\u` + hex4(0xD800+(r>>10)) + `\u` + hex4(0xDC00+(r&0x3FF)))
				continue
			}
			b.WriteString(`\u` + hex4(r))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

func hex4(r rune) string {
	const digits = "0123456789abcdef"
	return string([]byte{
		digits[(r>>12)&0xF],
		digits[(r>>8)&0xF],
		digits[(r>>4)&0xF],
		digits[r&0xF],
	})
}

// jsonBody is `resp.json() if resp.content else {}` (app.py:211, 230, 244, 255).
// An empty body is an empty dict, and a malformed one is an error the caller
// turns into the Python's `except` branch.
//
// openai.DecodeOrderedJSON is json.loads: it keeps object key order, keeps
// numbers as literals, and rejects trailing data the way CPython does.
func jsonBody(raw []byte) (any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	return openai.DecodeOrderedJSON(raw)
}

// ---------------------------------------------------------------------------
// Python value semantics
// ---------------------------------------------------------------------------

// jsonObject is the read side of an insertion-ordered decoded object; the value
// openai.DecodeOrderedJSON returns satisfies it.
type jsonObject interface {
	Get(key string) any
	Keys() []string
}

// asDict is `isinstance(x, dict)`.
func asDict(v any) (jsonObject, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case jsonObject:
		if t == nil {
			return nil, false
		}
		return t, true
	case map[string]any:
		// Only jsonBody's empty-body literal and hand-written test values take
		// this path; a plain map has already lost CPython's insertion order, so
		// it is wrapped in SORTED key order — deterministic, just not the wire
		// order. See the DIVERGENCE note on pyRepr.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return plainObject{vals: t, keys: keys}, true
	default:
		return nil, false
	}
}

type plainObject struct {
	vals map[string]any
	keys []string
}

func (o plainObject) Get(key string) any { return o.vals[key] }
func (o plainObject) Keys() []string     { return o.keys }

// pyTruthy mirrors Python truthiness, the semantics behind every `a or b` here:
// None / "" / 0 / False / [] / {} are falsy, " " and "0" are not.
func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case json.Number:
		// bool(0) / bool(0.0) is False whatever the literal spelling was.
		if i, err := t.Int64(); err == nil {
			return i != 0
		}
		if f, err := t.Float64(); err == nil {
			return f != 0
		}
		return t.String() != ""
	case []any:
		return len(t) > 0
	case jsonObject:
		return t != nil && len(t.Keys()) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// pyStr mirrors str(v) for the shapes json.loads produces.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return pyFloatRepr(t)
	case json.Number:
		// Kept as the literal CPython saw: float64 would render a long id as
		// 1.2345678901234567e+19.
		return t.String()
	default:
		return pyRepr(v)
	}
}

// pyFloatRepr is str(float) — shortest round-trip, and never bare of a ".0".
//
// strconv.FormatFloat(f, 'g', -1, 64) is NOT it. Both pick the shortest digits
// that round-trip, but they disagree on when scientific notation kicks in:
// Go's shortest-%g goes exponential once the decimal point passes position 6,
// CPython's repr only past position 16, so FormatFloat printed 1234567.0 as
// "1.234567e+06" where CPython prints "1234567.0"
// (CPython Python/pystrtod.c: use_exp = decpt <= -4 || decpt > 16).
func pyFloatRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	exp := 0
	if i := strings.IndexByte(sci, 'e'); i >= 0 {
		exp, _ = strconv.Atoi(sci[i+1:])
	}
	if decpt := exp + 1; decpt <= -4 || decpt > 16 {
		return sci
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// pyRepr is str() of a container — what a solver returning
// {"token": {"a": 1}} would produce. Absurd, but `str(...)` accepts it and the
// result would be handed back as the token, so it is spelled out rather than
// left to %v.
//
// DIVERGENCE: for a Go map[string]any the CPython insertion order is already
// gone, so keys are rendered sorted. Bodies decoded by jsonBody keep their real
// order and are unaffected.
func pyRepr(v any) string {
	switch t := v.(type) {
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, pyRepr(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case jsonObject:
		keys := t.Keys()
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, pyStrRepr(k)+": "+pyRepr(t.Get(k)))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case map[string]any:
		obj, _ := asDict(t)
		return pyRepr(obj)
	case string:
		return pyStrRepr(t)
	default:
		return pyStr(v)
	}
}

// pyStrRepr is repr() of a str: CPython prefers single quotes and only switches
// to double quotes when the value contains a ' but no ".
//
// The escape set is EVERY non-printable code point, not just the C0 controls:
// CPython escapes anything whose str.isprintable() is false, i.e. anything in a
// C* or Z* general category except the ASCII space — which is exactly Go's
// unicode.IsPrint. Testing `r < 0x20` alone (as this did) left U+0085, U+00A0,
// U+00AD, U+200B, U+2028, U+2029, U+3000 and every unassigned code point
// unescaped.
//
// The spelling is CPython's: \t \n \r and the active quote get short forms,
// everything else becomes \xXX below U+0100, \uXXXX below U+10000, \UXXXXXXXX
// above. \b, \f and \v are NOT short forms in repr (they are in json.dumps).
func pyStrRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pyStrOr is `str(a or b or ... or "")`: the first truthy operand, str()-ed.
func pyStrOr(values ...any) string {
	for _, v := range values {
		if pyTruthy(v) {
			return pyStr(v)
		}
	}
	return ""
}

// pyStripStrOr is `str(a or b or ... or "").strip()`.
func pyStripStrOr(values ...any) string { return pyStrip(pyStrOr(values...)) }

// pyStrip is str.strip(): Python's whitespace class, which includes the
// C0 separators 0x1c-0x1f that unicode.IsSpace does not.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

func pyIsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0:
		return true
	}
	switch {
	case r >= 0x2000 && r <= 0x200a:
		return true
	case r == 0x1680 || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000:
		return true
	}
	return false
}
