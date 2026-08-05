package proxyhealth

import "testing"

func TestNormalizeProxyURL(t *testing.T) {
	cases := map[string]string{
		"":                         "",
		"  ":                       "",
		"127.0.0.1:7890":           "http://127.0.0.1:7890",
		"http://127.0.0.1:7890":    "http://127.0.0.1:7890",
		"https://u:p@host:8080":    "https://u:p@host:8080",
		"socks5://127.0.0.1:1080":  "socks5h://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080": "socks5h://127.0.0.1:1080",
		"SOCKS5://127.0.0.1:1080":  "socks5h://127.0.0.1:1080",
	}
	for in, want := range cases {
		if got := normalizeProxyURL(in, "http"); got != want {
			t.Fatalf("normalizeProxyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSocksScheme(t *testing.T) {
	yes := []string{"socks5://x", "socks5h://x", "SOCKS5H://x"}
	no := []string{"http://x", "https://x", "", "x"}
	for _, u := range yes {
		if !isSocksScheme(u) {
			t.Fatalf("isSocksScheme(%q) = false, want true", u)
		}
	}
	for _, u := range no {
		if isSocksScheme(u) {
			t.Fatalf("isSocksScheme(%q) = true, want false", u)
		}
	}
}
