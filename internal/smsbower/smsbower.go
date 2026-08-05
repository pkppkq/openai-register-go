// Package smsbower is a Go port of the Python SMSBowerClient used to rent
// disposable phone numbers and poll for the SMS verification code.
//
// It is a faithful port of the SMSBowerClient / SMSBowerError classes: the same
// handler_api.php endpoint, the same action/query parameters, the same
// error-code message map, the same transient-error retry policy, and the same
// STATUS_* parsing for the polling loop.
package smsbower

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Defaults mirror the module-level constants in app.py.
const (
	DefaultAPIURL  = "https://smsbower.page/stubs/handler_api.php"
	DefaultService = "dr"
	DefaultCountry = "33"

	// requestTimeout is the per-HTTP-request timeout (Python used timeout=30).
	requestTimeout = 30 * time.Second
	// maxAttempts matches the Python range(1, 5) => 4 attempts.
	maxAttempts = 4
)

// errorMessages maps SMSBower error codes to user-facing (Chinese) messages.
// Kept identical to SMSBowerClient.ERROR_MESSAGES.
var errorMessages = map[string]string{
	"BAD_KEY":             "API Key 无效",
	"BAD_ACTION":          "API action 无效",
	"BAD_SERVICE":         "服务代码无效",
	"BAD_COUNTRY":         "国家 ID 无效",
	"NO_NUMBERS":          "当前地区没有可用号码",
	"NO_BALANCE":          "账户余额不足",
	"NO_ACTIVATION":       "激活 ID 不存在",
	"EARLY_CANCEL_DENIED": "购买后暂时不允许取消",
	"MAX_PRICE_EXCEEDED":  "可用号码价格超过最高限价",
}

// transientMarkers are substrings (case-folded) that mark a retryable
// network/transport hiccup. Identical to SMSBowerClient.TRANSIENT_MARKERS.
var transientMarkers = []string{
	"connection aborted",
	"connection reset",
	"connectionreseterror",
	"10054",
	"远程主机强迫关闭",
	"forcibly closed",
	"timed out",
	"timeout",
	"temporarily unavailable",
	"max retries exceeded",
	"broken pipe",
	"remote end closed",
	"empty reply from server",
	"ssl",
	"tls",
}

// SetStatus action codes, matching the SMSBower handler_api protocol.
const (
	StatusReadyToReceive = 1 // number received, ready for SMS
	StatusRequestAnother = 3 // request another SMS
	StatusFinish         = 6 // activation complete
	StatusCancel         = 8 // cancel activation
)

// Error is the Go equivalent of the Python SMSBowerError (a RuntimeError
// subclass). All user-facing failures returned by this package wrap an *Error.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return e.Msg }

func newErrorf(format string, args ...any) *Error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

// IsTransientError reports whether err (or its message) matches one of the
// transient markers, i.e. whether the operation is worth retrying. Mirrors
// SMSBowerClient.is_transient_error (app.py:3745-3748), which accepts either an
// exception or a string and folds it with str.casefold().
func IsTransientError(v any) bool {
	var text string
	switch t := v.(type) {
	case nil:
		return false
	case string:
		text = t
	case error:
		text = t.Error()
	default:
		text = fmt.Sprint(t)
	}
	text = pyCasefold(text)
	for _, marker := range transientMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// Number is the result of GetNumber (Python returned a dict with
// activation_id / number).
type Number struct {
	ActivationID string
	Number       string
}

// PriceQuote is the result of GetPriceQuote (cost/count for a service+country).
type PriceQuote struct {
	Cost  float64
	Count int
}

// PriceTier is one (cost, count) pair returned by GetPriceTiers.
type PriceTier struct {
	Cost  float64
	Count int
}

// Client talks to the SMSBower handler_api endpoint.
type Client struct {
	apiKey string
	apiURL string
	http   *http.Client
}

// NewClient builds a Client. apiURL falls back to DefaultAPIURL when empty.
// proxyURL, when non-empty, routes all requests through the given HTTP(S)
// proxy (the Python client accepted an injectable request_get for this).
func NewClient(apiKey, apiURL, proxyURL string) (*Client, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, &Error{Msg: "SMSBower API Key 为空"}
	}
	base := strings.TrimSpace(apiURL)
	if base == "" {
		base = DefaultAPIURL
	}

	transport := &http.Transport{}
	if p := strings.TrimSpace(proxyURL); p != "" {
		parsed, err := url.Parse(p)
		if err != nil {
			return nil, newErrorf("SMSBower 代理地址无效: %v", err)
		}
		transport.Proxy = http.ProxyURL(parsed)
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}

	return &Client{
		apiKey: key,
		apiURL: base,
		// No per-Client timeout: each request derives its own 30s context so
		// the polling loops remain cancellable via the caller's context.
		http: &http.Client{Transport: transport},
	}, nil
}

// param is one query entry. A SLICE of these, not a map: Python built the query
// from a dict, whose iteration order is insertion order, so the wire order is
// api_key, action, then the kwargs in source order (app.py:3751-3752). Go map
// iteration is randomised and url.Values.Encode() sorts alphabetically — either
// one would send a different query string than the Python client this endpoint
// has been serving all along.
type param struct{ key, value string }

// request performs one SMSBower API call with the same 4-attempt transient
// retry policy as SMSBowerClient._request (app.py:3750-3777). params with empty
// values are dropped from the query (the `value not in ("", None)` filter);
// note that an intentional "0" is NOT empty and survives, which is what keeps
// setStatus(..., 0) legal.
func (c *Client) request(ctx context.Context, action string, params ...param) (string, error) {
	var query strings.Builder
	query.WriteString("api_key=")
	query.WriteString(url.QueryEscape(c.apiKey))
	query.WriteString("&action=")
	query.WriteString(url.QueryEscape(action))
	for _, p := range params {
		if p.value == "" {
			continue
		}
		query.WriteByte('&')
		query.WriteString(url.QueryEscape(p.key))
		query.WriteByte('=')
		query.WriteString(url.QueryEscape(p.value))
	}
	fullURL := c.apiURL + "?" + query.String()

	var lastError string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		text, err := c.doOnce(ctx, fullURL)
		if err != nil {
			lastError = err.Error()
			if attempt < maxAttempts && IsTransientError(err) {
				if sleepErr := sleepFor(ctx, backoff(attempt)); sleepErr != nil {
					return "", newErrorf("SMSBower 请求失败: %v", sleepErr)
				}
				continue
			}
			return "", newErrorf("SMSBower 请求失败: %v", err)
		}

		// pyStrip, not strings.TrimSpace: str.strip() also eats U+001C..U+001F.
		// A body of "\x1cNO_BALANCE" is the NO_BALANCE sentinel to Python and was
		// a plain successful response to TrimSpace — i.e. "out of money" got
		// reported as an unrecognised-response parse failure two layers up.
		text = pyStrip(text)
		if text == "" {
			lastError = "SMSBower 返回空响应"
			if attempt < maxAttempts {
				if sleepErr := sleepFor(ctx, backoff(attempt)); sleepErr != nil {
					return "", newErrorf("SMSBower 请求失败: %v", sleepErr)
				}
				continue
			}
			return "", &Error{Msg: lastError}
		}

		errorCode := pyUpper(pyStrip(splitFirst(text, ":")))
		if msg, ok := errorMessages[errorCode]; ok {
			return "", newErrorf("%s (%s)", msg, errorCode)
		}
		if strings.HasPrefix(errorCode, "ERROR") || strings.HasPrefix(errorCode, "BAD_") {
			return "", newErrorf("SMSBower 返回错误: %s", truncate(text, 300))
		}
		return text, nil
	}

	if lastError == "" {
		lastError = "unknown"
	}
	return "", newErrorf("SMSBower 请求失败: %s", lastError)
}

// doOnce issues a single GET with a 30s timeout derived from ctx, applies the
// raise_for_status equivalent, and returns the response body text.
func (c *Client) doOnce(ctx context.Context, fullURL string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fullURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// A fired per-request 30s deadline (with the caller's context still
		// live) is the equivalent of requests' timeout=30, which Python treats
		// as a transient "timed out" failure worth retrying.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("SMSBower 请求 timed out: %w", err)
		}
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		// Equivalent of requests' raise_for_status().
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return string(body), nil
}

// GetBalance returns the account balance string (get_balance, app.py:3779-3783).
func (c *Client) GetBalance(ctx context.Context) (string, error) {
	text, err := c.request(ctx, "getBalance")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(text, "ACCESS_BALANCE:") {
		return "", newErrorf("SMSBower 余额响应无法识别: %s", truncate(text, 200))
	}
	return pyStrip(afterFirst(text, ":")), nil
}

// numberRe is re.fullmatch(r"ACCESS_NUMBER:([^:]+):(\+?\d+)", text)
// (app.py:3792). \d is spelled \p{Nd} because Python's \d on a str pattern is
// every Unicode decimal digit (680 code points), not [0-9]; Go's ^...$ without
// (?m) anchors to end-of-TEXT, which is what makes it a true fullmatch.
var numberRe = regexp.MustCompile(`^ACCESS_NUMBER:([^:]+):(\+?\p{Nd}+)$`)

// GetNumber rents a number for the given service+country (get_number,
// app.py:3785-3798). Empty service/country fall back to the defaults, matching
// the Python.
//
// MONEY: this is the call that rents a billable number. Never invoke it from a
// test; exercise the request construction and the response parsing separately.
func (c *Client) GetNumber(ctx context.Context, service, country, maxPrice string) (Number, error) {
	text, err := c.request(ctx, "getNumber",
		param{"service", orDefault(service, DefaultService)},
		param{"country", orDefault(country, DefaultCountry)},
		param{"maxPrice", pyStrip(maxPrice)},
	)
	if err != nil {
		return Number{}, err
	}
	m := numberRe.FindStringSubmatch(text)
	if m == nil {
		return Number{}, newErrorf("SMSBower 取号响应无法识别: %s", truncate(text, 200))
	}
	number := m[2]
	if !strings.HasPrefix(number, "+") {
		number = "+" + number
	}
	return Number{ActivationID: m[1], Number: number}, nil
}

// GetPriceQuote returns the cost/count quote for service+country
// (get_price_quote, app.py:3800-3817, action getPrices).
//
// DIVERGENCE (two, both deliberate):
//
//  1. Python looked the country/service up with the UNSTRIPPED argument
//     (app.py:3810-3811) while sending the STRIPPED one (app.py:3803-3804), so
//     a padded country like " 33 " asked the API for 33 and then failed to find
//     33 in the reply. The same stripped value is used for both here.
//  2. Python returned cost/count raw, so a JSON null printed as nothing and a
//     float count kept its fraction; these are typed, so a missing field is
//     indistinguishable from 0 and a float count is truncated. Both differences
//     are confined to the log line built in phoneprovider.scanPrices.
func (c *Client) GetPriceQuote(ctx context.Context, service, country string) (PriceQuote, error) {
	svc := orDefault(service, DefaultService)
	ctry := orDefault(country, DefaultCountry)
	text, err := c.request(ctx, "getPrices",
		param{"service", svc},
		param{"country", ctry},
	)
	if err != nil {
		return PriceQuote{}, err
	}

	serviceData, err := countryServiceObject(text, ctry, svc, "价格")
	if err != nil {
		return PriceQuote{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(serviceData, &fields); err != nil || fields == nil {
		return PriceQuote{}, newErrorf("SMSBower 价格响应缺少 %s/%s: %s", ctry, svc, truncate(text, 200))
	}
	cost, _ := pyFloatValue(fields["cost"])
	count, _ := pyIntValue(fields["count"])
	return PriceQuote{Cost: cost, Count: count}, nil
}

// GetPriceTiers returns the available (cost, count) tiers sorted ascending by
// cost, dropping tiers with count <= 0 (get_price_tiers, app.py:3819-3843,
// action getPricesV2).
//
// The tier order is what phoneprovider walks when it rents, and the first five
// entries are echoed into the user's log, so it has to be reproducible: the
// tiers are read in JSON key order (Python iterated a dict, which is insertion
// ordered, where a Go map is randomised) and sorted with a STABLE sort, because
// Python's list.sort is stable and leaves equal costs in their JSON order.
func (c *Client) GetPriceTiers(ctx context.Context, service, country string) ([]PriceTier, error) {
	svc := orDefault(service, DefaultService)
	ctry := orDefault(country, DefaultCountry)
	text, err := c.request(ctx, "getPricesV2",
		param{"service", svc},
		param{"country", ctry},
	)
	if err != nil {
		return nil, err
	}

	serviceData, err := countryServiceObject(text, ctry, svc, "分档价格")
	if err != nil {
		return nil, err
	}
	entries, ok := orderedObject(serviceData)
	if !ok {
		return nil, newErrorf("SMSBower 分档价格响应缺少 %s/%s: %s", ctry, svc, truncate(text, 200))
	}

	tiers := make([]PriceTier, 0, len(entries))
	for _, entry := range entries {
		// `float(cost_text)` (app.py:3836): a key that is not a number is skipped
		// by the except, and so is a count that int() refuses.
		cost, costErr := strconv.ParseFloat(pyStrip(entry.key), 64)
		if costErr != nil {
			continue
		}
		count, countOK := pyIntValue(entry.value)
		if !countOK {
			continue
		}
		if count > 0 {
			tiers = append(tiers, PriceTier{Cost: cost, Count: count})
		}
	}
	sort.SliceStable(tiers, func(i, j int) bool { return tiers[i].Cost < tiers[j].Cost })
	return tiers, nil
}

// countryServiceObject is the payload[country][service] walk shared by the two
// price calls (app.py:3810-3813 / 3829-3832), including the `isinstance(...,
// dict)` guards: anything that is not an object at any level produces the
// "缺少" error, and only a syntactically broken body produces "无法识别".
//
// DIVERGENCE: Python echoed `str(payload)[:200]`, a Python dict repr; the raw
// response text is echoed instead — the same information in the shape the
// server actually sent.
func countryServiceObject(text, country, service, what string) (json.RawMessage, error) {
	missing := func() error {
		return newErrorf("SMSBower %s响应缺少 %s/%s: %s", what, country, service, truncate(text, 200))
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		if !json.Valid([]byte(text)) {
			return nil, newErrorf("SMSBower %s响应无法识别: %s", what, truncate(text, 200))
		}
		// Valid JSON that simply is not an object (a list, a string, a number):
		// Python's `payload.get(...) if isinstance(payload, dict) else None`.
		return nil, missing()
	}
	if top == nil { // JSON null unmarshals into a nil map WITHOUT an error.
		return nil, missing()
	}
	countryRaw, ok := top[country]
	if !ok {
		return nil, missing()
	}
	var countryData map[string]json.RawMessage
	if err := json.Unmarshal(countryRaw, &countryData); err != nil || countryData == nil {
		return nil, missing()
	}
	serviceRaw, ok := countryData[service]
	if !ok {
		return nil, missing()
	}
	if !isJSONObject(serviceRaw) {
		return nil, missing()
	}
	return serviceRaw, nil
}

func isJSONObject(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

type objectEntry struct {
	key   string
	value json.RawMessage
}

// orderedObject decodes a JSON object preserving key order, which
// encoding/json's map decoding throws away. A duplicate key keeps its
// first-seen position and takes the LAST value, matching CPython's dict.
func orderedObject(raw json.RawMessage) ([]objectEntry, bool) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, false
	}
	var entries []objectEntry
	index := map[string]int{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		if at, seen := index[key]; seen {
			entries[at].value = value
			continue
		}
		index[key] = len(entries)
		entries = append(entries, objectEntry{key: key, value: value})
	}
	return entries, true
}

// pyFloatValue is `float(x)` applied to a decoded JSON value, tolerating the
// numeric-as-string shape the API sometimes uses. ok is false where Python's
// float() would have raised.
func pyFloatValue(raw json.RawMessage) (float64, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, false
	}
	if strings.HasPrefix(text, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
		text = s
	}
	value, err := strconv.ParseFloat(pyStrip(text), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// pyIntValue is `int(value or 0)` (app.py:3837) applied to a decoded JSON
// value: null / "" / 0 collapse to 0, a float TRUNCATES toward zero (int(3.9)
// is 3 — dropping that tier instead would send the rental to a dearer one), and
// anything int() would refuse reports ok=false so the caller skips the entry.
func pyIntValue(raw json.RawMessage) (int, bool) {
	text := strings.TrimSpace(string(raw))
	switch text {
	case "", "null", "false":
		return 0, true
	case "true":
		return 1, true
	}
	if strings.HasPrefix(text, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
		s = pyStrip(s)
		if s == "" { // `"" or 0`
			return 0, true
		}
		// int(str) accepts integer text only: int("3.9") raises.
		value, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return int(math.Trunc(value)), true
}

// GetStatus returns the activation status (upper-cased) and its value part
// (get_status, app.py:3845-3848, action getStatus). e.g. ("STATUS_OK", "123456").
func (c *Client) GetStatus(ctx context.Context, activationID string) (status, value string, err error) {
	text, err := c.request(ctx, "getStatus", param{"id", pyStrip(activationID)})
	if err != nil {
		return "", "", err
	}
	head, sep, tail := partition(text, ":")
	status = pyUpper(pyStrip(head))
	if sep {
		value = pyStrip(tail)
	}
	return status, value, nil
}

// SetStatus updates the activation status (set_status, app.py:3850-3851, action
// setStatus). Use the Status* constants for status.
//
// MONEY: 6 finishes and charges the rental, 8 cancels it. Never guess the code.
func (c *Client) SetStatus(ctx context.Context, activationID string, status int) (string, error) {
	return c.request(ctx, "setStatus",
		param{"id", pyStrip(activationID)},
		// Python passed the int and requests rendered it as decimal text; 0 is
		// NOT removed by the `value not in ("", None)` filter.
		param{"status", strconv.Itoa(status)},
	)
}

// nonDigitRe is re.sub(r"\D+", "", value) (app.py:3866). \D is spelled as the
// negation of \p{Nd} because Python's \d is every Unicode decimal digit: an SMS
// delivered in full-width digits yields a code in Python and, with RE2's ASCII
// \D, would be stripped to nothing and reported as "未解析出验证码" — after the
// number had already been paid for.
var nonDigitRe = regexp.MustCompile(`[^\p{Nd}]+`)

// WaitForCode polls getStatus until the SMS verification code arrives, the
// activation is cancelled, or the timeout elapses (wait_for_code). timeout and
// interval are in seconds; ctx cancellation aborts the loop early.
func (c *Client) WaitForCode(ctx context.Context, activationID string, timeout, interval int) (string, error) {
	if timeout < 1 {
		timeout = 1
	}
	if interval < 1 {
		interval = 1
	}
	deadline := nowFor().Add(time.Duration(timeout) * time.Second)
	intervalDur := time.Duration(interval) * time.Second
	lastStatus := ""

	for nowFor().Before(deadline) {
		status, value, err := c.GetStatus(ctx, activationID)
		if err != nil {
			var smsErr *Error
			if errors.As(err, &smsErr) && IsTransientError(err) &&
				nowFor().Add(intervalDur).Before(deadline) {
				if sleepErr := sleepFor(ctx, capToDeadline(intervalDur, deadline)); sleepErr != nil {
					return "", newErrorf("等待 SMSBower 验证码中断: %v", sleepErr)
				}
				continue
			}
			return "", err
		}

		if value != "" {
			lastStatus = status + ":" + value
		} else {
			lastStatus = status
		}

		switch status {
		case "STATUS_OK":
			code := nonDigitRe.ReplaceAllString(value, "")
			if code != "" {
				return code, nil
			}
			return "", newErrorf("SMSBower 已收到短信，但未解析出验证码: %s", truncate(value, 100))
		case "STATUS_CANCEL":
			return "", &Error{Msg: "SMSBower 激活已取消"}
		case "STATUS_WAIT_CODE", "STATUS_WAIT_RETRY":
			// keep polling
		default:
			return "", newErrorf("SMSBower 激活状态无法识别: %s", lastStatus)
		}

		if sleepErr := sleepFor(ctx, capToDeadline(intervalDur, deadline)); sleepErr != nil {
			return "", newErrorf("等待 SMSBower 验证码中断: %v", sleepErr)
		}
	}

	if lastStatus == "" {
		lastStatus = "无"
	}
	return "", newErrorf("等待 SMSBower 验证码超时，最后状态: %s", lastStatus)
}

// --- helpers ---

// backoff mirrors time.sleep(min(8, attempt * 1.5)).
func backoff(attempt int) time.Duration {
	secs := math.Min(8, float64(attempt)*1.5)
	return time.Duration(secs * float64(time.Second))
}

// capToDeadline mirrors min(interval, max(0, deadline - now)).
func capToDeadline(interval time.Duration, deadline time.Time) time.Duration {
	remaining := deadline.Sub(nowFor())
	if remaining < 0 {
		remaining = 0
	}
	if interval < remaining {
		return interval
	}
	return remaining
}

// sleepFor is the retry/poll delay. It is a variable ONLY so the tests can
// observe the backoff schedule without spending it; production always uses
// sleepCtx.
var sleepFor = sleepCtx

// nowFor 与 sleepFor 配套注入测试时钟，避免跳过 sleep 后在真实的一秒内空转数千次。
var nowFor = time.Now

// sleepCtx sleeps for d, returning early with ctx.Err() if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// splitFirst returns the part of s before the first sep (or all of s).
func splitFirst(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i]
	}
	return s
}

// afterFirst returns the part of s after the first sep (or "").
func afterFirst(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return s[i+len(sep):]
	}
	return ""
}

// partition mirrors Python str.partition.
func partition(s, sep string) (head string, found bool, tail string) {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i], true, s[i+len(sep):]
	}
	return s, false, ""
}

// truncate is Python's text[:n], which counts CODE POINTS. Slicing bytes both
// keeps a different amount of text (a 400-character Chinese error message came
// out 100 characters long, not 300) and can split a UTF-8 sequence, putting a
// replacement character into a message the user reads.
func truncate(s string, n int) string {
	if len(s) <= n { // n bytes or fewer is always n runes or fewer
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

func orDefault(v, def string) string {
	if t := pyStrip(v); t != "" {
		return t
	}
	return def
}

// ---------------------------------------------------------------------------
// Python string primitives
// ---------------------------------------------------------------------------

// isPySpace is str.isspace(): the 29 code points Python strips. Go's
// unicode.IsSpace covers all of them EXCEPT the C0 separators U+001C..U+001F,
// and that gap is the whole difference between strings.TrimSpace and str.strip().
func isPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip is str.strip() with no argument.
func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// upperFullReplacer holds the code points whose str.upper() EXPANDS, restricted
// to those that can produce ASCII letters and so change whether a response
// prefix matches an error sentinel. strings.ToUpper is simple case mapping and
// leaves every one of them alone.
var upperFullReplacer = strings.NewReplacer(
	"ß", "SS", "ﬀ", "FF", "ﬁ", "FI", "ﬂ", "FL", "ﬃ", "FFI", "ﬄ", "FFL", "ﬅ", "ST", "ﬆ", "ST",
)

// pyUpper is str.upper() for the purposes of the STATUS_* / error-code compare.
func pyUpper(s string) string { return strings.ToUpper(upperFullReplacer.Replace(s)) }

// casefoldReplacer is the COMPLETE set of code points whose str.casefold()
// differs from Go's strings.ToLower in a way that adds or removes an ASCII
// letter — enumerated by scanning all 1,114,112 code points against CPython
// 3.12 (298 differ at all; these 18 are the ones that can change whether an
// ASCII transient marker is a substring).
//
// The first two are the ones that bite: "ssl" hides inside a folded "ß"+"l",
// and folding U+0130 inserts a combining dot that BREAKS "timed out" where
// ToLower would have completed it. Both flip whether a failed request is
// retried instead of surfacing as a hard error.
var casefoldReplacer = strings.NewReplacer(
	"\u00DF", "ss", // U+00DF LATIN SMALL LETTER SHARP S -> U+0073 U+0073
	"\u0130", "i\u0307", // U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE -> U+0069 U+0307
	"\u0149", "\u02BCn", // U+0149 LATIN SMALL LETTER N PRECEDED BY APOSTROPHE -> U+02BC U+006E
	"\u017F", "s", // U+017F LATIN SMALL LETTER LONG S -> U+0073
	"\u01F0", "j\u030C", // U+01F0 LATIN SMALL LETTER J WITH CARON -> U+006A U+030C
	"\u1E96", "h\u0331", // U+1E96 LATIN SMALL LETTER H WITH LINE BELOW -> U+0068 U+0331
	"\u1E97", "t\u0308", // U+1E97 LATIN SMALL LETTER T WITH DIAERESIS -> U+0074 U+0308
	"\u1E98", "w\u030A", // U+1E98 LATIN SMALL LETTER W WITH RING ABOVE -> U+0077 U+030A
	"\u1E99", "y\u030A", // U+1E99 LATIN SMALL LETTER Y WITH RING ABOVE -> U+0079 U+030A
	"\u1E9A", "a\u02BE", // U+1E9A LATIN SMALL LETTER A WITH RIGHT HALF RING -> U+0061 U+02BE
	"\u1E9E", "ss", // U+1E9E LATIN CAPITAL LETTER SHARP S -> U+0073 U+0073
	"\uFB00", "ff", // U+FB00 LATIN SMALL LIGATURE FF -> U+0066 U+0066
	"\uFB01", "fi", // U+FB01 LATIN SMALL LIGATURE FI -> U+0066 U+0069
	"\uFB02", "fl", // U+FB02 LATIN SMALL LIGATURE FL -> U+0066 U+006C
	"\uFB03", "ffi", // U+FB03 LATIN SMALL LIGATURE FFI -> U+0066 U+0066 U+0069
	"\uFB04", "ffl", // U+FB04 LATIN SMALL LIGATURE FFL -> U+0066 U+0066 U+006C
	"\uFB05", "st", // U+FB05 LATIN SMALL LIGATURE LONG S T -> U+0073 U+0074
	"\uFB06", "st", // U+FB06 LATIN SMALL LIGATURE ST -> U+0073 U+0074
)

// pyCasefold is str.casefold() restricted to what a substring test over the
// ASCII transient markers can observe. The ~280 remaining differences between
// full folding and simple lower-casing are all non-ASCII to non-ASCII (Greek,
// Cherokee, Deseret …) and cannot change the result.
func pyCasefold(s string) string { return strings.ToLower(casefoldReplacer.Replace(s)) }
