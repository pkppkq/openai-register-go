package tlsclient

import (
	"testing"

	http "github.com/bogdanfinn/fhttp"
)

// A client with no cookie jar drops every Set-Cookie, so each request looks like
// a first visit — which breaks any ported flow that assumed curl_cffi.Session
// semantics. This asserts the jar is actually installed, not just requested.
func TestNewInstallsACookieJar(t *testing.T) {
	c, err := New("", 5)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.HTTP.GetCookieJar() == nil {
		t.Fatal("no cookie jar: every Set-Cookie would be silently dropped")
	}
}

// Header ORDER is part of the fingerprint. A caller that supplies its own
// HeaderOrderKey replaces the default, and anything it did not name would
// otherwise go out in Go map-iteration position — different on every request.
func TestCompleteHeaderOrderIsDeterministicAndStable(t *testing.T) {
	build := func() http.Header {
		h := http.Header{
			"sec-ch-ua":         {"x"},
			"user-agent":        {"x"},
			"accept":            {"x"},
			"accept-encoding":   {"x"},
			"authorization":     {"x"},
			"oai-device-id":     {"x"},
			http.HeaderOrderKey: {"authorization", "user-agent"},
		}
		completeHeaderOrder(h)
		return h
	}

	first := build()
	order := first[http.HeaderOrderKey]
	// The caller's names keep their exact positions, at the front.
	if len(order) < 2 || order[0] != "authorization" || order[1] != "user-agent" {
		t.Fatalf("caller order was not preserved: %v", order)
	}
	// Everything else is present exactly once, so nothing rides along unordered.
	seen := map[string]int{}
	for _, k := range order {
		seen[k]++
	}
	for _, want := range []string{"sec-ch-ua", "accept", "accept-encoding", "oai-device-id"} {
		if seen[want] != 1 {
			t.Errorf("%q appears %d times in the order, want exactly 1: %v", want, seen[want], order)
		}
	}
	if seen[http.HeaderOrderKey] != 0 {
		t.Errorf("the order key ordered itself: %v", order)
	}

	// Rebuilding must give byte-identical order — that is the whole point.
	for i := 0; i < 20; i++ {
		got := build()[http.HeaderOrderKey]
		if len(got) != len(order) {
			t.Fatalf("run %d: length changed: %v vs %v", i, got, order)
		}
		for j := range order {
			if got[j] != order[j] {
				t.Fatalf("run %d: order is not stable: %v vs %v", i, got, order)
			}
		}
	}
}

// No HeaderOrderKey means the caller is not ordering anything; leave it alone
// rather than inventing an order they did not ask for.
func TestCompleteHeaderOrderLeavesUnorderedHeadersAlone(t *testing.T) {
	h := http.Header{"accept": {"x"}, "user-agent": {"y"}}
	completeHeaderOrder(h)
	if _, ok := h[http.HeaderOrderKey]; ok {
		t.Errorf("an order key was invented: %v", h[http.HeaderOrderKey])
	}
}

// The client hint must not contradict the user-agent: the UA claims Chrome, so
// sec-ch-ua has to claim Chrome too (app.py:5592).
func TestChromeHeadersMatchPython(t *testing.T) {
	c := &Client{UserAgent: "ua"}
	h := c.ChromeHeaders()
	// Direct map access, NOT Header.Get: the keys are deliberately lowercase (the
	// wire casing Chrome uses) and Get would canonicalise to "Sec-Ch-Ua" and miss.
	if got := h["sec-ch-ua"]; len(got) != 1 || got[0] != `"Google Chrome";v="146", "Chromium";v="146", "Not.A/Brand";v="24"` {
		t.Errorf("sec-ch-ua = %v", got)
	}
	if got := h["accept-language"]; len(got) != 1 || got[0] != "zh-CN,zh;q=0.9,en;q=0.8" {
		t.Errorf("accept-language = %v", got)
	}
	// Every default header is named in the default order.
	order := map[string]bool{}
	for _, k := range h[http.HeaderOrderKey] {
		order[k] = true
	}
	for k := range h {
		if k == http.HeaderOrderKey {
			continue
		}
		if !order[k] {
			t.Errorf("default header %q is not in the default order", k)
		}
	}
}
