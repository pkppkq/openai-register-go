package smsbower

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Every expectation in this file was produced by executing the VERBATIM
// app.py line-slice for SMSBowerError + SMSBowerClient (app.py:3704-3875) under
// CPython 3.12 with a fake request_get and a fake clock, then recording what it
// returned. Nothing here was derived by reading the Python and reasoning about
// it — that is how the divergences these tests pin got in.
//
// MONEY: nothing in this file may reach the real handler_api.php. Every test
// drives an httptest server or calls a pure helper. GetNumber rents a BILLABLE
// number; it appears below only against a local server.

// ---------------------------------------------------------------------------
// test server
// ---------------------------------------------------------------------------

type fakeAPI struct {
	bodies  []string
	status  []int
	queries []string
	calls   int
	srv     *httptest.Server
}

// newFakeAPI serves bodies[i] for call i, repeating the last one forever.
func newFakeAPI(t *testing.T, bodies ...string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{bodies: bodies}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := f.calls
		f.calls++
		f.queries = append(f.queries, r.URL.RawQuery)
		if i >= len(f.bodies) {
			i = len(f.bodies) - 1
		}
		if i < len(f.status) && f.status[i] >= 400 {
			w.WriteHeader(f.status[i])
		}
		_, _ = w.Write([]byte(f.bodies[i]))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient("KEY", f.srv.URL, "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// noSleep swaps the retry delay for a recorder, so the 1.5/3/4.5s backoff can be
// asserted without spending nine seconds per case.
func noSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	fakeNow := time.Unix(1_700_000_000, 0)
	prevSleep := sleepFor
	prevNow := nowFor
	nowFor = func() time.Time { return fakeNow }
	sleepFor = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		fakeNow = fakeNow.Add(d)
		return ctx.Err()
	}
	t.Cleanup(func() {
		sleepFor = prevSleep
		nowFor = prevNow
	})
	return &slept
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// is_transient_error / casefold (app.py:3745-3748)
// ---------------------------------------------------------------------------

// TestIsTransientErrorMatchesPythonCasefold pins str.casefold(), not
// strings.ToLower. Both rows marked DIVERGENCE below were wrong before: they
// decide whether a failed request is retried three more times or surfaces
// immediately, i.e. whether a rental attempt is abandoned on a blip.
func TestIsTransientErrorMatchesPythonCasefold(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"  ", false},
		{"connection reset by peer", true},
		{"Connection Aborted", true},
		{"CONNECTION RESET", true},
		{"TIMEOUT", true},
		{"TiMeD OuT", true},
		{"Max Retries Exceeded", true},
		{"10054", true},
		{"1005", false},
		{"远程主机强迫关闭", true},
		{"SSL handshake", true},
		{"ssl", true},
		{"tls", true},
		{"no match here", false},
		{"Broken Pipe", true},
		{"remote end closed", true},
		{"empty reply from server", true},
		{"temporarily unavailable", true},
		{"None", false},
		{"0", false},
		// DIVERGENCE FIXED: casefold("ßl") is "ssl"; ToLower left "ßl".
		{"ßl", true},
		{"großl", true},
		{"ẞl", true}, // U+1E9E CAPITAL SHARP S also folds to "ss"
		// DIVERGENCE FIXED: casefold("İ") is "i"+U+0307, so the combining
		// dot splits "timed out"; ToLower("İ") is a bare "i" and matched.
		{"TİMED OUT", false},
		{"TIMED OUT", true},
		// Full-width letters have no case mapping at all in either language.
		{"ＴＩＭＥＤ ＯＵＴ", false},
		{"ı", false},
	}
	for _, tc := range cases {
		if got := IsTransientError(tc.in); got != tc.want {
			t.Errorf("IsTransientError(%q) = %v, python says %v", tc.in, got, tc.want)
		}
	}
	if IsTransientError(nil) {
		t.Error("IsTransientError(nil) must be false: str(None or \"\") is \"\"")
	}
	if IsTransientError(0) {
		t.Error("IsTransientError(0) must be false: str(0 or \"\") is \"\"")
	}
}

// TestPyCasefoldTable pins the 18 code points whose casefold differs from
// strings.ToLower in a way an ASCII substring test can see. The table was
// enumerated by scanning all 1,114,112 code points against CPython 3.12; if a
// Unicode update adds a nineteenth, this test will not catch it, but the two
// entries that matter in practice are asserted directly.
func TestPyCasefoldTable(t *testing.T) {
	cases := [][2]string{
		{"ß", "ss"}, {"İ", "i̇"}, {"ŉ", "ʼn"},
		{"ſ", "s"}, {"ǰ", "ǰ"}, {"ẖ", "ẖ"},
		{"ẗ", "ẗ"}, {"ẘ", "ẘ"}, {"ẙ", "ẙ"},
		{"ẚ", "aʾ"}, {"ẞ", "ss"}, {"ﬀ", "ff"},
		{"ﬁ", "fi"}, {"ﬂ", "fl"}, {"ﬃ", "ffi"},
		{"ﬄ", "ffl"}, {"ﬅ", "st"}, {"ﬆ", "st"},
	}
	for _, tc := range cases {
		if got := pyCasefold(tc[0]); got != tc[1] {
			t.Errorf("pyCasefold(%q) = %q, CPython says %q", tc[0], got, tc[1])
		}
	}
	// Folding must be idempotent over ASCII and must not disturb the one
	// non-ASCII marker.
	if got := pyCasefold("远程主机强迫关闭"); got != "远程主机强迫关闭" {
		t.Errorf("pyCasefold mangled the Chinese marker: %q", got)
	}
}

// ---------------------------------------------------------------------------
// _request (app.py:3750-3777)
// ---------------------------------------------------------------------------

// TestRequestQueryOrderMatchesPython pins the wire order. Python built the query
// from a dict (api_key, action, then kwargs in source order); a Go map plus
// url.Values.Encode() would have sent it sorted alphabetically. The endpoint has
// only ever seen the Python order.
func TestRequestQueryOrderMatchesPython(t *testing.T) {
	t.Run("getNumber", func(t *testing.T) {
		f := newFakeAPI(t, "ACCESS_NUMBER:1:+7")
		if _, err := f.client(t).GetNumber(context.Background(), "dr", "33", "0.07"); err != nil {
			t.Fatalf("GetNumber: %v", err)
		}
		want := "api_key=KEY&action=getNumber&service=dr&country=33&maxPrice=0.07"
		if f.queries[0] != want {
			t.Errorf("query = %q, python sends %q", f.queries[0], want)
		}
	})
	t.Run("getNumber drops the empty maxPrice", func(t *testing.T) {
		// `value not in ("", None)` (app.py:3752); empty service/country fall
		// back to the defaults first (app.py:3788-3789).
		f := newFakeAPI(t, "ACCESS_NUMBER:1:+7")
		if _, err := f.client(t).GetNumber(context.Background(), "", "", ""); err != nil {
			t.Fatalf("GetNumber: %v", err)
		}
		want := "api_key=KEY&action=getNumber&service=dr&country=33"
		if f.queries[0] != want {
			t.Errorf("query = %q, python sends %q", f.queries[0], want)
		}
	})
	t.Run("setStatus keeps a zero status but drops an empty id", func(t *testing.T) {
		// 0 is not in ("", None), so it survives the filter; the id does not.
		f := newFakeAPI(t, "ACCESS_ACTIVATION")
		if _, err := f.client(t).SetStatus(context.Background(), "", 0); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		want := "api_key=KEY&action=setStatus&status=0"
		if f.queries[0] != want {
			t.Errorf("query = %q, python sends %q", f.queries[0], want)
		}
	})
	t.Run("setStatus strips the id", func(t *testing.T) {
		f := newFakeAPI(t, "ACCESS_ACTIVATION")
		if _, err := f.client(t).SetStatus(context.Background(), "  A1  ", 8); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		want := "api_key=KEY&action=setStatus&id=A1&status=8"
		if f.queries[0] != want {
			t.Errorf("query = %q, python sends %q", f.queries[0], want)
		}
	})
}

// TestRequestBodyClassification is the response-classification table straight
// out of CPython. The \x1c rows are the fix: str.strip() removes U+001C..U+001F
// and strings.TrimSpace does not, so "\x1cNO_BALANCE" — an out-of-funds reply —
// used to sail through as a SUCCESSFUL response body.
func TestRequestBodyClassification(t *testing.T) {
	cases := []struct {
		body     string
		want     string // returned text, "" when it must error
		wantErr  string // exact error text
		attempts int
	}{
		{body: "ACCESS_BALANCE:12.34", want: "ACCESS_BALANCE:12.34", attempts: 1},
		{body: "  ACCESS_BALANCE:12.34  ", want: "ACCESS_BALANCE:12.34", attempts: 1},
		{body: "ACCESS_BALANCE:12.34\r\n", want: "ACCESS_BALANCE:12.34", attempts: 1},
		{body: "", wantErr: "SMSBower 返回空响应", attempts: 4},
		{body: "   ", wantErr: "SMSBower 返回空响应", attempts: 4},
		// DIVERGENCE FIXED: TrimSpace left U+001C, so the body looked non-empty.
		{body: "\x1c", wantErr: "SMSBower 返回空响应", attempts: 4},
		{body: "\x1d\x1e\x1f", wantErr: "SMSBower 返回空响应", attempts: 4},
		// DIVERGENCE FIXED: this used to be returned as a success body.
		{body: "\x1cNO_BALANCE", wantErr: "账户余额不足 (NO_BALANCE)", attempts: 1},
		{body: "NO_BALANCE", wantErr: "账户余额不足 (NO_BALANCE)", attempts: 1},
		{body: "no_balance", wantErr: "账户余额不足 (NO_BALANCE)", attempts: 1},
		{body: "NO_BALANCE:extra", wantErr: "账户余额不足 (NO_BALANCE)", attempts: 1},
		{body: " NO_BALANCE : x", wantErr: "账户余额不足 (NO_BALANCE)", attempts: 1},
		// A sentinel that merely STARTS WITH a known one is not the known one,
		// and does not start with ERROR or BAD_ either: plain success.
		{body: "NO_BALANCES", want: "NO_BALANCES", attempts: 1},
		{body: "BAD_KEY", wantErr: "API Key 无效 (BAD_KEY)", attempts: 1},
		{body: "BAD_KEYS", wantErr: "SMSBower 返回错误: BAD_KEYS", attempts: 1},
		{body: "BAD_", wantErr: "SMSBower 返回错误: BAD_", attempts: 1},
		{body: "ERROR", wantErr: "SMSBower 返回错误: ERROR", attempts: 1},
		{body: "ERRORS", wantErr: "SMSBower 返回错误: ERRORS", attempts: 1},
		{body: "error_sql", wantErr: "SMSBower 返回错误: error_sql", attempts: 1},
		{body: "ERROR:detail", wantErr: "SMSBower 返回错误: ERROR:detail", attempts: 1},
		{body: "NO_NUMBERS", wantErr: "当前地区没有可用号码 (NO_NUMBERS)", attempts: 1},
		{body: "MAX_PRICE_EXCEEDED", wantErr: "可用号码价格超过最高限价 (MAX_PRICE_EXCEEDED)", attempts: 1},
		{body: "EARLY_CANCEL_DENIED", wantErr: "购买后暂时不允许取消 (EARLY_CANCEL_DENIED)", attempts: 1},
		{body: "STATUS_WAIT_CODE", want: "STATUS_WAIT_CODE", attempts: 1},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.body, "\x1c", "<FS>"), func(t *testing.T) {
			noSleep(t)
			f := newFakeAPI(t, tc.body)
			got, err := f.client(t).request(context.Background(), "getBalance")
			if tc.wantErr != "" {
				if errText(err) != tc.wantErr {
					t.Errorf("err = %q, python raises %q", errText(err), tc.wantErr)
				}
			} else if err != nil || got != tc.want {
				t.Errorf("got (%q, %v), python returns %q", got, err, tc.want)
			}
			if f.calls != tc.attempts {
				t.Errorf("%d HTTP attempts, python makes %d", f.calls, tc.attempts)
			}
		})
	}
}

// TestRequestBackoffSchedule pins time.sleep(min(8, attempt * 1.5))
// (app.py:3761/3768) and the four-attempt cap of range(1, 5).
func TestRequestBackoffSchedule(t *testing.T) {
	slept := noSleep(t)
	f := newFakeAPI(t, "", "", "", "")
	if _, err := f.client(t).request(context.Background(), "getBalance"); errText(err) != "SMSBower 返回空响应" {
		t.Fatalf("err = %v", err)
	}
	want := []time.Duration{1500 * time.Millisecond, 3 * time.Second, 4500 * time.Millisecond}
	if len(*slept) != len(want) {
		t.Fatalf("slept %v, python sleeps %v", *slept, want)
	}
	for i := range want {
		if (*slept)[i] != want[i] {
			t.Errorf("sleep %d = %v, python %v", i, (*slept)[i], want[i])
		}
	}
	if f.calls != 4 {
		t.Errorf("%d attempts, python makes 4", f.calls)
	}
	// min(8, attempt*1.5) clamps from attempt 6 on; only 1..3 are reachable
	// through _request, but the arithmetic itself is pinned here.
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{{1, 1500 * time.Millisecond}, {2, 3 * time.Second}, {3, 4500 * time.Millisecond},
		{5, 7500 * time.Millisecond}, {6, 8 * time.Second}, {100, 8 * time.Second}} {
		if got := backoff(tc.attempt); got != tc.want {
			t.Errorf("backoff(%d) = %v, python %v", tc.attempt, got, tc.want)
		}
	}
}

func TestRequestRecoversAfterEmptyBodies(t *testing.T) {
	noSleep(t)
	f := newFakeAPI(t, "", "", "ACCESS_BALANCE:9")
	got, err := f.client(t).request(context.Background(), "getBalance")
	if err != nil || got != "ACCESS_BALANCE:9" {
		t.Fatalf("got (%q, %v), python returns ACCESS_BALANCE:9", got, err)
	}
	if f.calls != 3 {
		t.Errorf("%d attempts, python makes 3", f.calls)
	}
}

// ---------------------------------------------------------------------------
// get_balance / get_number / get_status
// ---------------------------------------------------------------------------

func TestGetBalanceMatchesPython(t *testing.T) {
	cases := []struct{ body, want, wantErr string }{
		{body: "ACCESS_BALANCE:12.34", want: "12.34"},
		{body: "ACCESS_BALANCE:  12.34  ", want: "12.34"},
		{body: "ACCESS_BALANCE:", want: ""},
		{body: "ACCESS_BALANCE:1:2", want: "1:2"},
		// str.strip() eats U+001C on the value too (app.py:3783).
		{body: "ACCESS_BALANCE:\x1c12.34\x1c", want: "12.34"},
		{body: "ACCESS_BALANCE", wantErr: "SMSBower 余额响应无法识别: ACCESS_BALANCE"},
		{body: "access_balance:1", wantErr: "SMSBower 余额响应无法识别: access_balance:1"},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			f := newFakeAPI(t, tc.body)
			got, err := f.client(t).GetBalance(context.Background())
			if tc.wantErr != "" {
				if errText(err) != tc.wantErr {
					t.Errorf("err = %q, python %q", errText(err), tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got (%q, %v), python %q", got, err, tc.want)
			}
		})
	}
}

// TestGetNumberParsingMatchesPython drives GetNumber against a LOCAL server
// only: the real call rents a billable number.
//
// The Unicode-digit rows are the fix — Python's \d is \p{Nd} (680 code points),
// RE2's is [0-9], so a number rendered in non-ASCII digits used to be rejected
// as an unparseable response AFTER the rental had been paid for.
func TestGetNumberParsingMatchesPython(t *testing.T) {
	cases := []struct{ body, wantID, wantNum, wantErr string }{
		{body: "ACCESS_NUMBER:12345:+79001234567", wantID: "12345", wantNum: "+79001234567"},
		{body: "ACCESS_NUMBER:12345:79001234567", wantID: "12345", wantNum: "+79001234567"},
		{body: "ACCESS_NUMBER:abc-def:+1", wantID: "abc-def", wantNum: "+1"},
		{body: "ACCESS_NUMBER:中文:+7", wantID: "中文", wantNum: "+7"},
		{body: "ACCESS_NUMBER:1:١٢٣", wantID: "1", wantNum: "+١٢٣"},
		{body: "ACCESS_NUMBER:1:１２３", wantID: "1", wantNum: "+１２３"},
		{body: "ACCESS_NUMBER:1:+١٢", wantID: "1", wantNum: "+١٢"},
		{body: "ACCESS_NUMBER::+1", wantErr: "SMSBower 取号响应无法识别: ACCESS_NUMBER::+1"},
		{body: "ACCESS_NUMBER:1:2:3", wantErr: "SMSBower 取号响应无法识别: ACCESS_NUMBER:1:2:3"},
		{body: "ACCESS_NUMBER:1:+7900123456a", wantErr: "SMSBower 取号响应无法识别: ACCESS_NUMBER:1:+7900123456a"},
		{body: "ACCESS_NUMBER:1:++7900", wantErr: "SMSBower 取号响应无法识别: ACCESS_NUMBER:1:++7900"},
		{body: "access_number:1:+7", wantErr: "SMSBower 取号响应无法识别: access_number:1:+7"},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			f := newFakeAPI(t, tc.body)
			got, err := f.client(t).GetNumber(context.Background(), "dr", "33", "")
			if tc.wantErr != "" {
				if errText(err) != tc.wantErr {
					t.Errorf("err = %q, python %q", errText(err), tc.wantErr)
				}
				return
			}
			if err != nil || got.ActivationID != tc.wantID || got.Number != tc.wantNum {
				t.Errorf("got (%+v, %v), python {%q %q}", got, err, tc.wantID, tc.wantNum)
			}
		})
	}
}

func TestGetStatusMatchesPython(t *testing.T) {
	cases := []struct{ body, wantStatus, wantValue string }{
		{"STATUS_OK:123456", "STATUS_OK", "123456"},
		{"STATUS_OK:", "STATUS_OK", ""},
		{"STATUS_OK", "STATUS_OK", ""},
		{"status_ok:1", "STATUS_OK", "1"},
		{" STATUS_WAIT_CODE ", "STATUS_WAIT_CODE", ""},
		{"STATUS_OK: 123 456 ", "STATUS_OK", "123 456"},
		{"STATUS_OK:a:b", "STATUS_OK", "a:b"},
		// str.strip() on both halves, U+001C included.
		{"STATUS_OK:\x1c123\x1c", "STATUS_OK", "123"},
		{"\x1cSTATUS_OK:1", "STATUS_OK", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			f := newFakeAPI(t, tc.body)
			status, value, err := f.client(t).GetStatus(context.Background(), "ID")
			if err != nil || status != tc.wantStatus || value != tc.wantValue {
				t.Errorf("got (%q, %q, %v), python (%q, %q)", status, value, err, tc.wantStatus, tc.wantValue)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// prices (app.py:3800-3843)
// ---------------------------------------------------------------------------

func TestGetPriceQuoteMatchesPython(t *testing.T) {
	cases := []struct {
		body      string
		wantCost  float64
		wantCount int
		wantErr   string
	}{
		{body: `{"33":{"dr":{"cost":18.75,"count":1234}}}`, wantCost: 18.75, wantCount: 1234},
		// A numeric-as-string payload is a plain dict to Python; the strict
		// json.Number decode used to reject the whole response.
		{body: `{"33":{"dr":{"cost":"18.75","count":"1234"}}}`, wantCost: 18.75, wantCount: 1234},
		{body: `{"33":{"dr":{"cost":0,"count":0}}}`},
		{body: `{"33":{"dr":{"count":5}}}`, wantCount: 5},
		{body: `{"33":{"dr":{"cost":null,"count":null}}}`},
		{body: `{"33":{"dr":{"cost":1e3,"count":1}}}`, wantCost: 1000, wantCount: 1},
		// Everything that is not an object at some level is "缺少", never
		// "无法识别" — only a syntax error is unrecognisable.
		{body: `{"33":{"dr":[]}}`, wantErr: "SMSBower 价格响应缺少 33/dr: " + `{"33":{"dr":[]}}`},
		{body: `{"33":{}}`, wantErr: "SMSBower 价格响应缺少 33/dr: " + `{"33":{}}`},
		{body: `{}`, wantErr: "SMSBower 价格响应缺少 33/dr: {}"},
		{body: `[]`, wantErr: "SMSBower 价格响应缺少 33/dr: []"},
		{body: `null`, wantErr: "SMSBower 价格响应缺少 33/dr: null"},
		{body: `"x"`, wantErr: `SMSBower 价格响应缺少 33/dr: "x"`},
		{body: `not json`, wantErr: "SMSBower 价格响应无法识别: not json"},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			f := newFakeAPI(t, tc.body)
			got, err := f.client(t).GetPriceQuote(context.Background(), "dr", "33")
			if tc.wantErr != "" {
				if errText(err) != tc.wantErr {
					t.Errorf("err = %q, python %q", errText(err), tc.wantErr)
				}
				return
			}
			if err != nil || got.Cost != tc.wantCost || got.Count != tc.wantCount {
				t.Errorf("got (%+v, %v), python {%v %v}", got, err, tc.wantCost, tc.wantCount)
			}
		})
	}
}

// TestGetPriceQuoteStripsTheLookupKey documents a DELIBERATE divergence:
// app.py:3810-3811 looked the reply up with the raw " 33 " while app.py:3803-3804
// had asked the API for "33", so a padded country could never find its own quote.
func TestGetPriceQuoteStripsTheLookupKey(t *testing.T) {
	f := newFakeAPI(t, `{"33":{"dr":{"cost":1,"count":1}}}`)
	got, err := f.client(t).GetPriceQuote(context.Background(), " dr ", " 33 ")
	if err != nil {
		t.Fatalf("Go must find the quote Python could not: %v", err)
	}
	if got.Cost != 1 || got.Count != 1 {
		t.Errorf("got %+v, want {1 1}", got)
	}
}

// TestGetPriceTiersMatchesPython pins the tier walk that decides WHAT PRICE the
// rental is attempted at (phoneprovider.rentNumber walks this slice in order).
func TestGetPriceTiersMatchesPython(t *testing.T) {
	cases := []struct {
		body    string
		want    []PriceTier
		wantErr string
	}{
		{body: `{"33":{"dr":{"0.10":5,"0.05":3,"0.20":0}}}`,
			want: []PriceTier{{0.05, 3}, {0.1, 5}}},
		{body: `{"33":{"dr":{"0.05":"3","0.10":"5"}}}`,
			want: []PriceTier{{0.05, 3}, {0.1, 5}}},
		// int(3.9) is 3 (app.py:3837). Dropping the tier — which strconv.Atoi
		// forced — would have sent the rental to the DEARER tier.
		{body: `{"33":{"dr":{"0.05":3.9,"0.10":5}}}`,
			want: []PriceTier{{0.05, 3}, {0.1, 5}}},
		{body: `{"33":{"dr":{"0.05":null,"0.10":5}}}`, want: []PriceTier{{0.1, 5}}},
		{body: `{"33":{"dr":{"0.05":"","0.10":5}}}`, want: []PriceTier{{0.1, 5}}},
		{body: `{"33":{"dr":{"0.05":"abc","0.10":5}}}`, want: []PriceTier{{0.1, 5}}},
		{body: `{"33":{"dr":{"abc":5,"0.10":5}}}`, want: []PriceTier{{0.1, 5}}},
		{body: `{"33":{"dr":{"1e-2":5,"0.10":5}}}`, want: []PriceTier{{0.01, 5}, {0.1, 5}}},
		{body: `{"33":{"dr":{" 0.05 ":5}}}`, want: []PriceTier{{0.05, 5}}},
		{body: `{"33":{"dr":{"0.05":-3}}}`, want: []PriceTier{}},
		{body: `{"33":{"dr":{}}}`, want: []PriceTier{}},
		{body: `{"33":{"dr":[]}}`, wantErr: "SMSBower 分档价格响应缺少 33/dr: " + `{"33":{"dr":[]}}`},
		{body: `{"33":{}}`, wantErr: "SMSBower 分档价格响应缺少 33/dr: " + `{"33":{}}`},
		{body: `not json`, wantErr: "SMSBower 分档价格响应无法识别: not json"},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			f := newFakeAPI(t, tc.body)
			got, err := f.client(t).GetPriceTiers(context.Background(), "dr", "33")
			if tc.wantErr != "" {
				if errText(err) != tc.wantErr {
					t.Errorf("err = %q, python %q", errText(err), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, python %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("tier %d = %v, python %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestGetPriceTiersOrderIsDeterministic pins the two properties Go does not get
// for free: JSON key order (Go map iteration is randomised) and a STABLE sort
// (Python's list.sort keeps equal costs in insertion order). Three keys that all
// parse to 0.1 must come back 5, 7, 9 EVERY time — this loop fails within a few
// iterations if either property is lost, and the order reaches the user in the
// "可用档=" log line (app.py:16435).
func TestGetPriceTiersOrderIsDeterministic(t *testing.T) {
	const body = `{"33":{"dr":{"0.1":5,"0.10":7,"0.100":9,"0.05":1}}}`
	want := []PriceTier{{0.05, 1}, {0.1, 5}, {0.1, 7}, {0.1, 9}}
	for run := 0; run < 50; run++ {
		f := newFakeAPI(t, body)
		got, err := f.client(t).GetPriceTiers(context.Background(), "dr", "33")
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("run %d: tier %d = %v, python %v (full: %v)", run, i, got[i], want[i], got)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// wait_for_code (app.py:3853-3875)
// ---------------------------------------------------------------------------

func TestWaitForCodeMatchesPython(t *testing.T) {
	t.Run("code on the first poll", func(t *testing.T) {
		noSleep(t)
		f := newFakeAPI(t, "STATUS_OK:123456")
		got, err := f.client(t).WaitForCode(context.Background(), "ID", 180, 5)
		if err != nil || got != "123456" {
			t.Errorf("got (%q, %v), python 123456", got, err)
		}
		if f.calls != 1 {
			t.Errorf("%d polls, python 1", f.calls)
		}
	})
	t.Run("polls through STATUS_WAIT_CODE", func(t *testing.T) {
		slept := noSleep(t)
		f := newFakeAPI(t, "STATUS_WAIT_CODE", "STATUS_WAIT_CODE", "STATUS_OK:99 88 77")
		got, err := f.client(t).WaitForCode(context.Background(), "ID", 180, 5)
		if err != nil || got != "998877" {
			t.Errorf("got (%q, %v), python 998877", got, err)
		}
		if f.calls != 3 {
			t.Errorf("%d polls, python 3", f.calls)
		}
		if len(*slept) != 2 || (*slept)[0] != 5*time.Second || (*slept)[1] != 5*time.Second {
			t.Errorf("slept %v, python [5s 5s]", *slept)
		}
	})
	t.Run("STATUS_CANCEL", func(t *testing.T) {
		noSleep(t)
		f := newFakeAPI(t, "STATUS_CANCEL")
		_, err := f.client(t).WaitForCode(context.Background(), "ID", 180, 5)
		if errText(err) != "SMSBower 激活已取消" {
			t.Errorf("err = %q", errText(err))
		}
	})
	t.Run("an unknown status aborts and echoes status:value", func(t *testing.T) {
		noSleep(t)
		f := newFakeAPI(t, "STATUS_WHAT:zzz")
		_, err := f.client(t).WaitForCode(context.Background(), "ID", 180, 5)
		if errText(err) != "SMSBower 激活状态无法识别: STATUS_WHAT:zzz" {
			t.Errorf("err = %q", errText(err))
		}
	})
	t.Run("STATUS_OK with no digits", func(t *testing.T) {
		noSleep(t)
		f := newFakeAPI(t, "STATUS_OK:no digits here")
		_, err := f.client(t).WaitForCode(context.Background(), "ID", 180, 5)
		if errText(err) != "SMSBower 已收到短信，但未解析出验证码: no digits here" {
			t.Errorf("err = %q", errText(err))
		}
	})
	// DIVERGENCE FIXED: re.sub(r"\D+") keeps every Unicode decimal digit. With
	// RE2's ASCII \D this became "no code parsed" for a number already paid for.
	t.Run("STATUS_OK with full-width digits", func(t *testing.T) {
		noSleep(t)
		f := newFakeAPI(t, "STATUS_OK:１２３４５６")
		got, err := f.client(t).WaitForCode(context.Background(), "ID", 180, 5)
		if err != nil || got != "１２３４５６" {
			t.Errorf("got (%q, %v), python １２３４５６", got, err)
		}
	})
	t.Run("a hard error propagates unretried", func(t *testing.T) {
		noSleep(t)
		f := newFakeAPI(t, "NO_ACTIVATION")
		_, err := f.client(t).WaitForCode(context.Background(), "ID", 180, 5)
		if errText(err) != "激活 ID 不存在 (NO_ACTIVATION)" {
			t.Errorf("err = %q", errText(err))
		}
		if f.calls != 1 {
			t.Errorf("%d polls, python 1", f.calls)
		}
	})
	// max(1, int(timeout)) and max(1, int(interval)) (app.py:3854/3860): a
	// timeout of 0 or -5 still makes exactly one poll and one 1s sleep.
	t.Run("timeout and interval are clamped to 1", func(t *testing.T) {
		for _, timeout := range []int{0, -5} {
			slept := noSleep(t)
			f := newFakeAPI(t, "STATUS_WAIT_CODE")
			_, err := f.client(t).WaitForCode(context.Background(), "ID", timeout, 5)
			if !strings.HasPrefix(errText(err), "等待 SMSBower 验证码超时，最后状态: STATUS_WAIT_CODE") {
				t.Errorf("timeout=%d err = %q", timeout, errText(err))
			}
			if f.calls != 1 {
				t.Errorf("timeout=%d: %d polls, python 1", timeout, f.calls)
			}
			if len(*slept) != 1 || (*slept)[0] != time.Second {
				t.Errorf("timeout=%d slept %v, python [1s]", timeout, *slept)
			}
		}
	})
	t.Run("the timeout message reports 无 when nothing was ever read", func(t *testing.T) {
		c, err := NewClient("KEY", "http://127.0.0.1:1", "")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = c.WaitForCode(ctx, "ID", 1, 1)
		if err == nil {
			t.Fatal("want an error from a cancelled context")
		}
	})
}

// TestWaitForCodeDeadlineArithmetic spends one real second to pin the
// min(interval, deadline-now) cap of app.py:3861/3874 against the wall clock.
func TestWaitForCodeDeadlineArithmetic(t *testing.T) {
	var slept []time.Duration
	prev := sleepFor
	sleepFor = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return sleepCtx(ctx, d)
	}
	t.Cleanup(func() { sleepFor = prev })

	f := newFakeAPI(t, "STATUS_WAIT_CODE")
	_, err := f.client(t).WaitForCode(context.Background(), "ID", 1, 5)
	if !strings.HasPrefix(errText(err), "等待 SMSBower 验证码超时") {
		t.Fatalf("err = %q", errText(err))
	}
	if f.calls != 1 {
		t.Errorf("%d polls, python 1", f.calls)
	}
	// The interval is 5s but only ~1s of the deadline is left, so the sleep is
	// capped: min(5, deadline-now).
	if len(slept) != 1 || slept[0] > time.Second {
		t.Errorf("slept %v, python caps at <=1s", slept)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// TestTruncateCountsCodePoints pins Python's text[:n]. Byte slicing kept a
// different amount of text and could split a UTF-8 sequence in the middle of a
// Chinese error message the user reads.
func TestTruncateCountsCodePoints(t *testing.T) {
	long := strings.Repeat("错", 400)
	got := truncate(long, 300)
	if n := len([]rune(got)); n != 300 {
		t.Errorf("truncate kept %d code points, python keeps 300", n)
	}
	if !strings.HasPrefix(long, got) || strings.ContainsRune(got, '�') {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
	mixed := "E" + strings.Repeat("错", 400)
	got = truncate(mixed, 300)
	if n := len([]rune(got)); n != 300 {
		t.Errorf("truncate kept %d code points, python keeps 300", n)
	}
	if truncate("abc", 300) != "abc" {
		t.Error("short strings must pass through")
	}
	if truncate("", 300) != "" {
		t.Error("empty must pass through")
	}
}

// TestPyStripMatchesPython pins str.strip()'s 29 code points. Go's
// strings.TrimSpace omits U+001C..U+001F.
func TestPyStripMatchesPython(t *testing.T) {
	all := "\t\n\v\f\r\x1c\x1d\x1e\x1f   " +
		"           " +
		"    　"
	if n := len([]rune(all)); n != 29 {
		t.Fatalf("the fixture has %d code points, python strips 29", n)
	}
	if got := pyStrip(all + "x" + all); got != "x" {
		t.Errorf("pyStrip = %q, want \"x\"", got)
	}
	// U+200B ZERO WIDTH SPACE is NOT whitespace to Python.
	if got := pyStrip("​x​"); got != "​x​" {
		t.Errorf("pyStrip ate U+200B: %q", got)
	}
}

func TestCapToDeadline(t *testing.T) {
	now := time.Now()
	if got := capToDeadline(5*time.Second, now.Add(-time.Second)); got != 0 {
		t.Errorf("a passed deadline caps to 0, got %v (python: max(0, ...))", got)
	}
	if got := capToDeadline(time.Second, now.Add(time.Hour)); got != time.Second {
		t.Errorf("got %v, want the interval", got)
	}
}
