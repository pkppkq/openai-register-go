package openai

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// RandomURLSafeString mirrors Python random_urlsafe_string: it produces a
// URL-safe base64 token from max(1,length) random bytes, then slices it to
// exactly `length` characters. Used for PKCE verifier / state / session ids.
func RandomURLSafeString(length int) string {
	n := length
	if n < 1 {
		n = 1
	}
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	token := base64.RawURLEncoding.EncodeToString(buf)
	if length <= 0 {
		return ""
	}
	if len(token) > length {
		return token[:length]
	}
	return token
}

// PKCECodeChallenge mirrors pkce_code_challenge: base64url(sha256(verifier)) with
// padding stripped.
func PKCECodeChallenge(codeVerifier string) string {
	digest := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// DecodeJWTPayload mirrors decode_jwt_payload: split on '.', base64url-decode the
// claims segment, JSON-parse to a map. Returns an empty map on any failure.
func DecodeJWTPayload(token string) map[string]any {
	empty := map[string]any{}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return empty
	}
	seg := parts[1]
	// Python converts url-safe chars then re-pads before std-decoding; RawURLEncoding
	// (url-safe alphabet, no padding) is the direct equivalent for JWT segments.
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		// Tolerate segments that carry '=' padding.
		if raw, err = base64.URLEncoding.DecodeString(seg); err != nil {
			return empty
		}
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return empty
	}
	return data
}

// GetNestedRecord mirrors get_nested_record: payload[key] if it is an object,
// else an empty map.
func GetNestedRecord(payload map[string]any, key string) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	if v, ok := payload[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

// FirstNonEmpty mirrors first_non_empty: the first value whose stringified,
// trimmed form is non-empty.
func FirstNonEmpty(values ...any) string {
	for _, v := range values {
		if v == nil {
			continue
		}
		text := pyStrip(stringify(v))
		if text != "" {
			return text
		}
	}
	return ""
}

// stringify is Python's str(value) over the shapes json.loads produces.
//
// It is deliberately NOT accountPyStr: first_non_empty is str(value), not
// str(value or ""), so a False or a 0 stringifies to "False" / "0" — both non-empty,
// so both WIN the chain rather than being skipped.
func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		// CPython's json.loads yields an int for an integral JSON number, so str()
		// prints no ".0" and never an exponent. Go's %v renders float64(1234567890)
		// as "1.23456789e+09", which then corrupts the [-8:] id tail taken from it.
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	case fmt.Stringer:
		return t.String()
	default:
		// Lists and objects reach str() too: a JWT claim can hold either, and
		// Python renders "[1, 2]" / "{'a': 1}" where "%v" writes "[1 2]" /
		// "map[a:1]" — which would then become the account_id tail.
		return pyStr(t)
	}
}

// AuthRecord is the normalized OpenAI OAuth token record. Field order + JSON tags
// mirror the Python normalize_openai_auth_record dict so serialized output matches.
type AuthRecord struct {
	AccessToken  string `json:"access_token"`
	AccountID    string `json:"account_id"`
	Disabled     bool   `json:"disabled"`
	Email        string `json:"email"`
	Expired      string `json:"expired"`
	IDToken      string `json:"id_token"`
	LastRefresh  string `json:"last_refresh"`
	RefreshToken string `json:"refresh_token"`
	Type         string `json:"type"`
	Websockets   bool   `json:"websockets"`
}

// NormalizeOpenAIAuthRecord mirrors normalize_openai_auth_record: validate the
// token payload, derive account_id/email/exp from the access + id token claims,
// and build the codex-type record. Returns an error (loudly) on any missing field.
func NormalizeOpenAIAuthRecord(emailAddr string, payload map[string]any) (AuthRecord, error) {
	accessToken := asString(payload["access_token"])
	refreshToken := asString(payload["refresh_token"])
	idToken := asString(payload["id_token"])
	if accessToken == "" {
		return AuthRecord{}, fmt.Errorf("token响应缺少 access_token: %v", payload)
	}
	if refreshToken == "" {
		return AuthRecord{}, fmt.Errorf("token响应缺少 refresh_token: %v", payload)
	}
	if idToken == "" {
		return AuthRecord{}, fmt.Errorf("token响应缺少 id_token: %v", payload)
	}
	accessClaims := DecodeJWTPayload(accessToken)
	idClaims := DecodeJWTPayload(idToken)
	authClaim := GetNestedRecord(accessClaims, "https://api.openai.com/auth")
	idAuthClaim := GetNestedRecord(idClaims, "https://api.openai.com/auth")
	accountID := FirstNonEmpty(authClaim["chatgpt_account_id"], idAuthClaim["chatgpt_account_id"])
	exp := asInt(accessClaims["exp"])
	if accountID == "" {
		return AuthRecord{}, fmt.Errorf("token中缺少 account_id: %v", accessClaims)
	}
	if exp == 0 {
		return AuthRecord{}, fmt.Errorf("access_token中缺少 exp: %v", accessClaims)
	}
	return AuthRecord{
		AccessToken:  accessToken,
		AccountID:    accountID,
		Disabled:     false,
		Email:        FirstNonEmpty(idClaims["email"], accessClaims["email"], emailAddr),
		Expired:      isoZ(time.Unix(exp, 0).UTC(), false),
		IDToken:      idToken,
		LastRefresh:  isoZ(time.Now().UTC(), true),
		RefreshToken: refreshToken,
		Type:         "codex",
		Websockets:   false,
	}, nil
}

// OpenAIBrowserHeaders mirrors openai_browser_headers: the Chrome client-hint
// header set, with any extra entries merged over the defaults.
func OpenAIBrowserHeaders(extra map[string]string) map[string]string {
	headers := map[string]string{
		"user-agent":                  DefaultUserAgent,
		"accept-language":             "zh-CN,zh;q=0.9,en;q=0.8",
		"sec-ch-ua":                   `"Google Chrome";v="146", "Chromium";v="146", "Not.A/Brand";v="24"`,
		"sec-ch-ua-full-version-list": `"Google Chrome";v="146.0.0.0", "Chromium";v="146.0.0.0", "Not.A/Brand";v="24.0.0.0"`,
		"sec-ch-ua-mobile":            "?0",
		"sec-ch-ua-platform":          `"Windows"`,
		"sec-ch-ua-platform-version":  `"15.0.0"`,
		"sec-ch-viewport-width":       `"1365"`,
	}
	for k, v := range extra {
		headers[k] = v
	}
	return headers
}

// isoZ formats a UTC time like Python datetime.isoformat().replace("+00:00","Z").
// withMicros=true keeps 6-digit microseconds (datetime.now); false is whole
// seconds (datetime.fromtimestamp of an int exp).
func isoZ(t time.Time, withMicros bool) string {
	if withMicros {
		return t.Format("2006-01-02T15:04:05.000000") + "Z"
	}
	return t.Format("2006-01-02T15:04:05") + "Z"
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func asInt(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}
