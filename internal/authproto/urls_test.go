package authproto

import (
	"reflect"
	"testing"
)

// Expected values in this file come from running app.py's own helpers under
// this machine's CPython 3.12.0.

// ---------------------------------------------------------------------------
// token_debug_fingerprint (app.py:5495-5497)
// ---------------------------------------------------------------------------

func TestTokenDebugFingerprint(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "-"}, // the `if value else "-"` arm
		{"abc", "ba7816bf8f01"},
		{"eyJhbGciOiJIUzI1NiJ9.e30.sig", "04d5a59fb509"},
		{"中文", "72726d8818f6"},
	}
	for _, c := range cases {
		if got := TokenDebugFingerprint(c.in); got != c.want {
			t.Errorf("TokenDebugFingerprint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// normalize_auth_continue_url (app.py:5500-5504)
// ---------------------------------------------------------------------------

func TestNormalizeAuthContinueURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"\t\n", ""},
		{"/x", "https://auth.openai.com/x"},
		{"  /email-verification  ", "https://auth.openai.com/email-verification"},
		{"http://a/b", "http://a/b"},
		{"https://a/b", "https://a/b"},
		// "httpfoo" literally starts with "http", so it is passed through
		// unresolved — a Python quirk, not a bug to fix.
		{"httpfoo", "httpfoo"},
		// Protocol-relative: urljoin adopts the base scheme.
		{"//cdn.example.com/x", "https://cdn.example.com/x"},
		// No path on the base, so no "/" is inserted before the query.
		{"?a=b", "https://auth.openai.com?a=b"},
		{"log-in/password", "https://auth.openai.com/log-in/password"},
		{"https://auth.openai.com/x?y=1#z", "https://auth.openai.com/x?y=1#z"},
		// The startswith("http") test is CASE SENSITIVE, so this goes through
		// urljoin, which lowercases the SCHEME but leaves the HOST alone.
		{"hTTps://A/B", "https://A/B"},
		// NBSP: Python's str.strip() removes it, Go's unicode.IsSpace agrees,
		// but a naive strings.Trim(" ") would not.
		{"\u00a0/about-you\u00a0", "https://auth.openai.com/about-you"},
		{"\u3000", ""},
	}
	for _, c := range cases {
		if got := NormalizeAuthContinueURL(c.in); got != c.want {
			t.Errorf("NormalizeAuthContinueURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// is_transient_http_error (app.py:5604-5628) — the FULL marker list
// ---------------------------------------------------------------------------

func TestIsTransientHTTPErrorEveryMarker(t *testing.T) {
	// One case per marker, in the tuple's own order (app.py:5606-5627).
	markers := []string{
		"empty reply from server",
		"curl: (52)",
		"curl: (56)",
		"curl: (28)",
		"curl: (35)",
		"curl: (18)",
		"connection aborted",
		"connection reset",
		"connectionreseterror",
		"10054",
		"远程主机强迫关闭",
		"forcibly closed",
		"timed out",
		"timeout",
		"broken pipe",
		"remote end closed",
		"ssl",
		"tls",
		"eof occurred",
		"failed to perform",
	}
	if len(markers) != len(transientHTTPErrorMarkers) {
		t.Fatalf("marker list has %d entries, app.py has %d", len(transientHTTPErrorMarkers), len(markers))
	}
	for i, marker := range markers {
		if transientHTTPErrorMarkers[i] != marker {
			t.Errorf("marker %d = %q, want %q", i, transientHTTPErrorMarkers[i], marker)
		}
		// Present, and present under an upper-cased spelling (Python casefolds).
		for _, form := range []string{marker, "prefix " + marker + " suffix", upperASCII(marker)} {
			if !IsTransientHTTPError(form) {
				t.Errorf("IsTransientHTTPError(%q) = false, want true", form)
			}
		}
	}
	for _, notTransient := range []string{
		"",
		"400 code=phone_number_in_use",
		"invalid credentials",
		"账号已停用",
		"wrong_email_otp_code",
	} {
		if IsTransientHTTPError(notTransient) {
			t.Errorf("IsTransientHTTPError(%q) = true, want false", notTransient)
		}
	}
	// "ssl"/"tls" are substrings, so anything containing them counts — that
	// looseness is Python's and is preserved deliberately.
	if !IsTransientHTTPError("HTTP 500 from tlsproxy") {
		t.Error(`substring marker "tls" must still fire inside a longer word`)
	}
}

func TestIsSMSTransientErrorIsADifferentList(t *testing.T) {
	// SMSBowerClient.TRANSIENT_MARKERS (app.py:3720-3736) adds two markers the
	// HTTP list lacks and drops the curl codes; the two must not be merged.
	for _, only := range []string{"temporarily unavailable", "max retries exceeded"} {
		if !isSMSTransientError(only) {
			t.Errorf("isSMSTransientError(%q) = false", only)
		}
		if IsTransientHTTPError(only) {
			t.Errorf("IsTransientHTTPError(%q) = true, but that marker is SMSBower-only", only)
		}
	}
	for _, only := range []string{"curl: (52)", "eof occurred", "failed to perform"} {
		if isSMSTransientError(only) {
			t.Errorf("isSMSTransientError(%q) = true, but that marker is HTTP-only", only)
		}
	}
}

func upperASCII(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// _format_error_response (app.py:8039-8051)
// ---------------------------------------------------------------------------

func TestFormatErrorResponse(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			// error.code with a known phone reason -> the localized suffix.
			name:   "known phone code",
			status: 400,
			body:   `{"error":{"code":"phone_number_in_use"}}`,
			want:   "400 code=phone_number_in_use (手机号已被 OpenAI 使用/绑定过，不能再用于此账号)",
		},
		{
			name:   "unknown code, no suffix",
			status: 403,
			body:   `{"error":{"code":"wrong_email_otp_code"}}`,
			want:   "403 code=wrong_email_otp_code",
		},
		{
			// `error` is a bare string, so Python assigns it straight to `code`.
			name:   "error is a string",
			status: 500,
			body:   `{"error":"rate_limit_exceeded"}`,
			want:   "500 code=rate_limit_exceeded (请求过于频繁，需稍后重试)",
		},
		{
			// `if code:` is Python truthiness: an empty code falls through.
			name:   "empty code falls through to body",
			status: 400,
			body:   `{"error":{"code":""}}`,
			want:   `400 body={"error":{"code":""}}`,
		},
		{
			// 0 is falsy in Python too.
			name:   "zero code falls through to body",
			status: 400,
			body:   `{"error":{"code":0}}`,
			want:   `400 body={"error":{"code":0}}`,
		},
		{
			name:   "no error member",
			status: 404,
			body:   `{"detail":"nope"}`,
			want:   `404 body={"detail":"nope"}`,
		},
		{
			name:   "not JSON at all",
			status: 502,
			body:   "<html>bad gateway</html>",
			want:   "502 body=<html>bad gateway</html>",
		},
		{
			// A JSON list is not a dict, so `error` is None.
			name:   "JSON list",
			status: 400,
			body:   `[1,2]`,
			want:   "400 body=[1,2]",
		},
		{
			name:   "empty body",
			status: 500,
			want:   "500 body=",
		},
		{
			// A numeric code stringifies to its literal, not to a float.
			name:   "numeric code",
			status: 400,
			body:   `{"error":{"code":10054}}`,
			want:   "400 code=10054",
		},
	}
	for _, c := range cases {
		resp := &Response{StatusCode: c.status, Body: []byte(c.body)}
		if got := formatErrorResponse(resp); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestFormatErrorResponseTruncatesByRunes(t *testing.T) {
	// body[:500] counts CHARACTERS; a byte slice would split a Chinese rune.
	body := ""
	for i := 0; i < 600; i++ {
		body += "错"
	}
	got := formatErrorResponse(&Response{StatusCode: 500, Body: []byte(body)})
	want := "500 body="
	for i := 0; i < 500; i++ {
		want += "错"
	}
	if got != want {
		t.Errorf("rune truncation wrong: got %d runes of body", len([]rune(got))-len([]rune("500 body=")))
	}
}

// ---------------------------------------------------------------------------
// _response_has_auth_challenge (app.py:8657-8667)
// ---------------------------------------------------------------------------

func TestResponseHasAuthChallenge(t *testing.T) {
	cases := []struct {
		name string
		url  string
		body string
		want bool
	}{
		{"clean", "https://auth.openai.com/log-in", "<html>sign in</html>", false},
		{"marker in body", "https://auth.openai.com/x", "<div>Just a Moment...</div>", true},
		{"marker in url", "https://challenges.cloudflare.com/x", "", true},
		{"turnstile widget", "https://auth.openai.com/x", `<div class="cf-turnstile">`, true},
		{"cf chl param", "https://auth.openai.com/x?__cf_chl_tk=1", "", true},
		{"cf-challenge", "https://auth.openai.com/x", "<!-- cf-challenge -->", true},
	}
	for _, c := range cases {
		got := responseHasAuthChallenge(&Response{URL: c.url, Body: []byte(c.body)})
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// The body is sliced to 2000 CHARACTERS before the scan, so a marker past
	// that point is invisible — reproduced deliberately.
	filler := ""
	for i := 0; i < 2000; i++ {
		filler += "中"
	}
	if responseHasAuthChallenge(&Response{URL: "https://auth.openai.com/x", Body: []byte(filler + "turnstile")}) {
		t.Error("a marker beyond body[:2000] must not be seen")
	}
	// 1990 CJK chars + the 9-char marker = 1999 chars, just inside the window.
	if !responseHasAuthChallenge(&Response{URL: "https://auth.openai.com/x", Body: []byte(filler[:3*1990] + "turnstile")}) {
		t.Error("a marker inside body[:2000] must be seen")
	}
}

// ---------------------------------------------------------------------------
// parse_qs / urlencode
// ---------------------------------------------------------------------------

func TestParseQSDropsBlankValues(t *testing.T) {
	// CPython urllib.parse.parse_qs with its default keep_blank_values=False.
	cases := []struct {
		in   string
		want map[string][]string
	}{
		{"code=&state=x", map[string][]string{"state": {"x"}}},
		{"code=abc&state=", map[string][]string{"code": {"abc"}}},
		{"code=a%20b&state=s", map[string][]string{"code": {"a b"}, "state": {"s"}}},
		{"flag&code=1&state=2", map[string][]string{"code": {"1"}, "state": {"2"}}},
		{"code=1&code=2&state=3", map[string][]string{"code": {"1", "2"}, "state": {"3"}}},
		{"", map[string][]string{}},
	}
	for _, c := range cases {
		if got := parseQS(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseQS(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestQueryPairsEncodePreservesOrder(t *testing.T) {
	// CPython urlencode of the same dict. url.Values.Encode() would sort the
	// keys and produce a different string.
	q := queryPairs{
		{"issuer", "https://auth.openai.com"},
		{"client_id", "app_EMoamEEZ73f0CkXaXp7hrann"},
		{"audience", "https://api.openai.com/v1"},
		{"scope", "openid email profile offline_access"},
		{"login_hint", "a+b@ex.com"},
	}
	const want = "issuer=https%3A%2F%2Fauth.openai.com&client_id=app_EMoamEEZ73f0CkXaXp7hrann&audience=https%3A%2F%2Fapi.openai.com%2Fv1&scope=openid+email+profile+offline_access&login_hint=a%2Bb%40ex.com"
	if got := q.Encode(); got != want {
		t.Errorf("Encode:\n got %q\nwant %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Python value helpers
// ---------------------------------------------------------------------------

func TestPyReprMatchesCPython(t *testing.T) {
	// CPython str({...}).
	got := pyRepr(newOrderedMap(
		"a", 1,
		"b", "x'y",
		"c", nil,
		"d", true,
		"e", []any{1, "z"},
	))
	const want = `{'a': 1, 'b': "x'y", 'c': None, 'd': True, 'e': [1, 'z']}`
	if got != want {
		t.Errorf("pyRepr:\n got %s\nwant %s", got, want)
	}
}

func TestPyTruthy(t *testing.T) {
	truthy := []any{"0", " ", 1, -1, 1.5, true, []any{nil}, newOrderedMap("k", nil)}
	falsy := []any{nil, "", 0, 0.0, false, []any{}, newOrderedMap()}
	for _, v := range truthy {
		if !pyTruthy(v) {
			t.Errorf("pyTruthy(%#v) = false, want true", v)
		}
	}
	for _, v := range falsy {
		if pyTruthy(v) {
			t.Errorf("pyTruthy(%#v) = true, want false", v)
		}
	}
}

func TestTruncRunesCountsCharacters(t *testing.T) {
	if got := truncRunes("中文测试abc", 4); got != "中文测试" {
		t.Errorf("truncRunes = %q", got)
	}
	if got := truncRunes("abc", 10); got != "abc" {
		t.Errorf("truncRunes = %q", got)
	}
	if got := truncRunes("abc", 0); got != "" {
		t.Errorf("truncRunes = %q", got)
	}
}
