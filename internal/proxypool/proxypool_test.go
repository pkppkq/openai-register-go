package proxypool

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// The two golden tables below are not hand-written expectations: they are the
// output of app.py's own normalize_proxy_url (app.py:2396) and
// parse_proxy_pool_text (app.py:2421), executed over these exact inputs by a
// verbatim copy of app.py:2202-2467. Several rows look wrong and are not — a
// bare "curl" normalizes to "http://curl", "1.2.3.4:8080:u:p extra" swallows
// " extra" into the percent-encoded password, and a line whose remainder
// mentions a curl flag falls through to whole-line normalization. Python wins;
// that is the point of the table.
func TestNormalizeProxyURLMatchesPython(t *testing.T) {
	cases := []struct {
		in_  string
		want string
	}{
		{in_: "", want: ""},
		{in_: "   ", want: ""},
		{in_: "1.2.3.4:8080", want: "http://1.2.3.4:8080"},
		{in_: "http://1.2.3.4:8080", want: "http://1.2.3.4:8080"},
		{in_: "socks5://1.2.3.4:1080", want: "socks5h://1.2.3.4:1080"},
		{in_: "SOCKS5://1.2.3.4:1080", want: "socks5h://1.2.3.4:1080"},
		{in_: "socks5h://1.2.3.4:1080", want: "socks5h://1.2.3.4:1080"},
		{in_: "1.2.3.4:8080:user:pass", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "1.2.3.4:8080:user:pa:ss", want: "http://user:pa%3Ass@1.2.3.4:8080"},
		{in_: "http://1.2.3.4:8080:user:pass", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "socks5://1.2.3.4:1080:user:pass", want: "socks5h://user:pass@1.2.3.4:1080"},
		{in_: "SOCKS5://1.2.3.4:1080:USER:pass", want: "socks5h://USER:pass@1.2.3.4:1080"},
		{in_: "1.2.3.4 8080 user pass", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "1.2.3.4:8080 user:pass", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "http 1.2.3.4:8080 user:pass", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "socks5 1.2.3.4 1080 user pass", want: "socks5h://user:pass@1.2.3.4:1080"},
		{in_: "socks5h 1.2.3.4:1080 user:pass", want: "socks5h://user:pass@1.2.3.4:1080"},
		{in_: "curl -x http://1.2.3.4:8080 -U user:pass https://example.com", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "curl.exe --proxy socks5://1.2.3.4:1080 --proxy-user 'u:p'", want: "socks5://u:p@1.2.3.4:1080"},
		{in_: "PS C:\\> curl -x http://1.2.3.4:8080 -U user:pass", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "`curl -x http://1.2.3.4:8080`", want: "http://1.2.3.4:8080"},
		{in_: "curl --socks5 1.2.3.4:1080 -u user:pass", want: "socks5h://user:pass@1.2.3.4:1080"},
		{in_: "curl --socks5-hostname=1.2.3.4:1080 --proxy-user=user:p@ss", want: "socks5h://user:p%40ss@1.2.3.4:1080"},
		{in_: "curl --socks5h 1.2.3.4:1080", want: "socks5h://1.2.3.4:1080"},
		{in_: "curl --socks5=1.2.3.4:1080", want: "socks5h://1.2.3.4:1080"},
		{in_: "curl -uuser:pass -x 1.2.3.4:8080", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "curl -Uuser:pass -x 1.2.3.4:8080", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "curl -x “http://1.2.3.4:8080” -U ‘user:pass’", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "1.2.3.4：8080", want: "http://1.2.3.4:8080"},
		{in_: "\u00a01.2.3.4:8080\u00a0", want: "http://1.2.3.4:8080"},
		{in_: "\u3000http://a:b@c:1", want: "http://a:b@c:1"},
		{in_: "ftp://1.2.3.4:21", want: "ftp://1.2.3.4:21"},
		{in_: "'http://1.2.3.4:8080'", want: "'http://1.2.3.4:8080'"},
		{in_: "host.example.com:3128:user:pass", want: "http://user:pass@host.example.com:3128"},
		{in_: "user:pass@1.2.3.4:8080", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "http://user:pass@1.2.3.4:8080", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "curl -x http://1.2.3.4:8080", want: "http://1.2.3.4:8080"},
		{in_: "curl", want: "http://curl"},
		{in_: "curl -x", want: "http://curl -x"},
		{in_: "1.2.3.4:80a0:user:pass", want: "http://1.2.3.4:80a0:user:pass"},
		{in_: "  \t", want: ""},
		{in_: "\x1fhttp://1.2.3.4:8080\x1f", want: "http://1.2.3.4:8080"},
		{in_: "http://1.2.3.4:8080/path?x=1#f", want: "http://1.2.3.4:8080/path?x=1#f"},
		{in_: "curl -x http://1.2.3.4:8080/path?x=1#f -U u:p", want: "http://u:p@1.2.3.4:8080/path?x=1#f"},
		{in_: "1.2.3.4:8080:user:", want: "http://user@1.2.3.4:8080"},
		{in_: "1.2.3.4:8080::pass", want: "http://1.2.3.4:8080::pass"},
		{in_: "curl -x 1.2.3.4:8080 -U 'us er:p@ss/w?d'", want: "http://us%20er:p%40ss%2Fw%3Fd@1.2.3.4:8080"},
		{in_: "curl -x 1.2.3.4:8080 -U \"a\\\"b:c\\\\d\"", want: "http://a%22b:c%5Cd@1.2.3.4:8080"},
		{in_: "socks5://a.b:1080:u:p", want: "socks5h://u:p@a.b:1080"},
		{in_: "HTTP://1.2.3.4:8080", want: "HTTP://1.2.3.4:8080"},
		{in_: "curl -x 1.2.3.4:8080 --proxy-user user", want: "http://user@1.2.3.4:8080"},
		{in_: "some text with curl inside -x http://1.2.3.4:8080", want: "http://1.2.3.4:8080"},
		{in_: "abc curl.exe -x http://1.2.3.4:8080", want: "http://1.2.3.4:8080"},
		{in_: "curl -x 'http://1.2.3.4:8080", want: "curl -x 'http://1.2.3.4:8080"},
		{in_: "1.2.3.4:8080 user:pass extra", want: "http://1.2.3.4:8080 user:pass extra"},
		{in_: "http://1.2.3.4:8080 http://5.6.7.8:9090", want: "http://http:%2F%2F5.6.7.8%3A9090@1.2.3.4:8080"},
		{in_: "代理curl -x http://1.2.3.4:8080", want: "代理curl -x http://1.2.3.4:8080"},
		{in_: "1.2.3.4:8080:user:pass:extra", want: "http://user:pass%3Aextra@1.2.3.4:8080"},
		{in_: "http://[::1]:8080", want: "http://[::1]:8080"},
		{in_: "user:pass@host:1080:x:y", want: "http://user:pass@host:1080:x:y"},
		{in_: "中文:8080:用户:密码", want: "http://%E7%94%A8%E6%88%B7:%E5%AF%86%E7%A0%81@中文:8080"},
		{in_: "1.2.3.4:8080:user:pass  ", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "http://1.2.3.4:8080\t", want: "http://1.2.3.4:8080"},
		{in_: "curl\t-x\thttp://1.2.3.4:8080", want: "http://1.2.3.4:8080"},
		{in_: "curl.exefoo -x http://1.2.3.4:8080", want: "curl.exefoo -x http://1.2.3.4:8080"},
		{in_: "-x http://1.2.3.4:8080", want: "-x http://1.2.3.4:8080"},
		{in_: "1.2.3.4", want: "http://1.2.3.4"},
		{in_: "localhost:7890", want: "http://localhost:7890"},
		{in_: "http://127.0.0.1:7890", want: "http://127.0.0.1:7890"},
		{in_: "curl -x \"http://a:1\" -U \"u\\\"x:p\"", want: "http://u%22x:p@a:1"},
		{in_: "curl -x http://a:1 -U u\\:p", want: "http://u:p@a:1"},
		{in_: "curl -x '' -U u:p", want: "http://curl -x '' -U u:p"},
		{in_: "curl -x http://a:1 -U ''", want: "http://a:1"},
		{in_: "curl -x http://a:1 -U \"\"", want: "http://a:1"},
		{in_: "curl --preproxy socks5://a:1 -x http://b:2", want: "http://b:2"},
		{in_: "curl -x http://a:1 --proxy http://c:3", want: "http://c:3"},
		{in_: "1.2.3.4:８０８０:u:p", want: "http://u:p@1.2.3.4:８０８０"},
		{in_: "1.2.3.4:8080²:u:p", want: "http://u:p@1.2.3.4:8080²"},
		{in_: "curl -x http://a:1 -U 'u'\"'\"'s:p'", want: "http://u%27s:p@a:1"},
		{in_: "curl \\n -x http://a:1", want: "http://a:1"},
		{in_: "curl -x \"socks5://1.2.3.4:1080\" -U \"user:p ass\"", want: "socks5://user:p%20ass@1.2.3.4:1080"},
		{in_: "  curl   -x   http://a:1   -U   u:p  ", want: "http://u:p@a:1"},
		{in_: "http://a:1 ", want: "http://a:1"},
		{in_: " 1.2.3.4 8080 user pass extra", want: "http://user:pass@1.2.3.4:8080"},
		{in_: "1.2.3.4 8080 user", want: "http://1.2.3.4 8080 user"},
		{in_: "https 1.2.3.4:8080 u:p", want: "https://u:p@1.2.3.4:8080"},
		{in_: "HTTPS://a:1", want: "HTTPS://a:1"},
		{in_: "socks5h://a:1:u:p", want: "socks5h://u:p@a:1"},
		{in_: "a:1:u:p:extra:more", want: "http://u:p%3Aextra%3Amore@a:1"},
		{in_: "::1:8080:u:p", want: "http://::1:8080:u:p"},
		{in_: "curl -x http://a:1 -U u:p:q", want: "http://u:p%3Aq@a:1"},
		{in_: "\"1.2.3.4:8080\"", want: "http://\"1.2.3.4:8080\""},
		{in_: "curl -x=http://a:1", want: "curl -x=http://a:1"},
		{in_: "curl --proxy=socks5://a:1", want: "socks5://a:1"},
		{in_: "‘socks5://a:1’", want: "'socks5://a:1'"},
		{in_: "http://a:1\u2009http://b:2", want: "http://a:1\u2009http://b:2"},
		{in_: "1.2.3.4\u20098080\u2009u\u2009p", want: "http://1.2.3.4\u20098080\u2009u\u2009p"},
		{in_: "curl -x http://a:1 -U u:p\u2009", want: "http://u:p@a:1"},
		{in_: "socks5://user:pass@host:1080", want: "socks5h://user:pass@host:1080"},
		{in_: "curl -x http://a:1 -U 'u:p@w/o?rd '", want: "http://u:p%40w%2Fo%3Frd@a:1"},
		{in_: "HTTP 1.2.3.4:8080 u:p", want: "http://u:p@1.2.3.4:8080"},
		{in_: "SOCKS5H 1.2.3.4:1080 u:p", want: "socks5h://u:p@1.2.3.4:1080"},
		{in_: "`` http://a:1 ``", want: " http://a:1 "},
		{in_: "1.2.3.4:8080:u:p\u3000", want: "http://u:p@1.2.3.4:8080"},
		{in_: "curl\u3000-x\u3000http://a:1", want: "http://a:1"},
		{in_: "\u2028http://a:1", want: "http://a:1"},
		{in_: "1.2.3.4:8080:用户:密码", want: "http://%E7%94%A8%E6%88%B7:%E5%AF%86%E7%A0%81@1.2.3.4:8080"},
		{in_: "curl -x http://a:1 -U 用户:密码", want: "http://%E7%94%A8%E6%88%B7:%E5%AF%86%E7%A0%81@a:1"},
		{in_: "1.2.3.4:0:u:p", want: "http://u:p@1.2.3.4:0"},
		{in_: "curl -x '' -U u:p", want: "http://curl -x '' -U u:p"},
		{in_: "curl --proxy-user= -x a:1", want: "http://a:1"},
		{in_: "a:1:u:p ", want: "http://u:p@a:1"},
		{in_: "\x1c\x1d1.2.3.4:8080\x1e\x1f", want: "http://1.2.3.4:8080"},
	}
	for _, tc := range cases {
		if got := NormalizeProxyURL(tc.in_); got != tc.want {
			t.Errorf("NormalizeProxyURL(%q)\n  got    %q\n  python %q", tc.in_, got, tc.want)
		}
	}
}

func TestParseProxyPoolTextMatchesPython(t *testing.T) {
	cases := []struct {
		in_  string
		want []string
	}{
		{in_: "", want: []string{}},
		{in_: "http://1.2.3.4:8080\nsocks5://5.6.7.8:1080\n\n1.2.3.4:3128:u:p", want: []string{"http://1.2.3.4:8080", "socks5h://5.6.7.8:1080", "http://u:p@1.2.3.4:3128"}},
		{in_: "http://a:1 http://b:2", want: []string{"http://a:1", "http://b:2"}},
		{in_: "http://1.2.3.4:8080 foo", want: []string{"http://1.2.3.4:8080"}},
		{in_: "http://1.2.3.4:8080 -x", want: []string{"http://1.2.3.4:8080"}},
		{in_: "http://1.2.3.4:8080 abc-x", want: []string{"http://1.2.3.4:8080 abc-x"}},
		{in_: "http://1.2.3.4:8080 --proxy-user", want: []string{"http://1.2.3.4:8080"}},
		{in_: "http://1.2.3.4:8080 x--proxy-user", want: []string{"http://1.2.3.4:8080 x--proxy-user"}},
		{in_: "curl -x http://1.2.3.4:8080 -U u:p", want: []string{"http://u:p@1.2.3.4:8080"}},
		{in_: "curl.exe -x http://1.2.3.4:8080", want: []string{"http://1.2.3.4:8080"}},
		{in_: "1.2.3.4:8080:u:p 5.6.7.8:9090:u2:p2", want: []string{"http://u:p@1.2.3.4:8080", "http://u2:p2@5.6.7.8:9090"}},
		{in_: "u:p@1.2.3.4:8080 u2:p2@5.6.7.8:9090", want: []string{"http://u:p@1.2.3.4:8080", "http://u2:p2@5.6.7.8:9090"}},
		{in_: "http://1.2.3.4:8080, http://5.6.7.8:9090;", want: []string{"http://1.2.3.4:8080", "http://5.6.7.8:9090"}},
		{in_: "\r\nhttp://a:1\r\n", want: []string{"http://a:1"}},
		{in_: "http://a:1\u2028http://b:2", want: []string{"http://a:1", "http://b:2"}},
		{in_: "http://a:1\x0bhttp://b:2", want: []string{"http://a:1", "http://b:2"}},
		{in_: "http://a:1\u0085http://b:2", want: []string{"http://a:1", "http://b:2"}},
		{in_: "代理curl 后缀", want: []string{"http://代理curl 后缀"}},
		{in_: "http://1.2.3.4:8080 代理curl", want: []string{"http://1.2.3.4:8080"}},
		{in_: "  \n \t \n", want: []string{}},
		{in_: "curl代理 -x http://1.2.3.4:8080", want: []string{"http://1.2.3.4:8080"}},
		{in_: "socks5://1.2.3.4:1080\nsocks5://1.2.3.4:1080", want: []string{"socks5h://1.2.3.4:1080", "socks5h://1.2.3.4:1080"}},
		{in_: "http://a:1 http://b:2 http://c:3", want: []string{"http://a:1", "http://b:2", "http://c:3"}},
		{in_: "1.2.3.4:8080", want: []string{"http://1.2.3.4:8080"}},
		{in_: "line with no proxy at all", want: []string{"http://line with no proxy at all"}},
		{in_: "http://a b", want: []string{"http://a"}},
		{in_: "  http://1.2.3.4:8080  ,  ", want: []string{"http://1.2.3.4:8080  ,"}},
		{in_: "curl -x http://1.2.3.4:8080\n1.2.3.4:3128:u:p\nhttp://5.6.7.8:9090 note", want: []string{"http://1.2.3.4:8080", "http://u:p@1.2.3.4:3128", "http://5.6.7.8:9090"}},
		{in_: "http://1.2.3.4:8080;http://5.6.7.8:9090", want: []string{"http://1.2.3.4:8080", "http://5.6.7.8:9090"}},
		{in_: "1.2.3.4 8080 u p\n5.6.7.8 9090 u2 p2", want: []string{"http://u:p@1.2.3.4:8080", "http://u2:p2@5.6.7.8:9090"}},
		{in_: "http://a:1\nhttp://a:1\nhttp://a:1", want: []string{"http://a:1", "http://a:1", "http://a:1"}},
		{in_: "socks5://1.2.3.4:1080 u:p", want: []string{"socks5h://1.2.3.4:1080"}},
		{in_: "\u00a0http://1.2.3.4:8080\u00a0\nhttp://5.6.7.8:9090", want: []string{"http://1.2.3.4:8080", "http://5.6.7.8:9090"}},
		{in_: "1.2.3.4：8080：user：pass", want: []string{"http://user:pass@1.2.3.4:8080"}},
		{in_: "http://1.2.3.4:8080  http://1.2.3.4:8080", want: []string{"http://1.2.3.4:8080", "http://1.2.3.4:8080"}},
		{in_: "curl -x http://a:1 -U u:p\ncurl.exe --proxy socks5://b:2", want: []string{"http://u:p@a:1", "socks5://b:2"}},
		{in_: "http://a:1 http://b:2\nhttp://c:3 note\ncurl -x http://d:4", want: []string{"http://a:1", "http://b:2", "http://c:3", "http://d:4"}},
		{in_: "a:1:u:p\nb:2:u:p\n\n\n", want: []string{"http://u:p@a:1", "http://u:p@b:2"}},
		{in_: "http://a:1;http://b:2;http://c:3", want: []string{"http://a:1", "http://b:2", "http://c:3"}},
		{in_: "１.2.3.4:8080", want: []string{"http://１.2.3.4:8080"}},
		{in_: "http://a:1 -U", want: []string{"http://a:1"}},
		{in_: "http://a:1 u--socks5", want: []string{"http://a:1 u--socks5"}},
		{in_: "http://a:1 socks5://b:2 note", want: []string{"http://a:1", "socks5h://b:2"}},
		{in_: "u:p@a:1 note", want: []string{"http://u:p@a:1 note"}},
		{in_: "u:p@a:1", want: []string{"http://u:p@a:1"}},
		{in_: "1.2.3.4:8080:u:p extra", want: []string{"http://u:p%20extra@1.2.3.4:8080"}},
		{in_: "curl", want: []string{"http://curl"}},
		{in_: "curl.exe", want: []string{"http://curl.exe"}},
		{in_: "curlx -x http://a:1", want: []string{"http://a:1"}},
		{in_: "http://a:1\rhttp://b:2", want: []string{"http://a:1", "http://b:2"}},
		{in_: "\x1chttp://a:1\x1dhttp://b:2", want: []string{"http://a:1", "http://b:2"}},
		{in_: "http://a:1\u2009http://b:2", want: []string{"http://a:1", "http://b:2"}},
		{in_: "http://a:1\u2029http://b:2", want: []string{"http://a:1", "http://b:2"}},
		{in_: "http://a:1\u3000http://b:2", want: []string{"http://a:1", "http://b:2"}},
		{in_: "http://a:1 socks5://\n", want: []string{"http://a:1"}},
		{in_: "http://a:1 http://：", want: []string{"http://a:1", "http://:"}},
		{in_: ", , ,", want: []string{"http://, , ,"}},
		{in_: "\u3000", want: []string{}},
		{in_: "1.2.3.4:8080:u:p\ta:2:u:p", want: []string{"http://u:p@1.2.3.4:8080", "http://u:p@a:2"}},
		{in_: "curl -x http://a:1 -U u:p note", want: []string{"http://u:p@a:1"}},
		{in_: "http://a:1 curl", want: []string{"http://curl"}},
		{in_: "http://a:1 abc--socks5h", want: []string{"http://a:1 abc--socks5h"}},
		{in_: "u:p@a:1 u2:p2@b:2 c:3:u:p", want: []string{"http://u:p@a:1", "http://u2:p2@b:2", "http://u:p@c:3"}},
		{in_: "http://a:1#frag http://b:2?q=1", want: []string{"http://a:1#frag", "http://b:2?q=1"}},
	}
	for _, tc := range cases {
		got := ParseProxyPoolText(tc.in_)
		if len(got) != len(tc.want) {
			t.Errorf("ParseProxyPoolText(%q)\n  got    %#v\n  python %#v", tc.in_, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("ParseProxyPoolText(%q)[%d]\n  got    %q\n  python %q", tc.in_, i, got[i], tc.want[i])
			}
		}
	}
}

// Python's re \s is Unicode-aware; RE2's is [\t\n\f\r ]. Every pattern in this
// package spells the class out instead, and a thin space (U+2009 — one of the
// separators _clean_proxy_input does NOT fold to " ") is what tells the two
// apart: with an ASCII \s the first URL match would run on into the second.
func TestUnicodeWhitespaceSplitsLikePython(t *testing.T) {
	got := ParseProxyPoolText("http://a:1\u2009http://b:2")
	want := []string{"http://a:1", "http://b:2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("thin space: got %#v want %#v", got, want)
	}
	// U+2028/U+2029/U+000B/U+001C..U+001E are line breaks to str.splitlines()
	// but not to strings.Split(s, "\n").
	for _, sep := range []string{"\u2028", "\u2029", "\v", "\x1c", "\x1d", "\x1e", "\u0085", "\r\n"} {
		if n := len(ParseProxyPoolText("http://a:1" + sep + "http://b:2")); n != 2 {
			t.Errorf("separator %q: got %d lines, want 2", sep, n)
		}
	}
}

// str.isdigit() accepts Numeric_Type=Digit, which is wider than Go's Nd-only
// unicode.IsDigit. app.py feeds it straight into the port test, so a port
// written with a superscript digit is a port to Python and must be one here too.
func TestPortDigitTestMatchesPythonIsdigit(t *testing.T) {
	if got := NormalizeProxyURL("1.2.3.4:8080\u00b2:u:p"); got != "http://u:p@1.2.3.4:8080\u00b2" {
		t.Errorf("superscript port: got %q", got)
	}
	// Full-width digits are Nd, so both languages already agree on them.
	if got := NormalizeProxyURL("1.2.3.4:\uff18\uff10\uff18\uff10:u:p"); got != "http://u:p@1.2.3.4:\uff18\uff10\uff18\uff10" {
		t.Errorf("full-width port: got %q", got)
	}
	// A hex-looking port is not a port in either language.
	if got := NormalizeProxyURL("1.2.3.4:80a0:user:pass"); got != "http://1.2.3.4:80a0:user:pass" {
		t.Errorf("non-digit port: got %q", got)
	}
}

func poolWith(t *testing.T, lines ...string) *Pool {
	t.Helper()
	p := NewPool(strings.Join(lines, "\n"))
	if p.Remaining() != len(lines) {
		t.Fatalf("setup: pool has %d entries, want %d", p.Remaining(), len(lines))
	}
	return p
}

// Take is _rotate_proxy_pool_values (app.py:17316): the head moves to the TAIL,
// so a pool cycles forever and never shrinks. A queue that consumed its head
// would run a batch out of proxies after one pass.
func TestTakeCyclesAndDoesNotConsume(t *testing.T) {
	p := poolWith(t, "http://a:1", "http://b:2", "http://c:3")
	var got []string
	for i := 0; i < 7; i++ {
		got = append(got, p.Take())
	}
	want := "http://a:1|http://b:2|http://c:3|http://a:1|http://b:2|http://c:3|http://a:1"
	if strings.Join(got, "|") != want {
		t.Errorf("rotation order:\n got  %s\n want %s", strings.Join(got, "|"), want)
	}
	if p.Remaining() != 3 {
		t.Errorf("pool shrank to %d", p.Remaining())
	}
}

func TestTakeNSemantics(t *testing.T) {
	cases := []struct {
		name      string
		n         int
		wantTaken string
		wantOrder string
	}{
		{"zero", 0, "", "http://a:1|http://b:2|http://c:3"},
		{"negative", -2, "", "http://a:1|http://b:2|http://c:3"},
		{"two", 2, "http://a:1|http://b:2", "http://c:3|http://a:1|http://b:2"},
		// count > len: Python's min() clamp yields every entry exactly once and
		// rest = proxies[len:] + taken leaves the order untouched.
		{"overdraw", 9, "http://a:1|http://b:2|http://c:3", "http://a:1|http://b:2|http://c:3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := poolWith(t, "http://a:1", "http://b:2", "http://c:3")
			if got := strings.Join(p.TakeN(tc.n), "|"); got != tc.wantTaken {
				t.Errorf("taken = %q, want %q", got, tc.wantTaken)
			}
			if got := strings.Join(p.List(), "|"); got != tc.wantOrder {
				t.Errorf("order = %q, want %q", got, tc.wantOrder)
			}
		})
	}
}

func TestEmptyPoolReads(t *testing.T) {
	p := &Pool{}
	if got := p.Take(); got != "" {
		t.Errorf("Take on empty = %q", got)
	}
	if got := p.Peek(); got != "" {
		t.Errorf("Peek on empty = %q", got)
	}
	if got := p.TakeN(3); got != nil {
		t.Errorf("TakeN on empty = %#v", got)
	}
	if got := p.Text(); got != "" {
		t.Errorf("Text on empty = %q", got)
	}
}

// Entries are stored normalized, so a removal request spelled any of the ways
// normalize_proxy_url accepts still matches the stored line — that is exactly
// what _remove_register_dynamic_proxy_value (app.py:17342) does by normalizing
// both sides. It also stops after the FIRST hit, keeping later duplicates.
func TestRemoveMatchesNormalizedAndOnlyOnce(t *testing.T) {
	p := poolWith(t, "1.2.3.4:8080:u:p", "http://u:p@1.2.3.4:8080", "http://b:2")
	if !p.Remove("1.2.3.4 8080 u p") {
		t.Fatal("Remove returned false for an equivalent spelling")
	}
	if got := strings.Join(p.List(), "|"); got != "http://u:p@1.2.3.4:8080|http://b:2" {
		t.Errorf("after Remove: %q", got)
	}
	if p.Remove("") {
		t.Error("Remove(\"\") reported a removal")
	}
	if p.Remove("http://nothere:1") {
		t.Error("Remove of an absent proxy reported a removal")
	}
}

// 清理无效代理 prunes a dead proxy from all four pools at once
// (_remove_dynamic_proxy_values_everywhere, app.py:17365) — not just from the
// pool it was taken from — and removes every duplicate, unlike Remove.
func TestRemoveEverywhereSpansAllFourPools(t *testing.T) {
	s := NewSet()
	s.SetText(RoleRegister, "http://a:1\nhttp://a:1\nhttp://b:2")
	s.SetText(RoleCreate, "http://a:1")
	s.SetText(RoleFollowup, "http://c:3")
	s.SetText(RoleApprove, "1.2.3.4:8080:u:p")

	counts, total := s.RemoveEverywhere([]string{"http://a:1", "1.2.3.4 8080 u p", "", "   "})
	if total != 4 {
		t.Errorf("total removed = %d, want 4", total)
	}
	want := map[Role]int{RoleRegister: 2, RoleCreate: 1, RoleApprove: 1}
	for role, n := range want {
		if counts[role] != n {
			t.Errorf("counts[%s] = %d, want %d", role, counts[role], n)
		}
	}
	if _, ok := counts[RoleFollowup]; ok {
		t.Errorf("untouched pool reported in counts: %#v", counts)
	}
	if got := s.Text(RoleRegister); got != "http://b:2" {
		t.Errorf("register pool = %q", got)
	}
	if _, total := s.RemoveEverywhere(nil); total != 0 {
		t.Error("RemoveEverywhere(nil) removed something")
	}
}

// UI_SPEC G6: under 全走本地代理 every pool read returns empty and every
// reuse-proxy read returns "". The gate must also not rotate the pool — a
// gated Take that still rotated would silently reorder the user's list.
func TestLocalOnlyGate(t *testing.T) {
	s := NewSet()
	s.SetText(RoleRegister, "http://r:1\nhttp://r:2")
	s.SetText(RoleCreate, "http://c:1")
	s.SetText(RoleFollowup, "http://f:1")
	s.SetText(RoleApprove, "http://p:1")
	s.SetReuse(RoleCreate, "1.2.3.4:8080:u:p")
	s.SetReuse(RoleFollowup, "http://reuse:2")
	s.SetReuse(RoleApprove, "http://reuse:3")

	if got := s.SetMode(RouteModeLocalOnly); got != RouteModeLocalOnly {
		t.Fatalf("SetMode returned %q", got)
	}
	if !s.LocalOnly() {
		t.Fatal("LocalOnly() is false after switching to 全走本地代理")
	}

	for _, role := range Roles {
		if got := s.List(role); got != nil {
			t.Errorf("List(%s) = %#v, want nil", role, got)
		}
		if got := s.Peek(role); got != "" {
			t.Errorf("Peek(%s) = %q", role, got)
		}
		if got := s.Take(role); got != "" {
			t.Errorf("Take(%s) = %q", role, got)
		}
		if got := s.TakeN(role, 2); got != nil {
			t.Errorf("TakeN(%s) = %#v", role, got)
		}
		if got := s.Reuse(role); got != "" {
			t.Errorf("Reuse(%s) = %q", role, got)
		}
	}
	if got := s.TakeAuth(false); got != "" {
		t.Errorf("TakeAuth(false) = %q", got)
	}
	if got := s.TakeAuth(true); got != "" {
		t.Errorf("TakeAuth(true) = %q", got)
	}
	if got := s.TakeFollowupOrCreate(); got != "" {
		t.Errorf("TakeFollowupOrCreate = %q", got)
	}

	// The editor still shows what the user typed: _proxy_pool_nonempty_line_count
	// (app.py:13205) and the persisted settings value are NOT gated.
	if got := s.Text(RoleRegister); got != "http://r:1\nhttp://r:2" {
		t.Errorf("gated Text(register) = %q — the gate rotated or cleared the pool", got)
	}
	if got := s.Remaining(RoleRegister); got != 2 {
		t.Errorf("gated Remaining(register) = %d, want 2", got)
	}
	if got := s.ReuseText(RoleCreate); got != "1.2.3.4:8080:u:p" {
		t.Errorf("gated ReuseText(create) = %q", got)
	}

	s.SetMode(RouteModeDefault)
	if got := s.Take(RoleRegister); got != "http://r:1" {
		t.Errorf("after leaving local-only, Take = %q, want the untouched head", got)
	}
	if got := s.Reuse(RoleCreate); got != "http://u:p@1.2.3.4:8080" {
		t.Errorf("Reuse(create) = %q, want the normalized override", got)
	}
}

func TestNormalizeRouteMode(t *testing.T) {
	cases := []struct{ in_, want string }{
		{"", RouteModeDefault},
		{"照旧", RouteModeDefault},
		{"全走本地代理", RouteModeLocalOnly},
		{"  全走本地代理  ", RouteModeLocalOnly},
		// str.strip() also eats U+001C..U+001F, which strings.TrimSpace does not.
		{"\x1c全走本地代理\x1f", RouteModeLocalOnly},
		{"\u3000全走本地代理\u00a0", RouteModeLocalOnly},
		{"全走本地代理x", RouteModeDefault},
		{"local", RouteModeDefault},
		{"   ", RouteModeDefault},
	}
	for _, tc := range cases {
		if got := NormalizeRouteMode(tc.in_); got != tc.want {
			t.Errorf("NormalizeRouteMode(%q) = %q, want %q", tc.in_, got, tc.want)
		}
	}
}

func TestTakeAuthAndFollowupFallback(t *testing.T) {
	s := NewSet()
	s.SetText(RoleRegister, "http://r:1")
	s.SetText(RoleCreate, "http://c:1")

	if got := s.TakeAuth(false); got != "http://r:1" {
		t.Errorf("TakeAuth(false) = %q, want the register pool", got)
	}
	// 注册时使用支付链接动态代理 routes auth through the 第一步 pool instead.
	if got := s.TakeAuth(true); got != "http://c:1" {
		t.Errorf("TakeAuth(true) = %q, want the create pool", got)
	}
	// _take_followup_or_payment_dynamic_proxy (app.py:17486): empty followup
	// pool falls back to 第一步.
	if got := s.TakeFollowupOrCreate(); got != "http://c:1" {
		t.Errorf("fallback = %q, want the create pool", got)
	}
	s.SetText(RoleFollowup, "http://f:1")
	if got := s.TakeFollowupOrCreate(); got != "http://f:1" {
		t.Errorf("with a followup pool = %q, want the followup pool", got)
	}
	if got := s.Peek(RoleCreate); got != "http://c:1" {
		t.Errorf("create pool head moved to %q — the fallback consumed it needlessly", got)
	}
}

func TestPoolTitlesAndSnapshotOrder(t *testing.T) {
	if got := RoleRegister.Title(3); got != "注册/获取 Session 动态代理池（剩余 3）" {
		t.Errorf("title = %q", got)
	}
	if got := RoleApprove.Title(-1); got != "Approve 代理池（剩余 0）" {
		t.Errorf("negative count not floored: %q", got)
	}

	// Roles is the render order. Go randomises map iteration where a Python dict
	// is insertion-ordered, so anything user-visible must come from this slice.
	wantOrder := []Role{RoleRegister, RoleCreate, RoleFollowup, RoleApprove}
	for i, role := range Roles {
		if role != wantOrder[i] {
			t.Fatalf("Roles[%d] = %s, want %s", i, role, wantOrder[i])
		}
	}

	s := NewSet()
	s.SetText(RoleCreate, "http://c:1\nhttp://c:2")
	snap := s.Snapshot()
	if snap.Mode != RouteModeDefault || snap.LocalOnly {
		t.Errorf("snapshot mode = %q localOnly = %v", snap.Mode, snap.LocalOnly)
	}
	views := []PoolView{snap.Register, snap.Create, snap.Followup, snap.Approve}
	for i, view := range views {
		if view.Role != string(wantOrder[i]) {
			t.Errorf("snapshot field %d has role %q, want %q", i, view.Role, wantOrder[i])
		}
		if view.Title != wantOrder[i].Title(view.Remaining) {
			t.Errorf("snapshot title %q does not match count %d", view.Title, view.Remaining)
		}
	}
	if snap.Create.Remaining != 2 || snap.Create.Text != "http://c:1\nhttp://c:2" {
		t.Errorf("create view = %#v", snap.Create)
	}
}

func TestSetReuseIgnoresRegisterRole(t *testing.T) {
	s := NewSet()
	s.SetReuse(RoleRegister, "http://nope:1")
	if got := s.ReuseText(RoleRegister); got != "" {
		t.Errorf("register reuse stored %q; only create/followup/approve have one", got)
	}
	s.SetReuse(RoleApprove, "  1.2.3.4:8080:u:p  ")
	if got := s.ReuseText(RoleApprove); got != "  1.2.3.4:8080:u:p  " {
		t.Errorf("ReuseText must stay verbatim, got %q", got)
	}
	if got := s.Reuse(RoleApprove); got != "http://u:p@1.2.3.4:8080" {
		t.Errorf("Reuse = %q, want the normalized form", got)
	}
}

// The change callback builds the pools-updated payload, so it necessarily reads
// the set back. Firing it while holding a pool lock would deadlock the whole UI.
func TestOnChangeFiresWithoutHoldingLocks(t *testing.T) {
	s := NewSet()
	var mu sync.Mutex
	fired := 0
	s.SetOnChange(func() {
		snap := s.Snapshot() // re-entrant read: must not deadlock
		mu.Lock()
		fired++
		_ = snap
		mu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.SetText(RoleRegister, "http://a:1\nhttp://b:2")
		s.Take(RoleRegister)
		s.Remove(RoleRegister, "http://a:1")
		s.SetMode(RouteModeLocalOnly)
		s.SetReuse(RoleCreate, "http://r:1")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mutation deadlocked against the onChange callback")
	}

	mu.Lock()
	defer mu.Unlock()
	if fired != 5 {
		t.Errorf("callback fired %d times, want 5 (one per mutation)", fired)
	}
}

// The Tk app needed the take-auth-proxy round-trip because the pools lived in
// widgets owned by the UI thread. UI_SPEC §4.2 deletes that RPC, which only
// works if concurrent Take is safe and lossless.
func TestConcurrentTakeKeepsThePoolIntact(t *testing.T) {
	s := NewSet()
	s.SetText(RoleRegister, "http://a:1\nhttp://b:2\nhttp://c:3\nhttp://d:4\nhttp://e:5")

	var wg sync.WaitGroup
	seen := make(chan string, 400)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				seen <- s.Take(RoleRegister)
			}
		}()
	}
	wg.Wait()
	close(seen)

	valid := map[string]bool{
		"http://a:1": true, "http://b:2": true, "http://c:3": true,
		"http://d:4": true, "http://e:5": true,
	}
	count := 0
	for value := range seen {
		if !valid[value] {
			t.Fatalf("Take produced %q, which is not in the pool", value)
		}
		count++
	}
	if count != 400 {
		t.Errorf("got %d takes, want 400", count)
	}
	if s.Remaining(RoleRegister) != 5 {
		t.Errorf("pool now holds %d entries, want 5", s.Remaining(RoleRegister))
	}
	got := s.List(RoleRegister)
	unique := map[string]bool{}
	for _, value := range got {
		if unique[value] {
			t.Fatalf("duplicate entry after rotation: %#v", got)
		}
		unique[value] = true
	}
}

// SetText stores the textarea content VERBATIM. app.py only ever rewrites the
// widget from inside _rotate_proxy_pool_values / _remove_* (app.py:17322,
// 17357, 17385) — an edit leaves whatever the user typed in place, and the
// normalized form is a read (parse_proxy_pool_text), not a write.
func TestSetTextKeepsTheEditorContentVerbatim(t *testing.T) {
	s := NewSet()
	const raw = "  1.2.3.4:8080:u:p  \n\nsocks5://5.6.7.8:1080\n"
	s.SetText(RoleCreate, raw)
	if got := s.Text(RoleCreate); got != raw {
		t.Errorf("Text =\n%q\nwant the untouched edit\n%q", got, raw)
	}
	if got := s.Remaining(RoleCreate); got != 2 {
		t.Errorf("Remaining = %d, want 2", got)
	}
	want := "http://u:p@1.2.3.4:8080|socks5h://5.6.7.8:1080"
	if got := strings.Join(s.List(RoleCreate), "|"); got != want {
		t.Errorf("List = %q, want %q", got, want)
	}
	// The first rotation is what normalizes the widget (app.py:17322).
	s.Take(RoleCreate)
	if got := s.Text(RoleCreate); got != "socks5h://5.6.7.8:1080\nhttp://u:p@1.2.3.4:8080" {
		t.Errorf("after rotation Text = %q", got)
	}
	s.SetText(RoleCreate, "")
	if got := s.Text(RoleCreate); got != "" {
		t.Errorf("cleared pool = %q", got)
	}
}

func TestUnknownRoleIsInert(t *testing.T) {
	s := NewSet()
	const bogus = Role("nope")
	if s.Pool(bogus) != nil {
		t.Error("Pool(unknown) should be nil")
	}
	s.SetText(bogus, "http://a:1")
	if s.Take(bogus) != "" || s.Peek(bogus) != "" || s.Text(bogus) != "" ||
		s.Remaining(bogus) != 0 || s.List(bogus) != nil || s.TakeN(bogus, 2) != nil ||
		s.Remove(bogus, "http://a:1") {
		t.Error("an unknown role must read as an empty pool")
	}
	if got := bogus.TitleBase(); got != "nope" {
		t.Errorf("TitleBase fallback = %q", got)
	}
}
