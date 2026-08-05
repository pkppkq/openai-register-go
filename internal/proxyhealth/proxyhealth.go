// Package proxyhealth ports app.py's proxy health-detection: probe a proxy's
// exit geo (ipinfo.io) + ChatGPT + Stripe reachability through the impersonating
// TLS client, returning a models.ProxyHealthResult. SOCKS5 proxies are first
// fronted by an in-process HTTP chain (as in Python) to avoid system DNS/fake-ip
// skewing the result.
package proxyhealth

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/proxychain"
	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

var reCountryCode = regexp.MustCompile(`^[A-Z]{2}$`)

// LogFunc receives human-readable progress lines (nil is allowed).
type LogFunc func(string)

func failStage(stage, errText string, base *ipinfoBase) models.ProxyHealthResult {
	r := models.ProxyHealthResult{Success: false, FailedStage: stage, Error: errText}
	if base != nil {
		r.IP, r.Country, r.Region, r.City, r.Timezone, r.Org = base.IP, base.Country, base.Region, base.City, base.Timezone, base.Org
	}
	return r
}

type ipinfoBase struct {
	IP, Country, Region, City, Timezone, Org string
}

// normalizeProxyURL is the minimal normalization needed for already-stored proxy
// URLs (the full multi-format parser in app.py is a UI-input concern): ensure a
// scheme and map socks5 -> socks5h (remote DNS).
func normalizeProxyURL(value, defaultScheme string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	low := strings.ToLower(text)
	if strings.HasPrefix(low, "socks5://") {
		return "socks5h://" + text[len("socks5://"):]
	}
	if strings.Contains(text, "://") {
		return text
	}
	return defaultScheme + "://" + text
}

func isSocksScheme(u string) bool {
	low := strings.ToLower(u)
	return strings.HasPrefix(low, "socks5://") || strings.HasPrefix(low, "socks5h://")
}

// effectiveProxy resolves the proxy the TLS client should use: for SOCKS5 it
// starts an HTTP chain and returns its local URL + a cleanup func.
func effectiveProxy(normalized string) (string, func()) {
	if normalized == "" || !isSocksScheme(normalized) {
		return normalized, func() {}
	}
	chain := proxychain.New("", normalized, func(string) {})
	if err := chain.Start(); err != nil {
		return normalized, func() {} // fall back to direct socks5
	}
	return chain.URL(), func() { chain.Close() }
}

// DetectProxyHealth mirrors detect_proxy_health: ipinfo -> ChatGPT csrf -> Stripe
// connectivity, short-circuiting on the first failing stage.
func DetectProxyHealth(proxyURL string, timeoutSeconds int) models.ProxyHealthResult {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 15
	}
	normalized := normalizeProxyURL(proxyURL, "http")
	effective, cleanup := effectiveProxy(normalized)
	defer cleanup()

	client, err := tlsclient.New(effective, timeoutSeconds)
	if err != nil {
		return failStage("出口", err.Error(), nil)
	}

	// Stage 1: exit geo via ipinfo.io.
	status, body, err := client.Do("GET", "https://ipinfo.io/json", nil, nil)
	if err != nil {
		return failStage("出口", err.Error(), nil)
	}
	if status != 200 {
		return failStage("出口", fmt.Sprintf("HTTP %d", status), nil)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		payload = map[string]any{}
	}
	ip := strings.TrimSpace(asStr(payload["ip"]))
	country := strings.ToUpper(strings.TrimSpace(asStr(payload["country"])))
	if ip == "" || !reCountryCode.MatchString(country) {
		return failStage("出口", "IPInfo 缺少 IP 或国家代码", nil)
	}
	base := &ipinfoBase{
		IP:       ip,
		Country:  country,
		Region:   strings.TrimSpace(asStr(payload["region"])),
		City:     strings.TrimSpace(asStr(payload["city"])),
		Timezone: strings.TrimSpace(asStr(payload["timezone"])),
		Org:      strings.TrimSpace(asStr(payload["org"])),
	}

	// Stage 2: ChatGPT csrf — must be 200 or 403.
	chatgptStatus, _, err := client.Do("GET", openai.ChatGPTBaseURL+"/api/auth/csrf", nil, nil)
	if err != nil {
		return failStage("ChatGPT", err.Error(), base)
	}
	if chatgptStatus != 200 && chatgptStatus != 403 {
		r := failStage("ChatGPT", fmt.Sprintf("HTTP %d", chatgptStatus), base)
		r.ChatGPTStatus = chatgptStatus
		return r
	}

	// Stage 3: Stripe connectivity — 407/429/5xx are hard failures.
	stripeStatus, _, err := client.Do("GET", "https://api.stripe.com/v1/payment_pages/__connectivity_check__", nil, nil)
	if err != nil {
		r := failStage("Stripe", err.Error(), base)
		r.ChatGPTStatus = chatgptStatus
		return r
	}
	if stripeStatus == 407 || stripeStatus == 429 || stripeStatus >= 500 {
		r := failStage("Stripe", fmt.Sprintf("HTTP %d", stripeStatus), base)
		r.ChatGPTStatus = chatgptStatus
		r.StripeStatus = stripeStatus
		return r
	}

	return models.ProxyHealthResult{
		Success:       true,
		IP:            base.IP,
		Country:       base.Country,
		Region:        base.Region,
		City:          base.City,
		Timezone:      base.Timezone,
		Org:           base.Org,
		ChatGPTStatus: chatgptStatus,
		StripeStatus:  stripeStatus,
	}
}

// DetectProxyHealthWithRetry mirrors detect_proxy_health_with_retry.
func DetectProxyHealthWithRetry(proxyURL string, timeoutSeconds, attempts int, log LogFunc, label string) models.ProxyHealthResult {
	if attempts < 1 {
		attempts = 1
	}
	if label == "" {
		label = "代理"
	}
	last := models.ProxyHealthResult{Success: false, FailedStage: "unknown", Error: "未执行检测"}
	for attempt := 1; attempt <= attempts; attempt++ {
		last = DetectProxyHealth(proxyURL, timeoutSeconds)
		if last.Success {
			return last
		}
		if attempt < attempts {
			if log != nil {
				log(fmt.Sprintf("%s健康检查失败，准备重试(%d/%d): %s", label, attempt, attempts, last.Summary()))
			}
			time.Sleep(1500 * time.Millisecond)
		}
	}
	return last
}

// DetectLocalProxyHealth mirrors detect_local_proxy_health: skip ipinfo, probe
// auth.openai.com reachability, softly note ChatGPT csrf, and return a
// conservative US/UTC result on success.
func DetectLocalProxyHealth(proxyURL string, timeoutSeconds int) models.ProxyHealthResult {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 15
	}
	normalized := normalizeProxyURL(proxyURL, "http")
	if normalized == "" && strings.TrimSpace(proxyURL) == "" {
		return failStage("本地代理", "未配置本地代理", nil)
	}
	effective, cleanup := effectiveProxy(normalized)
	defer cleanup()

	client, err := tlsclient.New(effective, timeoutSeconds)
	if err != nil {
		return failStage("本地/Auth", err.Error(), nil)
	}

	authStatus, _, err := client.Do("GET", openai.AuthBaseURL+"/", nil, nil)
	if err != nil {
		return failStage("本地/Auth", err.Error(), nil)
	}
	if authStatus == 407 || authStatus == 429 || authStatus >= 500 {
		return failStage("本地/Auth", fmt.Sprintf("auth.openai.com HTTP %d", authStatus), nil)
	}

	chatgptStatus := 0
	if s, _, err := client.Do("GET", openai.ChatGPTBaseURL+"/api/auth/csrf", nil, nil); err == nil {
		chatgptStatus = s
	}

	return models.ProxyHealthResult{
		Success:       true,
		IP:            "local",
		Country:       "US",
		Timezone:      "UTC",
		Org:           "local-proxy-only",
		ChatGPTStatus: chatgptStatus,
		StripeStatus:  0,
	}
}

// DetectLocalProxyHealthWithRetry mirrors detect_local_proxy_health_with_retry
// (backoff min(2*attempt, 5)s).
func DetectLocalProxyHealthWithRetry(proxyURL string, timeoutSeconds, attempts int, log LogFunc, label string) models.ProxyHealthResult {
	if attempts < 1 {
		attempts = 1
	}
	if label == "" {
		label = "本地代理"
	}
	last := models.ProxyHealthResult{Success: false, FailedStage: "unknown", Error: "未执行检测"}
	for attempt := 1; attempt <= attempts; attempt++ {
		last = DetectLocalProxyHealth(proxyURL, timeoutSeconds)
		if last.Success {
			return last
		}
		if attempt < attempts {
			if log != nil {
				log(fmt.Sprintf("%s连通检查失败，准备重试(%d/%d): %s", label, attempt, attempts, last.Summary()))
			}
			d := time.Duration(2*attempt) * time.Second
			if d > 5*time.Second {
				d = 5 * time.Second
			}
			time.Sleep(d)
		}
	}
	return last
}

func asStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
