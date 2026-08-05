package turnstile

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/authproto"
)

// ---------------------------------------------------------------------------
// Fake solver
//
// NO TEST IN THIS PACKAGE MAY OPEN A SOCKET, let alone reach a real
// turnstile_solver: the solver is a service the USER runs, and a test that
// depends on a service being up is not a test. Everything below runs against
// this in-memory Doer.
// ---------------------------------------------------------------------------

type call struct {
	method      string
	url         string
	body        string
	contentType string
	timeout     time.Duration // remaining ctx budget at call time, rounded
}

type fakeSolver struct {
	t         *testing.T
	handler   func(c call) (*http.Response, error)
	calls     []call
	forbidden bool // when true, any call fails the test
}

func (f *fakeSolver) Do(req *http.Request) (*http.Response, error) {
	f.t.Helper()
	if f.forbidden {
		f.t.Fatalf("unexpected HTTP call: %s %s", req.Method, req.URL)
	}
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			f.t.Fatalf("read body: %v", err)
		}
		body = string(raw)
	}
	c := call{
		method:      req.Method,
		url:         req.URL.String(),
		body:        body,
		contentType: req.Header.Get("Content-Type"),
	}
	if deadline, ok := req.Context().Deadline(); ok {
		c.timeout = time.Until(deadline).Round(time.Second)
	}
	f.calls = append(f.calls, c)
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	return f.handler(c)
}

func (f *fakeSolver) paths() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method+" "+c.url)
	}
	return out
}

func reply(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

// newTestClient wires a Client to the fake and freezes the clock so the polling
// loop is deterministic and instant.
func newTestClient(t *testing.T, handler func(c call) (*http.Response, error)) (*Client, *fakeSolver) {
	t.Helper()
	fake := &fakeSolver{t: t, handler: handler}
	client := NewWithClient(fake)
	clock := time.Unix(1700000000, 0)
	client.now = func() time.Time { return clock }
	client.sleep = func(_ context.Context, d time.Duration) { clock = clock.Add(d) }
	return client, fake
}

func solveDefault(t *testing.T, client *Client) string {
	t.Helper()
	return client.Solve(context.Background(), Request{
		Sitekey:   "0x4AAA",
		PageURL:   "https://auth.openai.com/api/accounts/authorize/continue",
		SolverURL: "http://127.0.0.1:9999",
		Timeout:   120 * time.Second,
	})
}

// ---------------------------------------------------------------------------
// Callback shape — the whole point of the package
// ---------------------------------------------------------------------------

var (
	_ authproto.TurnstileSolver = SolveToken
	_ authproto.TurnstileSolver = New().SolveToken
)

// ---------------------------------------------------------------------------
// app.py:194-198 — argument normalization
// ---------------------------------------------------------------------------

func TestSolveRejectsBlankArgumentsWithoutCallingSolver(t *testing.T) {
	cases := []struct{ name, sitekey, pageURL string }{
		{"no sitekey", "", "https://x/y"},
		{"blank sitekey", " \t ", "https://x/y"},
		// NBSP and FIGURE SPACE are whitespace to str.strip() as well.
		{"unicode-blank sitekey", "  ", "https://x/y"},
		// So are the C0 separators, which unicode.IsSpace does NOT cover.
		{"separator-blank sitekey", "\x1c\x1f", "https://x/y"},
		{"no page url", "0x4AAA", ""},
		{"blank page url", "0x4AAA", "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, fake := newTestClient(t, nil)
			fake.forbidden = true
			got := client.Solve(context.Background(), Request{
				Sitekey: tc.sitekey, PageURL: tc.pageURL, Timeout: 120 * time.Second,
			})
			if got != "" {
				t.Errorf("token = %q, want empty", got)
			}
		})
	}
}

func TestSolveDefaultURLAndTrailingSlashes(t *testing.T) {
	cases := []struct{ solverURL, wantBase string }{
		{"", DefaultSolverURL},
		{"http://127.0.0.1:9999", "http://127.0.0.1:9999"},
		// rstrip("/") removes EVERY trailing slash, not just one.
		{"http://127.0.0.1:9999///", "http://127.0.0.1:9999"},
	}
	for _, tc := range cases {
		client, fake := newTestClient(t, func(call) (*http.Response, error) {
			return reply(200, `{"token":"tk"}`), nil
		})
		client.Solve(context.Background(), Request{
			Sitekey: "0x4AAA", PageURL: "https://x/y", SolverURL: tc.solverURL, Timeout: time.Second,
		})
		if len(fake.calls) != 1 {
			t.Fatalf("%q: calls = %v", tc.solverURL, fake.paths())
		}
		want := tc.wantBase + "/v1/leases"
		if fake.calls[0].url != want {
			t.Errorf("%q: url = %q, want %q", tc.solverURL, fake.calls[0].url, want)
		}
	}
	if DefaultSolverURL != "http://127.0.0.1:8888" {
		t.Errorf("DefaultSolverURL = %q, want app.py:139's value", DefaultSolverURL)
	}
}

// ---------------------------------------------------------------------------
// app.py:199-206 — the request body
// ---------------------------------------------------------------------------

func TestSolveRequestBody(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{
			name: "only the required keys, in dict order",
			req:  Request{Sitekey: " 0x4AAA ", PageURL: " https://x/y "},
			want: `{"sitekey": "0x4AAA", "url": "https://x/y"}`,
		},
		{
			name: "blank action and cdata are dropped",
			req:  Request{Sitekey: "0x4AAA", PageURL: "https://x/y", Action: "  ", CData: "\t"},
			want: `{"sitekey": "0x4AAA", "url": "https://x/y"}`,
		},
		{
			name: "action and cdata keep their literal position",
			req:  Request{Sitekey: "0x4AAA", PageURL: "https://x/y", Action: " login ", CData: "c1"},
			want: `{"sitekey": "0x4AAA", "url": "https://x/y", "action": "login", "cdata": "c1"}`,
		},
		{
			name: "cdata without action",
			req:  Request{Sitekey: "0x4AAA", PageURL: "https://x/y", CData: "c1"},
			want: `{"sitekey": "0x4AAA", "url": "https://x/y", "cdata": "c1"}`,
		},
		{
			// json.dumps leaves < > & alone where encoding/json would escape
			// them, and ensure_ascii=True escapes every non-ASCII rune, so the
			// CJK character below reaches the wire as a \u sequence.
			name: "json.dumps escape table",
			req:  Request{Sitekey: "a<b>c&d", PageURL: "https://x/中?q=\"1\""},
			want: "{\"sitekey\": \"a<b>c&d\", \"url\": \"https://x/\\u4e2d?q=\\\"1\\\"\"}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, fake := newTestClient(t, func(call) (*http.Response, error) {
				return reply(200, `{"token":"tk"}`), nil
			})
			tc.req.SolverURL = "http://s"
			tc.req.Timeout = time.Second
			client.Solve(context.Background(), tc.req)
			if len(fake.calls) != 1 {
				t.Fatalf("calls = %v", fake.paths())
			}
			got := fake.calls[0]
			if got.body != tc.want {
				t.Errorf("body  = %s\nwant  = %s", got.body, tc.want)
			}
			if got.method != http.MethodPost {
				t.Errorf("method = %s", got.method)
			}
			if got.contentType != "application/json" {
				t.Errorf("content-type = %q", got.contentType)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// app.py:207-223 — POST /v1/leases
// ---------------------------------------------------------------------------

func TestSolveImmediateToken(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"token key", `{"token":"  tk-1  "}`, "tk-1"},
		{"value key fallback", `{"value":"tk-2"}`, "tk-2"},
		{"empty token falls through to value", `{"token":"","value":"tk-3"}`, "tk-3"},
		{"token wins over value", `{"token":"tk-4","value":"tk-5"}`, "tk-4"},
		// str() coercion: a numeric token is stringified, not rejected, and the
		// literal is preserved rather than round-tripped through a float64.
		{"numeric token", `{"token":12345678901234567890}`, "12345678901234567890"},
		{"false token falls through", `{"token":false,"value":"tk-6"}`, "tk-6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, fake := newTestClient(t, func(call) (*http.Response, error) {
				return reply(200, tc.body), nil
			})
			if got := solveDefault(t, client); got != tc.want {
				t.Errorf("token = %q, want %q", got, tc.want)
			}
			if len(fake.calls) != 1 {
				t.Errorf("calls = %v, want just the lease create", fake.paths())
			}
		})
	}
}

func TestSolveCreateFailuresReturnEmpty(t *testing.T) {
	cases := []struct {
		name    string
		handler func(call) (*http.Response, error)
		// wantCalls is 1 when the failure stops the whole function, 2 when it
		// falls through to the /v1/solve fallback.
		wantCalls int
	}{
		{"transport error", func(call) (*http.Response, error) { return nil, errors.New("connection refused") }, 1},
		{"http 400", func(call) (*http.Response, error) { return reply(400, `{"token":"tk"}`), nil }, 1},
		{"http 500", func(call) (*http.Response, error) { return reply(500, ""), nil }, 1},
		{"unparseable body", func(call) (*http.Response, error) { return reply(200, "<html>nope"), nil }, 1},
		{"trailing data", func(call) (*http.Response, error) { return reply(200, `{}{}`), nil }, 1},
		// A non-dict body skips the isinstance branch: lease_id stays "" and the
		// synchronous fallback runs (and fails here too).
		{"list body", func(call) (*http.Response, error) { return reply(200, `[1,2]`), nil }, 2},
		{"string body", func(call) (*http.Response, error) { return reply(200, `"hi"`), nil }, 2},
		// `create.json() if create.content else {}` — an empty body is {}.
		{"empty body", func(call) (*http.Response, error) { return reply(204, ""), nil }, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client, fake := newTestClient(t, func(c call) (*http.Response, error) {
				calls++
				if calls == 1 {
					return tc.handler(c)
				}
				return reply(503, ""), nil
			})
			if got := solveDefault(t, client); got != "" {
				t.Errorf("token = %q, want empty", got)
			}
			if len(fake.calls) != tc.wantCalls {
				t.Errorf("calls = %v, want %d", fake.paths(), tc.wantCalls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// app.py:225-236 — the synchronous /v1/solve fallback
// ---------------------------------------------------------------------------

func TestSolveSyncFallback(t *testing.T) {
	cases := []struct {
		name       string
		solveReply func() (*http.Response, error)
		want       string
	}{
		{"token", func() (*http.Response, error) { return reply(200, `{"token":" tk "}`), nil }, "tk"},
		{"value", func() (*http.Response, error) { return reply(200, `{"value":"tk"}`), nil }, "tk"},
		// Python returns the empty token from here rather than falling through.
		{"empty token", func() (*http.Response, error) { return reply(200, `{"token":""}`), nil }, ""},
		{"empty body", func() (*http.Response, error) { return reply(200, ""), nil }, ""},
		{"non-dict body", func() (*http.Response, error) { return reply(200, `[]`), nil }, ""},
		{"http 500", func() (*http.Response, error) { return reply(500, `{"token":"tk"}`), nil }, ""},
		{"transport error", func() (*http.Response, error) { return nil, errors.New("boom") }, ""},
		{"unparseable", func() (*http.Response, error) { return reply(200, "{"), nil }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, fake := newTestClient(t, func(c call) (*http.Response, error) {
				if strings.HasSuffix(c.url, "/v1/leases") {
					// No token and no lease id anywhere.
					return reply(200, `{"status":"queued"}`), nil
				}
				return tc.solveReply()
			})
			if got := solveDefault(t, client); got != tc.want {
				t.Errorf("token = %q, want %q", got, tc.want)
			}
			want := []string{
				"POST http://127.0.0.1:9999/v1/leases",
				"POST http://127.0.0.1:9999/v1/solve",
			}
			if strings.Join(fake.paths(), "|") != strings.Join(want, "|") {
				t.Fatalf("calls = %v, want %v", fake.paths(), want)
			}
			// The fallback re-sends the SAME payload.
			if fake.calls[1].body != fake.calls[0].body {
				t.Errorf("solve body = %s, lease body = %s", fake.calls[1].body, fake.calls[0].body)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// app.py:238-263 — the lease polling loop
// ---------------------------------------------------------------------------

func TestSolveLeaseIDPrecedence(t *testing.T) {
	cases := []struct{ name, body, wantID string }{
		{"lease_id", `{"lease_id":" L1 ","id":"L2","leaseId":"L3"}`, "L1"},
		{"id", `{"id":"L2","leaseId":"L3"}`, "L2"},
		{"leaseId", `{"leaseId":"L3"}`, "L3"},
		// An empty value is falsy, so `or` skips it.
		{"empty lease_id falls through", `{"lease_id":"","id":"L2"}`, "L2"},
		{"numeric id", `{"id":77}`, "77"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, fake := newTestClient(t, func(c call) (*http.Response, error) {
				if strings.HasSuffix(c.url, "/v1/leases") {
					return reply(200, tc.body), nil
				}
				return reply(200, `{"token":"tk"}`), nil
			})
			if got := solveDefault(t, client); got != "tk" {
				t.Fatalf("token = %q", got)
			}
			want := "http://127.0.0.1:9999/v1/leases/" + tc.wantID + "/consume"
			if len(fake.calls) != 2 || fake.calls[1].url != want {
				t.Fatalf("calls = %v, want the consume of %q", fake.paths(), tc.wantID)
			}
			// The consume POST carries no body and therefore no content-type.
			if fake.calls[1].body != "" || fake.calls[1].contentType != "" {
				t.Errorf("consume body = %q, content-type = %q", fake.calls[1].body, fake.calls[1].contentType)
			}
		})
	}
}

func TestSolveLeasePollingSucceedsOnLaterIteration(t *testing.T) {
	consumes := 0
	client, fake := newTestClient(t, func(c call) (*http.Response, error) {
		switch {
		case strings.HasSuffix(c.url, "/v1/leases"):
			return reply(200, `{"lease_id":"L1"}`), nil
		case strings.HasSuffix(c.url, "/consume"):
			consumes++
			if consumes < 3 {
				return reply(200, `{"status":"pending"}`), nil
			}
			return reply(200, `{"token":"tk-late"}`), nil
		default: // the GET fallback
			return reply(404, ""), nil
		}
	})
	if got := solveDefault(t, client); got != "tk-late" {
		t.Errorf("token = %q", got)
	}
	// create + 2 full iterations (consume + GET) + the winning consume.
	if len(fake.calls) != 6 {
		t.Errorf("calls = %v", fake.paths())
	}
}

func TestSolveLeaseGetFallback(t *testing.T) {
	client, fake := newTestClient(t, func(c call) (*http.Response, error) {
		switch {
		case strings.HasSuffix(c.url, "/v1/leases"):
			return reply(200, `{"lease_id":"L1"}`), nil
		case strings.HasSuffix(c.url, "/consume"):
			return reply(404, ""), nil
		default:
			return reply(200, `{"value":" tk-get "}`), nil
		}
	})
	if got := solveDefault(t, client); got != "tk-get" {
		t.Errorf("token = %q", got)
	}
	want := "GET http://127.0.0.1:9999/v1/leases/L1"
	if len(fake.calls) != 3 || fake.paths()[2] != want {
		t.Errorf("calls = %v, want the GET fallback last", fake.paths())
	}
}

func TestSolveConsumeTransportErrorSkipsTheGetFallback(t *testing.T) {
	// Both requests live in ONE try/except: a broken consume skips the GET for
	// that iteration and goes straight to the sleep.
	client, fake := newTestClient(t, func(c call) (*http.Response, error) {
		if strings.HasSuffix(c.url, "/v1/leases") {
			return reply(200, `{"lease_id":"L1"}`), nil
		}
		return nil, errors.New("connection reset")
	})
	client.Solve(context.Background(), Request{
		Sitekey: "0x4AAA", PageURL: "https://x/y", SolverURL: "http://s",
		Timeout: 3 * time.Second, // max(5.0, 3.0) -> a 5s window -> 5 iterations
	})
	for _, p := range fake.paths()[1:] {
		if !strings.HasSuffix(p, "/consume") {
			t.Fatalf("a GET slipped through after a failed consume: %v", fake.paths())
		}
	}
	if len(fake.calls) != 6 {
		t.Errorf("calls = %v, want create + 5 consumes", fake.paths())
	}
}

func TestSolveDeadLeaseStopsImmediately(t *testing.T) {
	// The set is {"failed", "error", "expired", "dead"}, compared after
	// str(...).lower().
	for _, status := range []string{"failed", "error", "expired", "dead", "EXPIRED", "Failed"} {
		t.Run(status, func(t *testing.T) {
			client, fake := newTestClient(t, func(c call) (*http.Response, error) {
				if strings.HasSuffix(c.url, "/v1/leases") {
					return reply(200, `{"lease_id":"L1"}`), nil
				}
				return reply(200, `{"status":"`+status+`"}`), nil
			})
			if got := solveDefault(t, client); got != "" {
				t.Errorf("token = %q, want empty", got)
			}
			if len(fake.calls) != 2 {
				t.Errorf("calls = %v, want create + one consume", fake.paths())
			}
		})
	}
}

func TestSolveLiveStatusKeepsPolling(t *testing.T) {
	client, fake := newTestClient(t, func(c call) (*http.Response, error) {
		if strings.HasSuffix(c.url, "/v1/leases") {
			return reply(200, `{"lease_id":"L1"}`), nil
		}
		// "pending" is not in the dead set, and a blank status is not either.
		return reply(200, `{"status":"pending"}`), nil
	})
	client.Solve(context.Background(), Request{
		Sitekey: "0x4AAA", PageURL: "https://x/y", SolverURL: "http://s", Timeout: 5 * time.Second,
	})
	if len(fake.calls) != 11 { // create + 5 * (consume + GET)
		t.Errorf("calls = %d (%v), want 11", len(fake.calls), fake.paths())
	}
}

func TestSolvePollingWindow(t *testing.T) {
	cases := []struct {
		timeout   time.Duration
		wantPolls int
	}{
		// The `float(timeout or 5)` fallback is unreachable in practice: a 0
		// timeout never survives the create call (see the test below). The
		// max(5.0, ...) floor is what this exercises.
		{time.Second, 5},
		{1500 * time.Millisecond, 5},
		{5 * time.Second, 5},
		{9 * time.Second, 9},
		{120 * time.Second, 120}, // the production value
	}
	for _, tc := range cases {
		client, fake := newTestClient(t, func(c call) (*http.Response, error) {
			if strings.HasSuffix(c.url, "/v1/leases") {
				return reply(200, `{"lease_id":"L1"}`), nil
			}
			return reply(500, ""), nil
		})
		if got := client.Solve(context.Background(), Request{
			Sitekey: "0x4AAA", PageURL: "https://x/y", SolverURL: "http://s", Timeout: tc.timeout,
		}); got != "" {
			t.Errorf("timeout %v: token = %q", tc.timeout, got)
		}
		// create + wantPolls * (consume + GET)
		if want := 1 + 2*tc.wantPolls; len(fake.calls) != want {
			t.Errorf("timeout %v: calls = %d, want %d", tc.timeout, len(fake.calls), want)
		}
	}
}

func TestSolveNonPositiveTimeoutDiesAtCreate(t *testing.T) {
	// requests refuses a timeout of 0 (urllib3 raises "Timeout value connect
	// was 0, but it must be > 0"), and the bare except turns that into "". An
	// already-expired context does the same thing here, so the polling window
	// is never reached.
	for _, timeout := range []time.Duration{0, -3 * time.Second} {
		client, fake := newTestClient(t, func(call) (*http.Response, error) {
			return reply(200, `{"token":"tk"}`), nil
		})
		if got := client.Solve(context.Background(), Request{
			Sitekey: "0x4AAA", PageURL: "https://x/y", SolverURL: "http://s", Timeout: timeout,
		}); got != "" {
			t.Errorf("timeout %v: token = %q, want empty", timeout, got)
		}
		if len(fake.calls) != 1 {
			t.Errorf("timeout %v: calls = %v, want only the doomed create", timeout, fake.paths())
		}
	}
}

// ---------------------------------------------------------------------------
// Per-request timeouts (app.py:208, 228, 242, 253)
// ---------------------------------------------------------------------------

func TestSolveRequestTimeouts(t *testing.T) {
	client, fake := newTestClient(t, func(c call) (*http.Response, error) {
		if strings.HasSuffix(c.url, "/v1/leases") {
			return reply(200, `{"lease_id":"L1"}`), nil
		}
		return reply(500, ""), nil
	})
	client.Solve(context.Background(), Request{
		Sitekey: "0x4AAA", PageURL: "https://x/y", SolverURL: "http://s", Timeout: 120 * time.Second,
	})
	if len(fake.calls) < 3 {
		t.Fatalf("calls = %v", fake.paths())
	}
	if got := fake.calls[0].timeout; got != 30*time.Second { // min(30.0, 120.0)
		t.Errorf("create timeout = %v, want 30s", got)
	}
	if got := fake.calls[1].timeout; got != 15*time.Second {
		t.Errorf("consume timeout = %v, want 15s", got)
	}
	if got := fake.calls[2].timeout; got != 15*time.Second {
		t.Errorf("lease GET timeout = %v, want 15s", got)
	}

	// The synchronous fallback gets the FULL timeout, uncapped.
	client2, fake2 := newTestClient(t, func(c call) (*http.Response, error) {
		if strings.HasSuffix(c.url, "/v1/leases") {
			return reply(200, `{}`), nil
		}
		return reply(500, ""), nil
	})
	client2.Solve(context.Background(), Request{
		Sitekey: "0x4AAA", PageURL: "https://x/y", SolverURL: "http://s", Timeout: 90 * time.Second,
	})
	if got := fake2.calls[1].timeout; got != 90*time.Second {
		t.Errorf("solve timeout = %v, want 90s", got)
	}

	// A timeout below the cap is used as-is by the create call.
	client3, fake3 := newTestClient(t, func(call) (*http.Response, error) {
		return reply(200, `{"token":"tk"}`), nil
	})
	client3.Solve(context.Background(), Request{
		Sitekey: "0x4AAA", PageURL: "https://x/y", SolverURL: "http://s", Timeout: 10 * time.Second,
	})
	if got := fake3.calls[0].timeout; got != 10*time.Second {
		t.Errorf("create timeout = %v, want 10s", got)
	}
}

// ---------------------------------------------------------------------------
// Cancellation — a DIVERGENCE from the Python, which could not be cancelled
// ---------------------------------------------------------------------------

func TestSolveHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, fake := newTestClient(t, func(c call) (*http.Response, error) {
		if strings.HasSuffix(c.url, "/v1/leases") {
			return reply(200, `{"lease_id":"L1"}`), nil
		}
		cancel()
		return reply(500, ""), nil
	})
	got := client.Solve(ctx, Request{
		Sitekey: "0x4AAA", PageURL: "https://x/y", SolverURL: "http://s", Timeout: 120 * time.Second,
	})
	if got != "" {
		t.Errorf("token = %q", got)
	}
	// create + the consume that cancels + the GET of that same iteration.
	if len(fake.calls) != 3 {
		t.Errorf("calls = %v, want the loop to stop after one iteration", fake.paths())
	}
}

// ---------------------------------------------------------------------------
// SolveToken adapter
// ---------------------------------------------------------------------------

func TestSolveTokenNeverReturnsAnError(t *testing.T) {
	client, _ := newTestClient(t, func(call) (*http.Response, error) {
		return nil, errors.New("solver offline")
	})
	token, err := client.SolveToken("0x4AAA", "https://x/y", "http://s", 120*time.Second)
	if token != "" || err != nil {
		t.Errorf("SolveToken = (%q, %v), want (empty, nil)", token, err)
	}

	client2, fake := newTestClient(t, func(call) (*http.Response, error) {
		return reply(200, `{"token":"tk"}`), nil
	})
	token, err = client2.SolveToken("0x4AAA", "https://x/y", "http://s", 120*time.Second)
	if token != "tk" || err != nil {
		t.Errorf("SolveToken = (%q, %v)", token, err)
	}
	// authproto passes the solver URL it already defaulted; nothing rewrites it.
	if fake.calls[0].url != "http://s/v1/leases" {
		t.Errorf("url = %q", fake.calls[0].url)
	}
}

// ---------------------------------------------------------------------------
// Python value semantics
// ---------------------------------------------------------------------------

func TestPyStripMatchesPythonWhitespace(t *testing.T) {
	// 0x1c-0x1f are whitespace to str.strip() but not to unicode.IsSpace.
	if got := pyStrip("\x1c\x1f tk 　"); got != "tk" {
		t.Errorf("pyStrip = %q", got)
	}
	if got := pyStrip("a b"); got != "a b" {
		t.Errorf("pyStrip = %q", got)
	}
}

func TestPyStrOrChain(t *testing.T) {
	cases := []struct {
		values []any
		want   string
	}{
		{[]any{nil, "b"}, "b"},
		{[]any{"", "b"}, "b"},
		{[]any{false, "b"}, "b"},
		{[]any{true, "b"}, "True"},
		{[]any{[]any{}, "b"}, "b"},
		{[]any{[]any{"x"}, "b"}, "['x']"},
		{[]any{map[string]any{}, "b"}, "b"},
		{[]any{" ", "b"}, " "}, // a blank string is truthy
		{[]any{nil, nil}, ""},
	}
	for _, tc := range cases {
		if got := pyStrOr(tc.values...); got != tc.want {
			t.Errorf("pyStrOr(%v) = %q, want %q", tc.values, got, tc.want)
		}
	}
}

func TestEncodePayloadEscapeTable(t *testing.T) {
	got := string(encodePayload([]kv{
		{"sitekey", "<a>&\"b\"\n"},
		{"url", "https://例.com/\U0001F600"},
	}))
	want := "{\"sitekey\": \"<a>&\\\"b\\\"\\n\", \"url\": \"https://\\u4f8b.com/\\ud83d\\ude00\"}"
	if got != want {
		t.Errorf("encodePayload =\n %s\nwant\n %s", got, want)
	}
}

func TestJSONBodyEmptyIsAnEmptyDict(t *testing.T) {
	v, err := jsonBody(nil)
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := asDict(v)
	if !ok || len(obj.Keys()) != 0 {
		t.Errorf("jsonBody(nil) = %#v", v)
	}
	if _, err := jsonBody([]byte(`{}{}`)); err == nil {
		t.Error("trailing data should be rejected, as json.loads does")
	}
}
