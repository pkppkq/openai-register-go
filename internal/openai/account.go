package openai

// Account type / plan classification and the OAuth refresh-token grant.
//
// Ported from app.py:
//   - is_openai_refresh_token            app.py:5449-5451
//   - summarize_chatgpt_access_token     app.py:5480-5492
//   - is_transient_http_error            app.py:5604-5628
//   - refresh_openai_access_token        app.py:5645-5674   (G9)
//   - PAID_CHATGPT_PLAN_TYPES            app.py:5677
//   - normalize_payload_key              app.py:5680-5681
//   - classify_chatgpt_plan_text         app.py:5684-5696   (G17)
//   - apply_inferred_plan_to_summary     app.py:5699-5710   (G17)
//   - infer_account_type_from_payload    app.py:5713-5786
//   - chatgpt_team_workspace_...payload  app.py:5808-5852
//   - merge_chatgpt_backend_plan_summary app.py:5855-5880   (G17)
//   - detect_openai_account_type         app.py:5883-5919   (G10)
//   - timestamp_from_unix_seconds        app.py:5100-5107 / normalize_iso_timestamp app.py:5079-5097
//
// Every package-level helper added here is prefixed `account`/`Account` so it
// cannot collide with another agent adding a file to this shared package.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

// AccountPaidPlanTypes mirrors PAID_CHATGPT_PLAN_TYPES (app.py:5677). Note that
// "pro" is a member here even though ClassifyChatGPTPlanText never *returns*
// "pro" (it folds pro into plus) — the set is also consulted with raw, unclassified
// plan strings coming from the UI 套餐覆盖 combo, which does offer "pro".
var AccountPaidPlanTypes = map[string]bool{"plus": true, "team": true, "k12": true, "pro": true}

// AccountIsOpenAIRefreshToken mirrors is_openai_refresh_token (app.py:5449):
// a trimmed value starting with "rt_" or "rt.".
func AccountIsOpenAIRefreshToken(value string) bool {
	text := pyStrip(value)
	return strings.HasPrefix(text, "rt_") || strings.HasPrefix(text, "rt.")
}

// AccountNormalizePayloadKey mirrors normalize_payload_key (app.py:5680):
// lowercase, then strip everything that is not [a-z0-9].
func AccountNormalizePayloadKey(key string) string {
	return accountNonAlnumRe.ReplaceAllString(pyLower(key), "")
}

var accountNonAlnumRe = regexp.MustCompile(`[^a-z0-9]`)

// ---------------------------------------------------------------------------
// G17: plan classification
// ---------------------------------------------------------------------------

// ClassifyChatGPTPlanText mirrors classify_chatgpt_plan_text (app.py:5684).
//
// The BRANCH ORDER is the whole point of this function and must not be
// reordered or merged:
//  1. enterprise|business|team -> "team"   (so "school team" is team, not k12)
//  2. k12|teacher|school       -> "k12"
//  3. chatgptplusplan|plus|pro -> "plus"   (Pro deliberately folds into plus;
//     substring match means "product"/"professional" also land here)
//  4. chatgptfreeplan|noplan|free|none -> "free"
//  5. otherwise ""
//
// Normalization is Python's: strip, lower, then delete "_", "-" and " " (only
// those three — other punctuation survives, unlike AccountNormalizePayloadKey).
func ClassifyChatGPTPlanText(value string) string {
	// str.strip() removes four code points TrimSpace does not (U+001C..U+001F),
	// and str.lower() maps U+0130 to TWO runes: "BUSİNESS".lower() is
	// "busi̇ness", which does NOT contain "business", so Python classifies it as
	// "" while strings.ToLower answered "team". pytext.go carries both rules.
	text := pyStrip(value)
	text = pyLower(text)
	text = strings.NewReplacer("_", "", "-", "", " ", "").Replace(text)
	if text == "" {
		return ""
	}
	if accountContainsAny(text, "enterprise", "business", "team") {
		return "team"
	}
	if accountContainsAny(text, "k12", "teacher", "school") {
		return "k12"
	}
	if accountContainsAny(text, "chatgptplusplan", "plus", "pro") {
		return "plus"
	}
	if accountContainsAny(text, "chatgptfreeplan", "noplan", "free", "none") {
		return "free"
	}
	return ""
}

func accountContainsAny(text string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// SummarizeChatGPTAccessToken mirrors summarize_chatgpt_access_token (app.py:5480).
//
// The result is a mutable bag, not a struct, because apply_inferred_plan_to_summary
// writes dynamic "<source>_plan_type"/"<source>_plan_detail" keys into it and the
// whole thing is persisted as the account's `access_summary`.
//
// DIVERGENCE: Python dicts keep insertion order, so json.dumps of a summary emits
// plan_type/account_id/... in that order; Go's encoding/json sorts map keys. Only
// the on-disk key order differs — no consumer reads by position.
func SummarizeChatGPTAccessToken(accessToken string) map[string]any {
	claims := DecodeJWTPayload(accessToken)
	auth := GetNestedRecord(claims, "https://api.openai.com/auth")
	accountID := FirstNonEmpty(auth["chatgpt_account_id"], auth["account_id"])
	userID := FirstNonEmpty(auth["chatgpt_user_id"], auth["user_id"])
	return map[string]any{
		"plan_type":       FirstNonEmpty(auth["chatgpt_plan_type"], auth["plan_type"], "unknown"),
		"account_id":      accountID,
		"account_id_tail": accountTail(accountID, 8),
		"user_id":         userID,
		"user_id_tail":    accountTail(userID, 8),
		"expires_at":      accountTimestampFromUnixSeconds(claims["exp"]),
	}
}

// accountTail mirrors Python's str(value)[-n:] — character (rune) based, and a
// short string is returned whole rather than padded or erroring.
func accountTail(value string, n int) string {
	runes := []rune(value)
	if len(runes) <= n {
		return value
	}
	return string(runes[len(runes)-n:])
}

// accountTimestampFromUnixSeconds mirrors timestamp_from_unix_seconds (app.py:5100)
// composed with normalize_iso_timestamp (app.py:5079): non-numeric or <=0 gives "",
// values above 1e11 are milliseconds, and the output carries exactly 3 fractional
// digits (Python isoformat(timespec="milliseconds")) with "+00:00" replaced by "Z".
func accountTimestampFromUnixSeconds(value any) string {
	numeric, ok := accountFloat(value)
	if !ok || !(numeric > 0) {
		return ""
	}
	seconds := numeric
	if seconds > 1e11 {
		seconds = seconds / 1000
	}
	whole := math.Floor(seconds)
	// datetime.fromtimestamp lands on the MICROSECOND grid before
	// isoformat(timespec="milliseconds") truncates, and CPython's round() is
	// half-to-even. Rounding straight to nanoseconds instead reports a millisecond
	// low whenever the µs step would have carried across a ms boundary
	// (e.g. 0.0019995s -> Python 002, nanosecond rounding 001).
	micros := int64(math.RoundToEven((seconds - whole) * 1e6))
	if micros >= 1e6 {
		whole++
		micros -= 1e6
	}
	return time.Unix(int64(whole), micros*1000).UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// accountFloat mirrors Python float(value) with its try/except: numbers convert,
// numeric strings convert (Python float() tolerates surrounding whitespace, so we
// trim), everything else fails.
func accountFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case nil:
		return 0, false
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		// float() strips Python whitespace, which TrimSpace under-does, and
		// accepts a Unicode-digit spelling ("１７００００００００") that ParseFloat
		// rejects outright.
		text := pyStrip(v)
		if f, err := strconv.ParseFloat(text, 64); err == nil {
			return f, true
		}
		if folded := pyFoldDigits(text); folded != text {
			f, err := strconv.ParseFloat(folded, 64)
			return f, err == nil
		}
		// DIVERGENCE: ParseFloat also accepts Go-only spellings float() refuses
		// (hex floats, "0x1p4"). Only reachable from a JWT claim that is a string
		// rather than a number, and a hex float there is not a timestamp anyway.
		return 0, false
	default:
		return 0, false
	}
}

// ApplyInferredPlanToSummary mirrors apply_inferred_plan_to_summary (app.py:5699).
//
// It records "<source>_plan_type"/"<source>_plan_detail" unconditionally, but only
// promotes the plan to the top-level "plan_type" when the new plan is paid OR the
// current plan is empty/unknown/free — i.e. a backend/session "plus" beats a stale
// "free" baked into the accessToken, while a backend "free" never demotes a paid one.
//
// DIVERGENCE: Python would raise on a None summary; a nil Go map is silently
// replaced by a fresh one (the caller gets it back as the return value).
func ApplyInferredPlanToSummary(summary map[string]any, inferredPlan, inferredDetail, source string) map[string]any {
	if summary == nil {
		summary = map[string]any{}
	}
	// Python lowercases the RAW inferred plan here — it is not re-classified, so a
	// UI override of "Pro" stays "pro" instead of folding to "plus".
	plan := pyLower(pyStrip(inferredPlan))
	if plan == "" {
		return summary
	}
	sourceKey := AccountNormalizePayloadKey(source)
	if sourceKey == "" {
		sourceKey = "payload"
	}
	summary[sourceKey+"_plan_type"] = plan
	summary[sourceKey+"_plan_detail"] = inferredDetail
	currentPlan := pyLower(pyStrip(accountPyStr(summary["plan_type"])))
	if AccountPaidPlanTypes[plan] || currentPlan == "" || currentPlan == "unknown" || currentPlan == "free" {
		summary["plan_type"] = plan
	}
	return summary
}

// accountBoolPaidKeys mirrors bool_paid_keys (app.py:5719).
var accountBoolPaidKeys = map[string]bool{
	"ispaidsubscriptionactive": true,
	"hasactivesubscription":    true,
	"hasactivepaidplan":        true,
	"haspaidsubscription":      true,
	"isplususer":               true,
	"isplus":                   true,
	"ispro":                    true,
	"ispaid":                   true,
	"ispaidsubscriber":         true,
	"ispayingcustomer":         true,
	"issubscribed":             true,
}

// accountPlanTextKeys mirrors plan_text_keys (app.py:5732).
var accountPlanTextKeys = map[string]bool{
	"subscriptionplan": true,
	"subscriptiontype": true,
	"plantype":         true,
	"chatgptplantype":  true,
	"plan":             true,
	"accountplan":      true,
	"accounttype":      true,
	"productname":      true,
	"product":          true,
	"sku":              true,
	"tier":             true,
	"currentplan":      true,
	"planname":         true,
	"billingplan":      true,
	"name":             true,
}

// accountNamePathRe mirrors the guard at app.py:5764 — bare "name" only counts on a
// plan/product/subscription/billing/sku/account path, so a user's display name is
// never mistaken for a plan.
var accountNamePathRe = regexp.MustCompile(`(?i)(plan|product|subscription|billing|sku|account)`)

// accountPlanPriority mirrors the priority table at app.py:5769.
var accountPlanPriority = map[string]int{"team": 4, "k12": 3, "plus": 2, "free": 1}

type accountStackItem struct {
	value any
	path  string
}

// InferAccountTypeFromPayload mirrors infer_account_type_from_payload (app.py:5713):
// an iterative LIFO walk over any JSON value collecting (a) boolean "is paid" flags
// and (b) plan-ish string fields, returning the highest-priority paid plan, else a
// bare paid boolean as "plus", else "free", else ("", "未发现明确套餐字段").
//
// Object members are visited in JSON document order when the payload was decoded
// through DecodeOrderedJSON, matching Python exactly. A payload handed in as a plain
// map[string]any falls back to sorted order (see accountObjectEntries); the RETURNED
// PLAN survives that fallback unchanged — paid selection is by strict priority,
// booleans collapse to "plus", free is free — only the `detail` string can name a
// different field when several fields tie.
func InferAccountTypeFromPayload(payload any) (string, string) {
	foundFree := ""
	foundPaid := ""
	bestPlan := ""
	bestDetail := ""
	bestPriority := 0

	stack := []accountStackItem{{value: payload, path: "payload"}}
	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if entries, isObject := accountObjectEntries(item.value); isObject {
			for _, e := range entries {
				key, value := e.key, e.value
				normalizedKey := AccountNormalizePayloadKey(key)
				fieldPath := item.path + "." + key
				if accountBoolPaidKeys[normalizedKey] {
					// Python compares with `is True` / `is False`: only real bools count.
					if b, ok := value.(bool); ok {
						if b {
							if foundPaid == "" {
								foundPaid = fieldPath + "=true"
							}
						} else if foundFree == "" {
							foundFree = fieldPath + "=false"
						}
					}
				}
				if s, ok := value.(string); ok && accountPlanTextKeys[normalizedKey] {
					if normalizedKey != "name" || accountNamePathRe.MatchString(fieldPath) {
						if plan := ClassifyChatGPTPlanText(s); plan != "" {
							priority := accountPlanPriority[plan]
							detail := fieldPath + "=" + s
							// Strict > : the FIRST field at the winning priority keeps the detail.
							if AccountPaidPlanTypes[plan] && priority > bestPriority {
								bestPlan = plan
								bestDetail = detail
								bestPriority = priority
							} else if plan == "free" && foundFree == "" {
								foundFree = detail
							}
						}
					}
				}
				if accountIsContainer(value) {
					stack = append(stack, accountStackItem{value: value, path: fieldPath})
				}
			}
		} else if list, isList := item.value.([]any); isList {
			for index, value := range list {
				stack = append(stack, accountStackItem{value: value, path: fmt.Sprintf("%s[%d]", item.path, index)})
			}
		}
	}
	if bestPlan != "" {
		return bestPlan, bestDetail
	}
	if foundPaid != "" {
		return "plus", foundPaid
	}
	if foundFree != "" {
		return "free", foundFree
	}
	return "", "未发现明确套餐字段"
}

// accountIsContainer mirrors isinstance(value, (dict, list)) for decoded JSON.
func accountIsContainer(value any) bool {
	switch value.(type) {
	case *teamObject, map[string]any, []any:
		return true
	}
	return false
}

// accountEntry is one member of a decoded JSON object.
type accountEntry struct {
	key   string
	value any
}

// accountObjectEntries yields a decoded JSON object's members in the order CPython
// would iterate the dict, and reports whether the value was an object at all
// (isinstance(value, dict)).
//
// *teamObject — what DecodeOrderedJSON produces — keeps JSON document order, which
// is exactly CPython's dict order. A plain map[string]any has already thrown that
// order away before this package ever sees it, so sorting is the only deterministic
// fallback left; callers on an order-sensitive path must decode through
// DecodeOrderedJSON instead of json.Unmarshal.
func accountObjectEntries(value any) ([]accountEntry, bool) {
	switch node := value.(type) {
	case *teamObject:
		keys := node.Keys()
		entries := make([]accountEntry, 0, len(keys))
		for _, key := range keys {
			entries = append(entries, accountEntry{key: key, value: node.Get(key)})
		}
		return entries, true
	case map[string]any:
		entries := make([]accountEntry, 0, len(node))
		for _, key := range accountSortedKeys(node) {
			entries = append(entries, accountEntry{key: key, value: node[key]})
		}
		return entries, true
	}
	return nil, false
}

// accountObjectGet is dict.get over either object representation; nil when the
// container is not an object or the key is absent.
func accountObjectGet(value any, key string) any {
	switch node := value.(type) {
	case *teamObject:
		return node.Get(key)
	case map[string]any:
		return node[key]
	}
	return nil
}

// DecodeOrderedJSON decodes a JSON document with object key order preserved, so the
// payload walkers in this file iterate members the way CPython does.
//
// Anything whose result feeds a Team workspace choice MUST decode through this:
// accountTeamWorkspaceFromBackendPayload breaks a role-score tie by document order,
// and the id it returns is the workspace a billable invite seat is created in.
func DecodeOrderedJSON(data []byte) (any, error) { return teamDecodeOrderedJSON(data) }

func accountSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// accountTeamWorkspace is the dict returned by chatgpt_team_workspace_from_backend_payload.
type accountTeamWorkspace struct {
	AccountID string
	PlanType  string
	Role      string
	Detail    string
}

var accountOwnerRoles = map[string]bool{
	"owner": true, "account-owner": true, "account_owner": true,
	"admin": true, "administrator": true,
}

// accountTeamWorkspaceFromBackendPayload mirrors
// chatgpt_team_workspace_from_backend_payload (app.py:5808): scan payload["accounts"]
// (dict or list) for entries whose inferred plan classifies as "team", then prefer an
// owner/admin role. Python's sort(reverse=True) is stable, so among equal role scores
// the first entry encountered wins — sort.SliceStable reproduces that.
//
// The tie-break makes document order load-bearing, and the id this returns is the
// workspace a billable Team invite seat is created in: decode `payload` through
// DecodeOrderedJSON. A map[string]any payload can only be visited in sorted order and
// will pick a different workspace whenever two team entries share a role score.
func accountTeamWorkspaceFromBackendPayload(payload any) accountTeamWorkspace {
	if _, isObject := accountObjectEntries(payload); !isObject {
		return accountTeamWorkspace{}
	}
	accounts := accountObjectGet(payload, "accounts")
	entries, isObject := accountObjectEntries(accounts)
	if !isObject {
		// A list carries no keys, so the account id must come from the entry body.
		if list, isList := accounts.([]any); isList {
			for _, value := range list {
				entries = append(entries, accountEntry{value: value})
			}
		}
	}

	type candidate struct {
		score     int
		workspace accountTeamWorkspace
	}
	var candidates []candidate
	for _, e := range entries {
		value := e.value
		if _, ok := accountObjectEntries(value); !ok {
			continue
		}
		account := accountObjectGet(value, "account")
		inferredPlan, detail := InferAccountTypeFromPayload(value)
		if ClassifyChatGPTPlanText(inferredPlan) != "team" {
			continue
		}
		accountID := FirstNonEmpty(accountObjectGet(account, "account_id"), accountObjectGet(account, "id"),
			accountObjectGet(value, "account_id"), accountObjectGet(value, "id"), e.key)
		if accountID == "" {
			continue
		}
		role := FirstNonEmpty(accountObjectGet(account, "account_user_role"), accountObjectGet(account, "role"),
			accountObjectGet(value, "account_user_role"), accountObjectGet(value, "role"))
		roleScore := 1
		if accountOwnerRoles[pyCaseFold(pyStrip(role))] {
			roleScore = 3
		}
		candidates = append(candidates, candidate{
			score: roleScore,
			workspace: accountTeamWorkspace{
				AccountID: accountID,
				PlanType:  "team",
				Role:      role,
				Detail:    detail,
			},
		})
	}
	if len(candidates) == 0 {
		return accountTeamWorkspace{}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	return candidates[0].workspace
}

// MergeChatGPTBackendPlanSummary mirrors merge_chatgpt_backend_plan_summary
// (app.py:5855). backendResults is whatever the caller collected from the
// /backend-api probes: a list of {"endpoint","status","payload"} objects (that is
// exactly the shape the page.evaluate at app.py:22401 returns), passed as []any,
// []map[string]any or nil.
//
// Branch order matters: the FIRST result that infers a PAID plan wins and returns
// immediately (a later "team" cannot beat an earlier "plus"); a free result is only
// remembered as a fallback, and only the first free one.
func MergeChatGPTBackendPlanSummary(summary map[string]any, backendResults any) map[string]any {
	if summary == nil {
		summary = map[string]any{}
	}
	fallbackSet := false
	fallbackPlan := ""
	fallbackDetail := ""
	for _, item := range accountBackendItems(backendResults) {
		// Python: str(item.get("endpoint") or "backend") — falsy endpoints fall back.
		endpoint := accountPyStr(accountObjectGet(item, "endpoint"))
		if endpoint == "" {
			endpoint = "backend"
		}
		statusCode := accountPyStr(accountObjectGet(item, "status"))
		payload := accountObjectGet(item, "payload")
		if !accountIsContainer(payload) {
			continue
		}
		inferredPlan, inferredDetail := InferAccountTypeFromPayload(payload)
		if inferredPlan == "" {
			continue
		}
		detail := fmt.Sprintf("%s HTTP %s -> %s", endpoint, statusCode, inferredDetail)
		workspace := accountTeamWorkspaceFromBackendPayload(payload)
		if workspace.AccountID != "" {
			summary["backend_workspace_id"] = workspace.AccountID
			summary["team_workspace_id"] = workspace.AccountID
			summary["backend_workspace_role"] = workspace.Role
		}
		if AccountPaidPlanTypes[inferredPlan] {
			return ApplyInferredPlanToSummary(summary, inferredPlan, detail, "backend")
		}
		if !fallbackSet {
			fallbackSet = true
			fallbackPlan = inferredPlan
			fallbackDetail = detail
		}
	}
	if fallbackSet {
		ApplyInferredPlanToSummary(summary, fallbackPlan, fallbackDetail, "backend")
	}
	return summary
}

// accountBackendItems normalizes the loosely typed backend result list. Python's
// guard is `backend_results if isinstance(backend_results, list) else []` plus a
// per-item `isinstance(item, dict)` skip.
// Items stay untyped because a DecodeOrderedJSON payload arrives as *teamObject;
// read their fields with accountObjectGet.
func accountBackendItems(value any) []any {
	var out []any
	switch list := value.(type) {
	case []any:
		for _, item := range list {
			if _, isObject := accountObjectEntries(item); isObject {
				out = append(out, item)
			}
		}
	case []map[string]any:
		for _, item := range list {
			out = append(out, item)
		}
	}
	return out
}

// accountPyStr mirrors Python's str(value or ""): falsy values (nil, "", 0, false)
// render as "".
//
// DIVERGENCE: the browser bridge hands Python an int status (200 -> "200") while Go's
// JSON decode yields float64(200); integral floats are printed without a fraction so
// the detail string reads "... HTTP 200 -> ..." exactly like Python's.
func accountPyStr(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return ""
	case float64:
		if v == 0 {
			return ""
		}
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'g', -1, 64)
	case int:
		if v == 0 {
			return ""
		}
		return strconv.Itoa(v)
	case int64:
		if v == 0 {
			return ""
		}
		return strconv.FormatInt(v, 10)
	case json.Number:
		if v.String() == "0" {
			return ""
		}
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ---------------------------------------------------------------------------
// G9: refresh_token grant
// ---------------------------------------------------------------------------

// accountTransientMarkers mirrors is_transient_http_error (app.py:5606-5627),
// verbatim and in order. Kept literal even though several entries are curl_cffi
// specific and Go's transport errors read differently (notably a bare "EOF" does not
// match "eof occurred") — Python wins on the retry policy.
var accountTransientMarkers = []string{
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

// accountIsTransientHTTPError mirrors is_transient_http_error (app.py:5604).
func accountIsTransientHTTPError(text string) bool {
	// "ssl" is one of the markers, and casefold turns a ß into "ss".
	lowered := pyCaseFold(text)
	for _, marker := range accountTransientMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

const (
	accountRefreshTimeoutSeconds = 45 // app.py:5657
	accountDetectTimeoutSeconds  = 30 // app.py:5909
	accountMaxAttempts           = 3  // app.py:5651 range(1, 4)
)

// RefreshOpenAIAccessToken mirrors refresh_openai_access_token (app.py:5645) — the
// OAuth refresh_token grant. It walks AuthOAuthTokenURLs in order, 3 attempts each,
// retrying only on a transient transport error or HTTP 5xx, and returns the raw token
// payload (the caller reads access_token / refresh_token / id_token from it).
//
// DIVERGENCE: Python posts with curl_cffi (impersonate=chrome136); Go uses
// tlsclient, which is this port's curl_cffi replacement (same Chrome JA3).
func RefreshOpenAIAccessToken(refreshToken, proxyURL string) (map[string]any, error) {
	if !AccountIsOpenAIRefreshToken(refreshToken) {
		return nil, fmt.Errorf("当前保存的 rt_token 不是有效 OpenAI refresh_token，请重新授权获取 RT")
	}
	client, err := tlsclient.New(proxyURL, accountRefreshTimeoutSeconds)
	if err != nil {
		return nil, fmt.Errorf("OpenAI RT 刷新 access_token 失败: %v", err)
	}
	headers := OpenAIBrowserHeaders(map[string]string{
		"accept":       "application/json",
		"content-type": "application/x-www-form-urlencoded",
	})
	// Python's requests encodes the data dict in insertion order; url.Values.Encode
	// would sort the keys, so the body is built by hand to keep grant_type first.
	body := []byte(accountFormEncode([][2]string{
		{"grant_type", "refresh_token"},
		{"client_id", DefaultClientID},
		{"refresh_token", refreshToken},
	}))

	lastError := ""
	for _, tokenURL := range AuthOAuthTokenURLs {
		for attempt := 1; attempt <= accountMaxAttempts; attempt++ {
			status, raw, err := client.DoSimple("POST", tokenURL, body, headers)
			if err != nil {
				lastError = fmt.Sprintf("endpoint=%s %v", tokenURL, err)
				if attempt < accountMaxAttempts && accountIsTransientHTTPError(err.Error()) {
					accountBackoff(attempt)
					continue
				}
				break
			}
			if accountHTTPOK(status) {
				var payload map[string]any
				if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
					// Python does response.json() OUTSIDE the try, so a non-JSON 2xx body
					// aborts the whole function instead of falling through to a retry.
					return nil, fmt.Errorf("OpenAI RT 刷新 access_token 响应不是 JSON 对象: endpoint=%s HTTP %d %s",
						tokenURL, status, accountTruncate(string(raw), 300))
				}
				if accountPyStr(payload["access_token"]) != "" {
					return payload, nil
				}
			}
			lastError = fmt.Sprintf("endpoint=%s HTTP %d %s", tokenURL, status, accountTruncate(string(raw), 300))
			if attempt < accountMaxAttempts && status >= 500 {
				accountBackoff(attempt)
				continue
			}
			break
		}
	}
	return nil, fmt.Errorf("OpenAI RT 刷新 access_token 失败: %s", lastError)
}

// accountBackoff mirrors time.sleep(min(5, attempt * 1.2)) (app.py:5662/5671).
func accountBackoff(attempt int) {
	delay := time.Duration(float64(attempt) * 1.2 * float64(time.Second))
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	time.Sleep(delay)
}

// accountHTTPOK mirrors requests' Response.ok (status < 400).
func accountHTTPOK(status int) bool { return status >= 200 && status < 400 }

// accountTruncate mirrors Python's text[:n] — rune based, not bytes.
func accountTruncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func accountFormEncode(pairs [][2]string) string {
	var b strings.Builder
	for i, kv := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(kv[0]))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(kv[1]))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// G10: account type detection (刷新类型)
// ---------------------------------------------------------------------------

// DetectOpenAIAccountType mirrors detect_openai_account_type (app.py:5883).
// It refreshes the RT, then probes the ChatGPT backend endpoints IN ORDER and
// returns the first one whose payload yields a plan.
//
// Returns (accountType, detail, newRefreshToken, error). newRefreshToken is the
// rotated RT when the grant returned one, else the RT that was passed in — the caller
// (刷新类型) writes it back to the account.
//
// DIVERGENCE: Python probes the backend with plain requests; Go uses tlsclient so the
// TLS fingerprint is Chrome's — a stock Go client is blocked by Cloudflare here.
func DetectOpenAIAccountType(refreshToken, proxyURL string) (string, string, string, error) {
	tokenPayload, err := RefreshOpenAIAccessToken(refreshToken, proxyURL)
	if err != nil {
		return "", "", "", err
	}
	accessToken := accountPyStr(tokenPayload["access_token"])
	newRT := accountPyStr(tokenPayload["refresh_token"])
	if newRT == "" {
		newRT = refreshToken
	}
	accessClaims := DecodeJWTPayload(accessToken)
	authClaim := GetNestedRecord(accessClaims, "https://api.openai.com/auth")
	accountID := FirstNonEmpty(authClaim["chatgpt_account_id"], authClaim["account_id"])

	client, err := tlsclient.New(proxyURL, accountDetectTimeoutSeconds)
	if err != nil {
		return "", "", "", fmt.Errorf("无法判断 Free/Plus: %v", err)
	}
	headers := OpenAIBrowserHeaders(map[string]string{
		"accept":        "application/json",
		"authorization": "Bearer " + accessToken,
		"origin":        ChatGPTBaseURL,
		"referer":       ChatGPTBaseURL + "/",
	})

	endpoints := []string{ChatGPTBaseURL + "/backend-api/accounts/check/v4-2023-04-27"}
	if accountID != "" {
		// Python interpolates the id raw (no quoting) — kept identical.
		endpoints = append(endpoints, ChatGPTBaseURL+"/backend-api/accounts/"+accountID+"/subscription")
	}
	endpoints = append(endpoints,
		ChatGPTBaseURL+"/backend-api/me",
		ChatGPTBaseURL+"/backend-api/models",
	)

	var errors []string
	for _, endpoint := range endpoints {
		status, raw, err := client.DoSimple("GET", endpoint, nil, headers)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}
		if !accountHTTPOK(status) {
			errors = append(errors, fmt.Sprintf("%s: HTTP %d", endpoint, status))
			continue
		}
		// Ordered decode so the reported field path matches Python's walk order.
		payload, err := DecodeOrderedJSON(raw)
		if err != nil {
			// response.json() sits INSIDE the try here (unlike the refresh helper), so a
			// bad body is just another recorded error and the walk continues.
			errors = append(errors, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}
		accountType, detail := InferAccountTypeFromPayload(payload)
		if accountType != "" {
			return accountType, fmt.Sprintf("%s -> %s", endpoint, detail), newRT, nil
		}
	}
	return "", "", "", fmt.Errorf("无法判断 Free/Plus: %s", strings.Join(accountLastN(errors, 3), " | "))
}

// accountLastN mirrors Python's errors[-3:].
func accountLastN(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[len(values)-n:]
}
