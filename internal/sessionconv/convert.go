package sessionconv

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/openai"
)

// AxonHubPlaceholderRefreshToken is AXONHUB_PLACEHOLDER_REFRESH_TOKEN
// (app.py:5071) — AxonHub refuses an entry with no refresh_token, so a sentinel
// is written and flagged with axonhub_refresh_token_placeholder.
const AxonHubPlaceholderRefreshToken = "__missing_refresh_token__"

// openaiAuthClaim / openaiProfileClaim are the namespaced JWT claim keys used
// throughout app.py.
const (
	openaiAuthClaim    = "https://api.openai.com/auth"
	openaiProfileClaim = "https://api.openai.com/profile"
)

// ErrMissingAccessToken is the RuntimeError("缺少 accessToken") raised at
// app.py:5218. The UI shows it verbatim in the per-account skip list.
var ErrMissingAccessToken = errors.New("缺少 accessToken")

// sessionconvNowUnix exists only so tests can pin the clock.
//
// Python quirk kept on purpose: synthetic_codex_id_token (app.py:5192) reads
// the WALL CLOCK via int(time.time()) for `iat`, even though its caller was
// handed an explicit `now`. Passing `now` here instead would be a (harmless)
// deviation, and Python wins.
var sessionconvNowUnix = func() int64 { return time.Now().Unix() }

var (
	reNonAlnumRun     = regexp.MustCompile(`[^a-z0-9]+`)
	reEdgeUnderscores = regexp.MustCompile(`^_+|_+$`)
)

// EmailKey ports email_key (app.py:5170-5171): lowercase, collapse every run of
// non [a-z0-9] characters to a single underscore, then trim edge underscores.
//
// pyStrip/pyLower, not TrimSpace/ToLower: "\x1ca@b.c\x1f" must strip to
// "a_b_c" (TrimSpace keeps the separators, so Go used to emit "_a_b_c_"), and
// "İx@y.z" must key as "i_x_y_z" because Python's full lowering of İ appends a
// combining dot that becomes its own separator.
func EmailKey(emailAddr string) string {
	lowered := pyLower(pyStrip(emailAddr))
	return reEdgeUnderscores.ReplaceAllString(reNonAlnumRun.ReplaceAllString(lowered, "_"), "")
}

// Converted is the intermediate returned by convert_chatgpt_session_record
// (app.py:5412-5424). Its own key order is irrelevant — only one member is ever
// serialized, by BuildSessionConversionDocument.
type Converted struct {
	Email                string
	Name                 string
	ExpiresAt            string
	AccessTokenExpiresAt int64

	CPA            *OrderedMap
	Cockpit        *OrderedMap
	NineRouter     any // *OrderedMap, or nil if strip_unavailable emptied it
	CodexAuthJSON  *OrderedMap
	AxonHub        any // *OrderedMap, or nil
	CodexManager   *OrderedMap
	Sub2APIAccount any // *OrderedMap, or nil
}

// EncodeBase64URLJSON ports encode_base64url_json (app.py:5074-5076):
// compact JSON, UTF-8, url-safe base64, padding stripped.
func EncodeBase64URLJSON(value any) (string, error) {
	raw, err := DumpCompactJSON(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString([]byte(raw)), nil
}

// SyntheticCodexIDToken ports synthetic_codex_id_token (app.py:5189-5206).
//
// Codex/CPA consumers require an id_token; when the session has none, an
// unsigned ("alg":"none", literal ".synthetic" signature) JWT carrying the
// chatgpt_* auth claims is fabricated. Returns "" when there is no account_id.
func SyntheticCodexIDToken(emailAddr, accountID, planType, userID, expiresAt string) string {
	if accountID == "" {
		return ""
	}
	now := sessionconvNowUnix()
	authInfo := NewOrderedMap().Set("chatgpt_account_id", accountID)
	if planType != "" {
		authInfo.Set("chatgpt_plan_type", planType)
	}
	if userID != "" {
		authInfo.Set("chatgpt_user_id", userID)
		authInfo.Set("user_id", userID)
	}
	exp := EpochSecondsFromValue(expiresAt)
	if exp == 0 {
		exp = now + 90*24*60*60
	}
	payload := NewOrderedMap().
		Set("iat", now).
		Set("exp", exp).
		Set(openaiAuthClaim, authInfo)
	if emailAddr != "" {
		payload.Set("email", emailAddr)
	}
	header := NewOrderedMap().
		Set("alg", "none").
		Set("typ", "JWT").
		Set("cpa_synthetic", true)
	headerPart, err := EncodeBase64URLJSON(header)
	if err != nil {
		return ""
	}
	payloadPart, err := EncodeBase64URLJSON(payload)
	if err != nil {
		return ""
	}
	return headerPart + "." + payloadPart + ".synthetic"
}

// StripUnavailable ports strip_unavailable (app.py:5155-5167): recursively drop
// nil and "" leaves, drop keys whose value stripped to nothing, and collapse an
// object that lost every key to nil.
//
// Python subtleties kept: 0 and false survive (`0 == ""` is False in Python),
// and an emptied LIST stays as [] while an emptied DICT becomes None.
func StripUnavailable(value any) any {
	switch t := value.(type) {
	case nil:
		return nil
	case *OrderedMap:
		if t == nil {
			return nil
		}
		out := NewOrderedMap()
		for _, key := range t.keys {
			if stripped := StripUnavailable(t.vals[key]); stripped != nil {
				out.Set(key, stripped)
			}
		}
		if out.Len() == 0 {
			return nil
		}
		return out
	case []any:
		out := []any{}
		for _, item := range t {
			if stripped := StripUnavailable(item); stripped != nil {
				out = append(out, stripped)
			}
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return t
	default:
		return value
	}
}

// ClassifyPlanText ports classify_chatgpt_plan_text (app.py:5684-5696).
//
// Local copy: gap G17 owns the canonical version elsewhere; this one exists so
// sessionconv has no cross-gap dependency. Keep the two in sync.
func ClassifyPlanText(value string) string {
	text := pyLower(pyStrip(value))
	text = strings.NewReplacer("_", "", "-", "", " ", "").Replace(text)
	if text == "" {
		return ""
	}
	for _, word := range []string{"enterprise", "business", "team"} {
		if strings.Contains(text, word) {
			return "team"
		}
	}
	for _, word := range []string{"k12", "teacher", "school"} {
		if strings.Contains(text, word) {
			return "k12"
		}
	}
	for _, word := range []string{"chatgptplusplan", "plus", "pro"} {
		if strings.Contains(text, word) {
			return "plus"
		}
	}
	for _, word := range []string{"chatgptfreeplan", "noplan", "free", "none"} {
		if strings.Contains(text, word) {
			return "free"
		}
	}
	return ""
}

// ConvertChatGPTSessionRecord ports convert_chatgpt_session_record
// (app.py:5209-5424): it reads a ChatGPT web-session record (or any of the
// export shapes this tool can round-trip) and builds all seven per-account
// payloads at once.
//
// Pass the zero time.Time to mean Python's `now = now or datetime.now(utc)`.
// Decode the record with ParseSessionRecord so numbers arrive as json.Number.
func ConvertChatGPTSessionRecord(record map[string]any, sourceName string, now time.Time) (Converted, error) {
	if record == nil {
		record = map[string]any{}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	get := func(key string) any { return record[key] }
	nested := func(key string) map[string]any { return openai.GetNestedRecord(record, key) }

	tokensRec := nested("tokens")
	tokenRec := nested("token")
	credentials := nested("credentials")

	accessToken := pyFirstNonEmpty(
		get("accessToken"), get("access_token"),
		tokensRec["accessToken"], tokensRec["access_token"],
		tokenRec["accessToken"], tokenRec["access_token"],
		credentials["accessToken"], credentials["access_token"],
	)
	if accessToken == "" {
		return Converted{}, ErrMissingAccessToken
	}
	sessionToken := pyFirstNonEmpty(
		get("sessionToken"), get("session_token"),
		tokensRec["sessionToken"], tokensRec["session_token"],
		tokenRec["sessionToken"], tokenRec["session_token"],
		credentials["session_token"],
	)
	refreshToken := pyFirstNonEmpty(
		get("refreshToken"), get("refresh_token"), get("openai_rt"),
		tokensRec["refreshToken"], tokensRec["refresh_token"],
		tokenRec["refreshToken"], tokenRec["refresh_token"],
		credentials["refresh_token"],
	)
	inputIDToken := pyFirstNonEmpty(
		get("idToken"), get("id_token"),
		tokensRec["idToken"], tokensRec["id_token"],
		tokenRec["idToken"], tokenRec["id_token"],
		credentials["id_token"],
	)

	payload := openai.DecodeJWTPayload(accessToken)
	idPayload := openai.DecodeJWTPayload(inputIDToken)
	auth := openai.GetNestedRecord(payload, openaiAuthClaim)
	idAuth := openai.GetNestedRecord(idPayload, openaiAuthClaim)
	profile := openai.GetNestedRecord(payload, openaiProfileClaim)
	accessExp := UnixSecondsFromJWTExp(payload["exp"])

	// A record that still carries a refresh_token can always re-mint an access
	// token, so app.py:5243 deliberately blanks the expiry for those.
	//
	// Python evaluates all five arguments before first_non_empty runs, so an
	// out-of-range timestamp in ANY of them aborts the conversion even when an
	// earlier one already won.
	expiresAt := ""
	if refreshToken == "" {
		fromExp, err := TimestampFromUnixSeconds(payload["exp"])
		if err != nil {
			return Converted{}, err
		}
		candidates := []any{fromExp}
		for _, key := range []string{"expires", "expiresAt", "expired", "expires_at"} {
			text, err := NormalizeISOTimestamp(get(key))
			if err != nil {
				return Converted{}, err
			}
			candidates = append(candidates, text)
		}
		expiresAt = pyFirstNonEmpty(candidates...)
	}

	user := nested("user")
	account := nested("account")
	meta := nested("meta")
	providerData := nested("providerSpecificData")

	emailAddr := pyFirstNonEmpty(
		user["email"], get("email"), meta["label"], get("label"), credentials["email"],
		providerData["email"], profile["email"], idPayload["email"], payload["email"], sourceName,
	)
	// app.py:5265 — a 9router export stores the account id in the top-level "id".
	// The guard is a raw `record.get("provider") == "codex"`, so a non-string
	// provider never matches however it stringifies.
	var codexProviderID any = ""
	if s, ok := get("provider").(string); ok && s == "codex" {
		codexProviderID = get("id")
	}
	accountID := pyFirstNonEmpty(
		account["id"], get("account_id"), tokensRec["accountId"], tokensRec["account_id"],
		get("chatgptAccountId"), get("chatgpt_account_id"), meta["chatgptAccountId"], meta["chatgpt_account_id"],
		tokensRec["chatgptAccountId"], tokensRec["chatgpt_account_id"], providerData["chatgptAccountId"],
		providerData["chatgpt_account_id"], credentials["chatgpt_account_id"], auth["chatgpt_account_id"],
		idAuth["chatgpt_account_id"], codexProviderID,
	)
	chatgptAccountID := pyFirstNonEmpty(
		get("chatgptAccountId"), get("chatgpt_account_id"), meta["chatgptAccountId"], meta["chatgpt_account_id"],
		tokensRec["chatgptAccountId"], tokensRec["chatgpt_account_id"], providerData["chatgptAccountId"],
		providerData["chatgpt_account_id"], credentials["chatgpt_account_id"], auth["chatgpt_account_id"],
		idAuth["chatgpt_account_id"],
	)
	workspaceID := pyFirstNonEmpty(
		account["workspaceId"], account["workspace_id"], get("workspaceId"), get("workspace_id"),
		meta["workspaceId"], meta["workspace_id"], providerData["workspaceId"], providerData["workspace_id"],
		credentials["workspace_id"], payload["workspace_id"], idPayload["workspace_id"],
	)
	userID := pyFirstNonEmpty(
		user["id"], get("user_id"), get("chatgptUserId"), providerData["chatgptUserId"],
		providerData["chatgpt_user_id"], auth["chatgpt_user_id"], auth["user_id"],
		idAuth["chatgpt_user_id"], idAuth["user_id"],
	)
	planType := pyFirstNonEmpty(
		// 顶层 plan_type 是刷新 Session 后校正过的结果，必须优先于原始
		// session.account.planType。(comment kept from app.py:5284)
		get("plan_type"), get("chatgpt_plan_type"), get("planType"), get("chatgptPlanType"),
		providerData["chatgptPlanType"], providerData["chatgpt_plan_type"], credentials["plan_type"],
		account["planType"], account["plan_type"],
		auth["chatgpt_plan_type"], idAuth["chatgpt_plan_type"],
	)
	if normalized := ClassifyPlanText(planType); normalized != "" {
		planType = normalized
	}

	exportedAt, err := NormalizeISOTimestamp(now)
	if err != nil {
		return Converted{}, err
	}
	expiresIn := GetExpiresIn(expiresAt, now)
	name := pyFirstNonEmpty(emailAddr, sourceName, "ChatGPT Account")
	sourceType := "chatgpt_web_session"
	if s, ok := get("provider").(string); ok && s == "codex" {
		if a, ok := get("authType").(string); ok && a == "oauth" {
			sourceType = "9router"
		}
	}
	idToken := inputIDToken
	if idToken == "" {
		idToken = SyntheticCodexIDToken(emailAddr, accountID, planType, userID, expiresAt)
	}

	// ---- CPA (app.py:5298-5314). Filter is `v is not None`, so "" survives.
	cpa := NewOrderedMap().
		Set("type", "codex").
		Set("account_id", accountID).
		Set("chatgpt_account_id", accountID).
		Set("email", emailAddr).
		Set("name", name).
		Set("plan_type", planType).
		Set("chatgpt_plan_type", planType).
		Set("id_token", idToken)
	if idToken != "" && inputIDToken == "" {
		cpa.Set("id_token_synthetic", true)
	}
	cpa.Set("access_token", accessToken).
		Set("refresh_token", refreshToken).
		Set("session_token", sessionToken).
		Set("last_refresh", exportedAt).
		Set("expired", expiresAt)
	if pyTruthy(get("disabled")) {
		cpa.Set("disabled", true)
	}

	// ---- Cockpit (app.py:5315-5325). No filtering at all.
	cockpit := NewOrderedMap().
		Set("type", "codex").
		Set("id_token", idToken).
		Set("access_token", accessToken).
		Set("refresh_token", refreshToken).
		Set("account_id", accountID).
		Set("last_refresh", exportedAt).
		Set("email", emailAddr).
		Set("expired", expiresAt).
		Set("account_note", pyFirstNonEmpty(
			get("account_note"), get("accountInfo"), get("account_info"),
			get("note"), get("notes"), get("remark"),
		))

	// ---- sub2api account (app.py:5326-5351), strip_unavailable applied.
	var sub2apiExpiresAt any
	var sub2apiAutoPause any
	if refreshToken == "" {
		sub2apiExpiresAt = accessExp
		if accessExp != 0 {
			sub2apiAutoPause = true
		}
	}
	sub2apiAccount := StripUnavailable(NewOrderedMap().
		Set("name", pyFirstNonEmpty(name, emailAddr, sourceName, "ChatGPT Account")).
		Set("platform", "openai").
		Set("type", "oauth").
		SetNotNil("expires_at", sub2apiExpiresAt).
		SetNotNil("auto_pause_on_expired", sub2apiAutoPause).
		Set("concurrency", 10).
		Set("priority", 1).
		Set("credentials", NewOrderedMap().
			Set("access_token", accessToken).
			Set("chatgpt_account_id", accountID).
			Set("chatgpt_user_id", userID).
			Set("email", emailAddr).
			Set("expires_at", expiresAt).
			SetNotNil("expires_in", expiresIn).
			Set("plan_type", planType)).
		Set("extra", NewOrderedMap().
			Set("email", emailAddr).
			Set("email_key", EmailKey(emailAddr)).
			Set("name", name).
			Set("auth_provider", pyFirstNonEmpty(get("authProvider"), get("auth_provider"))).
			Set("source", sourceType).
			Set("last_refresh", exportedAt)))

	// ---- 9router (app.py:5352-5375), strip_unavailable applied.
	//
	// app.py:5352 is
	//   int(record.get("priority") or 9) if str(record.get("priority") or "").strip().isdigit() else 9
	// so the GATE sees the stripped text while int() sees the raw value. Three
	// Python behaviours ride on that and each is reproduced:
	//   - str.isdigit() accepts every Unicode digit, so "１２" passes and int()
	//     resolves it to 12 (Go's [0-9] check used to fall back to 9);
	//   - int() strips a narrower whitespace set than str.strip(), so
	//     "\x1c7\x1f" passes the gate and then raises ValueError, skipping the
	//     account entirely;
	//   - Python ints are unbounded, so "99999999999999999999999" is written
	//     out in full. The value is carried as json.Number for that reason —
	//     int64 wraps.
	var priority any = json.Number("9")
	rawPriority := get("priority")
	if pyTruthy(rawPriority) && pyIsDigitString(pyStrip(pyStr(rawPriority))) {
		parsed, err := pyIntFromValue(rawPriority)
		if err != nil {
			return Converted{}, err
		}
		priority = parsed
	}
	isActive := !pyTruthy(get("disabled"))
	if b, ok := get("isActive").(bool); ok {
		isActive = b
	}
	createdAt, err := NormalizeISOTimestamp(get("createdAt"))
	if err != nil {
		return Converted{}, err
	}
	if createdAt == "" {
		createdAt = exportedAt
	}
	updatedAt, err := NormalizeISOTimestamp(get("updatedAt"))
	if err != nil {
		return Converted{}, err
	}
	if updatedAt == "" {
		updatedAt = exportedAt
	}
	nineRouter := StripUnavailable(NewOrderedMap().
		Set("accessToken", accessToken).
		Set("refreshToken", refreshToken).
		Set("expiresAt", expiresAt).
		Set("testStatus", pyFirstNonEmpty(get("testStatus"), get("test_status"), "active")).
		SetNotNil("expiresIn", expiresIn).
		Set("providerSpecificData", NewOrderedMap().
			Set("chatgptAccountId", accountID).
			Set("chatgptPlanType", planType)).
		Set("id", accountID).
		Set("provider", "codex").
		Set("authType", "oauth").
		Set("name", name).
		Set("email", emailAddr).
		Set("priority", priority).
		Set("isActive", isActive).
		Set("createdAt", createdAt).
		Set("updatedAt", updatedAt))

	// ---- Codex auth.json (app.py:5376-5386). No filtering; OPENAI_API_KEY is
	// an explicit JSON null.
	codexAuthJSON := NewOrderedMap().
		Set("auth_mode", "chatgpt").
		Set("OPENAI_API_KEY", nil).
		Set("tokens", NewOrderedMap().
			Set("id_token", idToken).
			Set("access_token", accessToken).
			Set("refresh_token", refreshToken).
			Set("account_id", accountID)).
		Set("last_refresh", exportedAt)

	// ---- AxonHub (app.py:5387-5397), strip_unavailable applied.
	axonRefresh := refreshToken
	var axonPlaceholder any
	var axonNote any
	if refreshToken == "" {
		axonRefresh = AxonHubPlaceholderRefreshToken
		axonPlaceholder = true
		axonNote = "refresh_token is a placeholder; access_token works only until it expires."
	}
	axonHub := StripUnavailable(NewOrderedMap().
		Set("auth_mode", "chatgpt").
		Set("last_refresh", GetAxonHubLastRefresh(expiresAt, now)).
		Set("tokens", NewOrderedMap().
			Set("access_token", accessToken).
			Set("refresh_token", axonRefresh).
			Set("id_token", idToken)).
		SetNotNil("axonhub_refresh_token_placeholder", axonPlaceholder).
		SetNotNil("axonhub_note", axonNote))

	// ---- Codex-Manager (app.py:5398-5411). tokens keeps id_token as the RAW
	// input token (never the synthetic one); meta drops falsy values.
	codexManagerTokens := NewOrderedMap().
		Set("access_token", accessToken).
		Set("refresh_token", refreshToken).
		Set("id_token", inputIDToken)
	if accountID != "" {
		codexManagerTokens.Set("account_id", accountID)
	}
	if chatgptAccountID != "" {
		codexManagerTokens.Set("chatgpt_account_id", chatgptAccountID)
	}
	codexManagerMeta := NewOrderedMap().
		SetTruthy("label", pyFirstNonEmpty(name, emailAddr, sourceName, "ChatGPT Account")).
		SetTruthy("workspace_id", workspaceID).
		SetTruthy("chatgpt_account_id", chatgptAccountID).
		SetTruthy("note", "Imported from ChatGPT session")
	codexManager := NewOrderedMap().
		Set("tokens", codexManagerTokens).
		Set("meta", codexManagerMeta)

	return Converted{
		Email:                emailAddr,
		Name:                 name,
		ExpiresAt:            expiresAt,
		AccessTokenExpiresAt: accessExp,
		CPA:                  cpa,
		Cockpit:              cockpit,
		NineRouter:           nineRouter,
		CodexAuthJSON:        codexAuthJSON,
		AxonHub:              axonHub,
		CodexManager:         codexManager,
		Sub2APIAccount:       sub2apiAccount,
	}, nil
}
