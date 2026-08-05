package sessionconv

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/openai"
)

// pyIntOrZero mirrors Python's `int(value or 0)` over JSON scalars.
func pyIntOrZero(v any) int64 {
	if !pyTruthy(v) {
		return 0
	}
	if f, ok := pyIsNumber(v); ok {
		return pyIntTrunc(f)
	}
	if s, ok := v.(string); ok {
		// Python int("123") works; int("12.5") raises, which the callers below
		// would let propagate. Returning 0 is the closest non-panicking match.
		if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// ParseExpiredTime ports parse_expired_time (app.py:4921-4930).
//
// Python quirk kept: only a TRAILING "Z" is rewritten to +00:00, and a value
// with no offset at all becomes a naive datetime whose .timestamp() is read in
// the machine's LOCAL zone. Every producer in app.py writes "...Z", so this
// only matters for hand-edited input.
func ParseExpiredTime(value any) int64 {
	text := ""
	if pyTruthy(value) {
		text = pyStrip(pyStr(value))
	}
	if text == "" {
		return 0
	}
	if strings.HasSuffix(text, "Z") {
		text = text[:len(text)-1] + "+00:00"
	}
	res, ok := pyFromISOFormat(text)
	if !ok {
		return 0
	}
	// DIVERGENCE (coarse, deliberate): a NAIVE datetime.timestamp() goes through
	// the platform mktime, which on Windows fails outside roughly
	// [1970-01-02 + utcoffset, 3001-01-19 - utcoffset] LOCAL — the exact edges
	// move with the machine's time zone and with CPython's fold probe, so they
	// are not reproducible from Go. pyDatetimeTimestamp applies the CRT gmtime
	// window instead: that catches the cases that actually occur (year 1, year
	// 9999) and leaves a one-day sliver at each end where Python returns 0 and
	// this returns the timestamp. app.py only ever writes an aware "...Z"
	// expiry, so the naive path needs hand-edited input.
	epoch, ok := pyDatetimeTimestamp(res)
	if !ok {
		return 0
	}
	return pyIntTrunc(epoch)
}

// ResolveOrganizationID ports resolve_organization_id (app.py:4933-4940):
// prefer the id_token's organizations list, fall back to the access token's,
// and take the first entry's id.
func ResolveOrganizationID(idClaims, accessClaims map[string]any) string {
	idAuth := openai.GetNestedRecord(idClaims, openaiAuthClaim)
	accessAuth := openai.GetNestedRecord(accessClaims, openaiAuthClaim)
	organizations, ok := idAuth["organizations"].([]any)
	if !ok {
		organizations, ok = accessAuth["organizations"].([]any)
		if !ok {
			return ""
		}
	}
	if len(organizations) == 0 {
		return ""
	}
	first, ok := organizations[0].(map[string]any)
	if !ok {
		return ""
	}
	return pyFirstNonEmpty(first["id"])
}

// BuildSub2APIAccount ports build_sub2api_account (app.py:5017-5050).
//
// `record` is an openai auth record (the dict produced by
// normalize_openai_auth_record / openai_record_from_refresh_payload): keys
// access_token, id_token, refresh_token, expired, email, account_id, plan_type.
func BuildSub2APIAccount(record map[string]any) *OrderedMap {
	if record == nil {
		record = map[string]any{}
	}
	accessToken := recordString(record, "access_token")
	idToken := recordString(record, "id_token")
	accessClaims := openai.DecodeJWTPayload(accessToken)
	idClaims := openai.DecodeJWTPayload(idToken)
	accessAuth := openai.GetNestedRecord(accessClaims, openaiAuthClaim)
	idAuth := openai.GetNestedRecord(idClaims, openaiAuthClaim)
	accessProfile := openai.GetNestedRecord(accessClaims, openaiProfileClaim)

	expiresAt := ParseExpiredTime(recordString(record, "expired"))
	if expiresAt == 0 {
		expiresAt = pyIntOrZero(accessClaims["exp"])
	}
	issuedAt := pyIntOrZero(accessClaims["iat"])
	expiresIn := int64(864000)
	if expiresAt != 0 && issuedAt != 0 {
		expiresIn = expiresAt - issuedAt
		if expiresIn < 0 {
			expiresIn = 0
		}
	}
	emailAddr := pyFirstNonEmpty(record["email"], accessProfile["email"], idClaims["email"], accessClaims["email"])
	planType := pyFirstNonEmpty(record["plan_type"], accessAuth["chatgpt_plan_type"], idAuth["chatgpt_plan_type"])

	name := emailAddr
	if name == "" {
		name = fmt.Sprintf("openai-%d", sessionconvNowUnix())
	}
	return NewOrderedMap().
		Set("name", name).
		Set("platform", "openai").
		Set("type", "oauth").
		Set("credentials", NewOrderedMap().
			Set("access_token", accessToken).
			Set("chatgpt_account_id", pyFirstNonEmpty(record["account_id"], accessAuth["chatgpt_account_id"], idAuth["chatgpt_account_id"])).
			// app.py:5035 lists access_auth["chatgpt_user_id"] twice; the
			// duplicate is a no-op and is kept so the fallback order matches.
			Set("chatgpt_user_id", pyFirstNonEmpty(accessAuth["chatgpt_user_id"], accessAuth["chatgpt_user_id"], accessAuth["user_id"], accessClaims["sub"])).
			Set("client_id", openai.DefaultClientID).
			Set("email", emailAddr).
			Set("expires_at", expiresAt).
			Set("expires_in", expiresIn).
			Set("id_token", idToken).
			Set("organization_id", ResolveOrganizationID(idClaims, accessClaims)).
			Set("plan_type", planType).
			Set("refresh_token", recordString(record, "refresh_token"))).
		Set("extra", NewOrderedMap().Set("email", emailAddr)).
		Set("concurrency", 10).
		Set("priority", 1).
		Set("rate_multiplier", 1).
		Set("auto_pause_on_expired", true)
}

// BuildSub2APIExport ports build_sub2api_export (app.py:5053-5059) — the
// 导出 sub2api JSON action. Unlike the sub2api branch of
// BuildSessionConversionDocument this one takes raw auth records, not
// Converted values, and stamps exported_at with SECOND precision.
func BuildSub2APIExport(records []map[string]any, now time.Time) *OrderedMap {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	accounts := []any{}
	for _, record := range records {
		accounts = append(accounts, BuildSub2APIAccount(record))
	}
	return NewOrderedMap().
		Set("exported_at", isoSeconds(now)).
		Set("proxies", []any{}).
		Set("accounts", accounts)
}

// AuthRecordMap converts an openai.AuthRecord into the loose dict shape
// BuildSub2APIAccount expects, so callers holding a typed record do not have to
// hand-build the map.
func AuthRecordMap(r openai.AuthRecord) map[string]any {
	return map[string]any{
		"access_token":  r.AccessToken,
		"account_id":    r.AccountID,
		"disabled":      r.Disabled,
		"email":         r.Email,
		"expired":       r.Expired,
		"id_token":      r.IDToken,
		"last_refresh":  r.LastRefresh,
		"refresh_token": r.RefreshToken,
		"type":          r.Type,
		"websockets":    r.Websockets,
	}
}

// recordString mirrors `str(record.get(key) or "")`.
func recordString(record map[string]any, key string) string {
	v := record[key]
	if !pyTruthy(v) {
		return ""
	}
	return pyStr(v)
}
