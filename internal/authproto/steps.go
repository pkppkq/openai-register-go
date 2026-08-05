package authproto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

// ---------------------------------------------------------------------------
// _fetch_sentinel_token (app.py:8109-8167)
// ---------------------------------------------------------------------------

// sentinelHeaders is the LITERAL dict of app.py:8113-8120, in its insertion
// order. Note this call does NOT go through _headers(): it sends its own
// seven-header set, so the sec-ch-ua-full-version-list / platform-version /
// viewport-width hints the rest of the flow sends are deliberately absent here.
func sentinelHeaders() []headerPair {
	return []headerPair{
		{"content-type", "text/plain;charset=UTF-8"},
		{"origin", "https://sentinel.openai.com"},
		{"referer", "https://sentinel.openai.com/backend-api/sentinel/frame.html"},
		{"user-agent", openai.DefaultUserAgent},
		{"sec-ch-ua", `"Google Chrome";v="146", "Chromium";v="146", "Not.A/Brand";v="24"`},
		{"sec-ch-ua-mobile", "?0"},
		{"sec-ch-ua-platform", `"Windows"`},
	}
}

// fetchSentinelToken mirrors _fetch_sentinel_token (app.py:8109-8167): ask
// sentinel for the requirements, solve the proof of work if one is demanded,
// and return the serialized openai-sentinel-token header value.
func (f *Flow) fetchSentinelToken(flow string) (string, error) {
	reqToken := generateSentinelRequirementsToken()
	// data=json.dumps({...}, separators=(",", ":")) — ensure_ascii defaults to
	// True here (unlike base64_json, which passes False).
	body := pyJSONDumps(newOrderedMap(
		"p", reqToken,
		"id", f.deviceID,
		"flow", flow,
	), true, true)
	resp, err := f.transport.Do(&Request{
		Method:          "POST",
		URL:             sentinelRequirementsURL,
		Header:          buildHeader(sentinelHeaders()),
		Body:            []byte(body),
		FollowRedirects: true,
	})
	if err != nil {
		return "", err
	}
	if !resp.OK() {
		return "", fmt.Errorf("请求 sentinel requirements 失败: %d body=%s",
			resp.StatusCode, truncRunes(resp.Text(), 300))
	}
	requirements, err := openai.DecodeOrderedJSON(resp.Body)
	if err != nil {
		return "", err
	}

	// `(requirements.get("turnstile") or {}).get("dx")`
	turnstileMeta := objGet(requirements, "turnstile")
	if pyTruthy(objGet(turnstileMeta, "dx")) {
		f.handleTurnstileRequirement(turnstileMeta)
	}

	// `pow_data = requirements.get("proofofwork") or {}`
	powData := objGet(requirements, "proofofwork")
	proof := generateSentinelRequirementsToken()
	if pyTruthy(objGet(powData, "required")) &&
		pyTruthy(objGet(powData, "seed")) &&
		pyTruthy(objGet(powData, "difficulty")) {
		proof = generateSentinelProofToken(pyStr(objGet(powData, "seed")), pyStr(objGet(powData, "difficulty")))
	}

	token := pyStrip(pyStrOr(objGet(requirements, "token")))
	if token == "" {
		// json.dumps of the PARSED payload with ensure_ascii=False and the
		// DEFAULT separators (", " / ": "), not the compact ones.
		return "", fmt.Errorf("请求 sentinel token 失败: body=%s",
			truncRunes(pyJSONDumps(requirements, false, false), 300))
	}
	// app.py:8162's `if token:` is dead — the check above already returned.
	f.transport.SetCookie("oai-sc", "0"+token, ".openai.com")

	return pyJSONDumps(newOrderedMap(
		"p", proof,
		"t", "",
		"c", token,
		"id", f.deviceID,
		"flow", flow,
	), true, true), nil
}

// handleTurnstileRequirement mirrors app.py:8129-8154. It never fails the
// request: every branch only logs, and the flow continues regardless.
func (f *Flow) handleTurnstileRequirement(turnstileMeta any) {
	if !f.turnstileSolverEnabled {
		f.log.emit("Sentinel 返回 Turnstile 提示；未启用 solver，协议模式先继续尝试；若被拦截请换代理或改用浏览器流程")
		return
	}
	// `str(a or b or c or "").strip()` — dict-key precedence is sitekey,
	// siteKey, site_key, and Python's `or` skips a present-but-empty value.
	sitekey := pyStrip(pyStrOr(orChain(
		objGet(turnstileMeta, "sitekey"),
		objGet(turnstileMeta, "siteKey"),
		objGet(turnstileMeta, "site_key"),
	)))
	pageURL := openai.AuthBaseURL + "/api/accounts/authorize/continue"
	if sitekey == "" {
		f.log.emit("Sentinel 返回 Turnstile 但未提供 sitekey，无法调用 solver；协议将继续尝试")
		return
	}
	f.log.emitf("Sentinel 要求 Turnstile，调用 solver sitekey=%s…", truncRunes(sitekey, 18))
	token := ""
	if f.turnstileSolver != nil {
		// Python: solve_turnstile_token(..., timeout=120.0), which swallows
		// every failure and returns "".
		if solved, err := f.turnstileSolver(sitekey, pageURL, f.turnstileSolverURL, 120*time.Second); err == nil {
			token = solved
		}
	}
	if token != "" {
		f.turnstileToken = token
		// len(token) is a CHARACTER count in Python.
		f.log.emitf("Turnstile solver 返回 token（len=%d）", runeLen(token))
		return
	}
	f.log.emit("Turnstile solver 未返回 token（离线/超时），协议将继续尝试；失败请换代理或改用浏览器")
}

// orChain mirrors `a or b or c`: the first truthy operand, nil when none is.
func orChain(values ...any) any {
	for _, v := range values {
		if pyTruthy(v) {
			return v
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// _authorize_continue (app.py:8169-8183)
// ---------------------------------------------------------------------------

func (f *Flow) authorizeContinue() (string, error) {
	sentinelToken, err := f.fetchSentinelToken("authorize_continue")
	if err != nil {
		return "", err
	}
	body := pyJSONDumps(newOrderedMap(
		"username", newOrderedMap("kind", "email", "value", f.account.Email),
	), true, true)
	resp, err := f.transport.Do(&Request{
		Method: "POST",
		URL:    openai.AuthAuthorizeContinueURL,
		Header: browserHeaders(
			headerPair{"content-type", "application/json"},
			headerPair{"auth0-client", openai.Auth0ClientHeader},
			headerPair{"openai-sentinel-token", sentinelToken},
		),
		Body:            []byte(body),
		FollowRedirects: true,
	})
	if err != nil {
		return "", err
	}
	if !resp.OK() {
		return "", fmt.Errorf("AuthorizeContinue请求失败: %s", formatErrorResponse(resp))
	}
	return continueURLField(resp)
}

// continueURLField is the
// `normalize_auth_continue_url(str(response.json().get("continue_url") or ""))`
// tail shared by five steps.
func continueURLField(resp *Response) (string, error) {
	payload, err := openai.DecodeOrderedJSON(resp.Body)
	if err != nil {
		return "", err
	}
	obj, isObj := asObject(payload)
	if !isObj {
		// Python would raise AttributeError from .get() on a non-dict.
		return "", fmt.Errorf("响应不是 JSON 对象: %s", truncRunes(resp.Text(), 500))
	}
	return NormalizeAuthContinueURL(pyStrOr(obj.Get("continue_url"))), nil
}

// ---------------------------------------------------------------------------
// _send_email_otp (app.py:8185-8194)
// ---------------------------------------------------------------------------

func (f *Flow) sendEmailOTP() (string, error) {
	resp, err := f.transport.Do(&Request{
		Method: "GET",
		URL:    openai.AuthEmailOTPSendURL,
		Header: browserHeaders(
			headerPair{"accept", "application/json"},
			headerPair{"referer", openai.AuthBaseURL + "/log-in"},
		),
		FollowRedirects: true,
	})
	if err != nil {
		return "", err
	}
	if !resp.OK() {
		return "", fmt.Errorf("EmailOtpSend请求失败: %s", formatErrorResponse(resp))
	}
	f.emailOTPRequestedAt = f.unixNow()
	return continueURLField(resp)
}

// ---------------------------------------------------------------------------
// _continue_url_from_payload (app.py:8196-8210)
// ---------------------------------------------------------------------------

// continueURLFromPayload mirrors _continue_url_from_payload: prefer the
// top-level continue_url, then page.payload.url, then page.payload.continue_url,
// and finally synthesize the email-verification URL from the page TYPE.
func (f *Flow) continueURLFromPayload(payload any) string {
	obj, isObj := asObject(payload)
	if !isObj {
		return ""
	}
	direct := pyStrip(pyStrOr(obj.Get("continue_url")))
	if direct != "" {
		return NormalizeAuthContinueURL(direct)
	}
	// `payload.get("page") if isinstance(payload.get("page"), dict) else {}`
	page, _ := asObject(obj.Get("page"))
	var pagePayload orderedObject
	if page != nil {
		pagePayload, _ = asObject(page.Get("payload"))
	}
	var nestedURL, nestedContinue any
	if pagePayload != nil {
		nestedURL = pagePayload.Get("url")
		nestedContinue = pagePayload.Get("continue_url")
	}
	// `str(a or b or "").strip()` — "url" wins, then "continue_url".
	nested := pyStrip(pyStrOr(orChain(nestedURL, nestedContinue)))
	if nested != "" {
		return NormalizeAuthContinueURL(nested)
	}
	pageType := ""
	if page != nil {
		pageType = pyStrip(pyStrOr(page.Get("type")))
	}
	if pageType == "email_otp_verification" {
		return openai.AuthBaseURL + "/email-verification"
	}
	return ""
}

// ---------------------------------------------------------------------------
// _read_email_otp_code (app.py:8212-8233)
// ---------------------------------------------------------------------------

// reNonUnicodeDigit is Python's re.sub(r"\D", "", code). Python's `\d` in str
// mode is the Unicode decimal-digit class, so `\D` is everything outside
// \p{Nd}; Go's RE2 `\D` is the ASCII-only [^0-9] and would keep a fullwidth
// digit that Python strips into the code.
var reNonUnicodeDigit = regexp.MustCompile(`[^\p{Nd}]`)

func (f *Flow) readEmailOTPCode() (string, error) {
	if f.manualEmailOTP {
		if f.inputCallback == nil {
			return "", errors.New("已启用手动输入邮箱验证码，但未配置输入回调")
		}
		f.log.emit("手动邮箱验证码模式：跳过邮箱令牌/IMAP，等待人工输入")
		raw, err := f.inputCallback("email-code", f.account.Email,
			fmt.Sprintf("请输入 %s 收到的 OpenAI 邮箱验证码（一般 6 位数字）", f.account.Email))
		if err != nil {
			return "", err
		}
		code := pyStrip(raw)
		if code == "" {
			return "", errors.New("已取消邮箱验证码输入")
		}
		digits := reNonUnicodeDigit.ReplaceAllString(code, "")
		if digits != "" {
			return digits, nil
		}
		return code, nil
	}
	if f.mailReaderFactory == nil {
		return "", errors.New("authproto: 未配置邮箱读取器")
	}
	reader, err := f.mailReaderFactory(f.account, f.log)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	// `self.email_otp_requested_at or time.time() - 10` — 0.0 is falsy, so an
	// unset timestamp means "look back 10 seconds from now".
	minTimestamp := f.emailOTPRequestedAt
	if minTimestamp == 0 {
		minTimestamp = f.unixNow() - 10
	}
	return reader.WaitForCode(minTimestamp)
}

// ---------------------------------------------------------------------------
// _email_otp_validate (app.py:8235-8262)
// ---------------------------------------------------------------------------

func (f *Flow) emailOTPValidate() (string, error) {
	lastError := ""
	for attempt := 1; attempt < 3; attempt++ {
		code, err := f.readEmailOTPCode()
		if err != nil {
			return "", err
		}
		resp, err := f.transport.Do(&Request{
			Method: "POST",
			URL:    openai.AuthEmailOTPValidateURL,
			Header: browserHeaders(
				headerPair{"accept", "application/json"},
				headerPair{"content-type", "application/json"},
				headerPair{"origin", openai.AuthBaseURL},
				headerPair{"referer", openai.AuthBaseURL + "/email-verification"},
			),
			Body:            []byte(pyJSONDumps(newOrderedMap("code", code), true, true)),
			FollowRedirects: true,
		})
		if err != nil {
			return "", err
		}
		if resp.OK() {
			return continueURLField(resp)
		}
		lastError = formatErrorResponse(resp)
		// `"account_deactivated" in last_error.casefold()` (app.py:8253).
		if strings.Contains(pyCasefold(lastError), "account_deactivated") {
			return "", &models.AccountDeactivatedError{
				Msg:    fmt.Sprintf("OpenAI 在邮箱验证码校验时返回 account_deactivated: %s", lastError),
				Status: "账号已停用",
			}
		}
		if !strings.Contains(lastError, "wrong_email_otp_code") || attempt >= 2 {
			return "", fmt.Errorf("EmailOtpValidate请求失败: %s", lastError)
		}
		f.log.emit("验证码疑似过期或取错，重新发码后重试")
		if _, err := f.sendEmailOTP(); err != nil {
			return "", err
		}
		f.sleep(2 * time.Second)
	}
	// Unreachable in Python too (the loop always returns or raises), kept for
	// shape: `last_error or 'unknown'`.
	if lastError == "" {
		lastError = "unknown"
	}
	return "", fmt.Errorf("EmailOtpValidate请求失败: %s", lastError)
}

// ---------------------------------------------------------------------------
// _openai_password_for_account / _generate_protocol_password
// (app.py:8264-8281)
// ---------------------------------------------------------------------------

// protocolPasswordAlphabet is app.py:8279 — the ambiguity-free alphabet
// (no I/O/l/0/1).
const protocolPasswordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789" // gitleaks:allow，密码生成字符表不是凭据

// generateProtocolPassword mirrors _generate_protocol_password
// (app.py:8278-8281): 12 secrets.choice characters plus the "A7!" suffix that
// satisfies OpenAI's complexity rule.
func generateProtocolPassword() string {
	core := make([]byte, 12)
	alphabetLen := big.NewInt(int64(len(protocolPasswordAlphabet)))
	for i := range core {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			// secrets.choice cannot fail in Python; refuse to emit a weak
			// password rather than silently degrading the entropy.
			return ""
		}
		core[i] = protocolPasswordAlphabet[n.Int64()]
	}
	return string(core) + "A7!"
}

// openaiPasswordForAccount mirrors _openai_password_for_account
// (app.py:8264-8276). It MUTATES account.Password when none was imported, the
// same way Python assigns self.account.password.
func (f *Flow) openaiPasswordForAccount() string {
	password := f.account.Password
	if password == "" {
		f.account.Password = generateProtocolPassword()
		f.log.emitf("协议密码页缺少导入密码，已生成临时密码: %s", f.account.Password)
		return f.account.Password
	}
	// len() is a CHARACTER count in Python.
	if runeLen(password) >= 12 {
		f.log.emit("协议密码页使用导入行已有密码继续")
		return password
	}
	// encode("utf-8", errors="ignore") — a Go string is already valid UTF-8
	// bytes, so there is nothing for "ignore" to drop.
	sum := sha256.Sum256([]byte(f.account.Email + ":" + password))
	digest := hex.EncodeToString(sum[:])
	f.log.emit("协议密码页导入密码不足 12 位，已按浏览器流程规则临时补足")
	return password + "A7!" + digest[:12]
}

// ---------------------------------------------------------------------------
// _password_verify (app.py:8283-8318)
// ---------------------------------------------------------------------------

func (f *Flow) passwordVerify() (string, error) {
	password := f.openaiPasswordForAccount()
	sentinelToken, err := f.fetchSentinelToken("password_verify")
	if err != nil {
		return "", err
	}
	resp, err := f.transport.Do(&Request{
		Method: "POST",
		URL:    openai.AuthBaseURL + "/api/accounts/password/verify",
		Header: browserHeaders(
			headerPair{"accept", "application/json"},
			headerPair{"content-type", "application/json"},
			headerPair{"auth0-client", openai.Auth0ClientHeader},
			headerPair{"origin", openai.AuthBaseURL},
			headerPair{"referer", openai.AuthBaseURL + "/log-in/password"},
			headerPair{"openai-sentinel-token", sentinelToken},
			headerPair{"oai-device-id", f.deviceID},
		),
		Body:            []byte(pyJSONDumps(newOrderedMap("password", password), true, true)),
		FollowRedirects: true,
	})
	if err != nil {
		return "", err
	}
	// `payload = response.json() if response.text else {}`, wrapped in a bare
	// except that also yields {}.
	payload := decodeOrEmpty(resp)
	if !resp.OK() {
		// `str(error.get("message") if isinstance(error, dict) else error or "")`
		// — note Python's precedence: the `or ""` binds to the ELSE branch only,
		// so a dict error with no "message" stringifies to "None", not "".
		errValue := objGet(payload, "error")
		errorMsg := ""
		if errObj, isObj := asObject(errValue); isObj {
			errorMsg = pyStr(errObj.Get("message"))
		} else {
			errorMsg = pyStrOr(errValue)
		}
		// app.py:8307 — the first test is case SENSITIVE, the second is not.
		if strings.Contains(errorMsg, "Invalid credentials") || strings.Contains(pyLower(errorMsg), "wrong password") {
			return "", errors.New("协议密码验证失败: 导入密码不正确")
		}
		return "", fmt.Errorf("协议PasswordVerify请求失败: %s", formatErrorResponse(resp))
	}
	continueURL := ""
	if obj, isObj := asObject(payload); isObj {
		continueURL = NormalizeAuthContinueURL(pyStrOr(obj.Get("continue_url")))
	}
	if continueURL != "" {
		return continueURL, nil
	}
	// app.py:8314's isinstance guard is on `page`, not on `payload`.
	pageType := ""
	if page, isObj := asObject(objGet(payload, "page")); isObj {
		pageType = pyStrOr(page.Get("type"))
	}
	if pageType == "email_otp_verification" {
		f.emailOTPRequestedAt = f.unixNow() - 10
		return openai.AuthBaseURL + "/email-verification", nil
	}
	return "", fmt.Errorf("协议密码验证后未返回 continue_url: %s", truncRunes(pyStr(payload), 500))
}

// decodeOrEmpty is `response.json() if response.text else {}` inside a bare
// try/except that also produces {}.
func decodeOrEmpty(resp *Response) any {
	if resp.Text() == "" {
		return newOrderedMap()
	}
	payload, err := openai.DecodeOrderedJSON(resp.Body)
	if err != nil {
		return newOrderedMap()
	}
	return payload
}

// ---------------------------------------------------------------------------
// _username_password_create (app.py:8320-8347)
// ---------------------------------------------------------------------------

func (f *Flow) usernamePasswordCreate() (string, error) {
	password := f.openaiPasswordForAccount()
	sentinelToken, err := f.fetchSentinelToken("username_password_create")
	if err != nil {
		return "", err
	}
	resp, err := f.transport.Do(&Request{
		Method: "POST",
		URL:    openai.AuthUserRegisterURL,
		Header: browserHeaders(
			headerPair{"accept", "application/json"},
			headerPair{"content-type", "application/json"},
			headerPair{"auth0-client", openai.Auth0ClientHeader},
			headerPair{"origin", openai.AuthBaseURL},
			headerPair{"referer", openai.AuthBaseURL + "/create-account/password"},
			headerPair{"openai-sentinel-token", sentinelToken},
			headerPair{"oai-device-id", f.deviceID},
		),
		Body: []byte(pyJSONDumps(newOrderedMap(
			"username", f.account.Email,
			"password", password,
		), true, true)),
		FollowRedirects: true,
	})
	if err != nil {
		return "", err
	}
	payload := decodeOrEmpty(resp)
	if !resp.OK() {
		return "", fmt.Errorf("协议UsernamePasswordCreate请求失败: %s", formatErrorResponse(resp))
	}
	if continueURL := f.continueURLFromPayload(payload); continueURL != "" {
		return continueURL, nil
	}
	f.log.emit("注册密码已提交，继续请求邮箱验证码")
	return f.sendEmailOTP()
}

// ---------------------------------------------------------------------------
// _create_account_profile (app.py:8349-8373)
// ---------------------------------------------------------------------------

func (f *Flow) createAccountProfile() (string, error) {
	name, birthdate := models.RandomProfile()
	resp, err := f.transport.Do(&Request{
		Method: "POST",
		URL:    openai.AuthCreateAccountURL,
		Header: browserHeaders(
			headerPair{"accept", "application/json"},
			headerPair{"content-type", "application/json"},
			headerPair{"auth0-client", openai.Auth0ClientHeader},
			headerPair{"origin", openai.AuthBaseURL},
			headerPair{"referer", openai.AuthBaseURL + "/about-you"},
			headerPair{"oai-device-id", f.deviceID},
		),
		Body: []byte(pyJSONDumps(newOrderedMap(
			"name", name,
			"birthdate", birthdate,
		), true, true)),
		FollowRedirects: true,
	})
	if err != nil {
		return "", err
	}
	payload := decodeOrEmpty(resp)
	if !resp.OK() {
		return "", fmt.Errorf("协议CreateAccount请求失败: %s", formatErrorResponse(resp))
	}
	if continueURL := f.continueURLFromPayload(payload); continueURL != "" {
		return continueURL, nil
	}
	return "", fmt.Errorf("协议CreateAccount后未返回 continue_url: %s", truncRunes(pyStr(payload), 500))
}

// ---------------------------------------------------------------------------
// _resolve_workspace_id / _select_workspace (app.py:8375-8417)
// ---------------------------------------------------------------------------

// resolveWorkspaceID mirrors _resolve_workspace_id (app.py:8375-8392): decode
// the oai-client-auth-session cookie's first dot-segment and pick a workspace.
//
// Selection order is app.py:8383-8388's comment made literal — a NON-personal
// (Team/org) workspace first, because a personal one leaves the account looking
// free after conversion; then a personal one; then simply the first entry.
func (f *Flow) resolveWorkspaceID() (string, error) {
	cookie := f.readCookie(openai.AuthBaseURL, "oai-client-auth-session")
	if cookie == "" {
		return "", errors.New("未找到 oai-client-auth-session cookie，无法提取 workspace")
	}
	encoded := strings.Split(cookie, ".")[0]
	// `encoded += "=" * (-len(encoded) % 4)` — Python's % is always
	// non-negative, so this is the standard base64 re-pad.
	if pad := (4 - len(encoded)%4) % 4; pad > 0 {
		encoded += strings.Repeat("=", pad)
	}
	// base64.urlsafe_b64decode TRANSLATES "-"/"_" to "+"/"/" and then runs the
	// STANDARD decoder, so it accepts a segment spelled in either alphabet.
	// base64.URLEncoding accepts only the url-safe one and would have rejected a
	// standard-alphabet cookie outright.
	//
	// DIVERGENCE: CPython's validate=False also silently DISCARDS characters
	// outside the alphabet before checking the length; Go reports them as
	// corrupt input. Both spellings fail the step, only the message differs.
	raw, err := base64.StdEncoding.DecodeString(
		strings.NewReplacer("-", "+", "_", "/").Replace(encoded))
	if err != nil {
		return "", err
	}
	payload, err := openai.DecodeOrderedJSON(raw)
	if err != nil {
		return "", err
	}
	// `payload.get("workspaces") or []`
	workspaces, _ := objGet(payload, "workspaces").([]any)
	var workspace any
	for _, item := range workspaces {
		if obj, isObj := asObject(item); isObj && pyStr(obj.Get("kind")) != "personal" {
			workspace = item
			break
		}
	}
	if workspace == nil {
		for _, item := range workspaces {
			if obj, isObj := asObject(item); isObj && pyStr(obj.Get("kind")) == "personal" {
				workspace = item
				break
			}
		}
	}
	if workspace == nil && len(workspaces) > 0 {
		workspace = workspaces[0]
	}
	workspaceID := ""
	if obj, isObj := asObject(workspace); isObj {
		workspaceID = pyStrOr(obj.Get("id"))
	}
	if workspaceID == "" {
		return "", fmt.Errorf("当前会话未发现 workspace: %s", pyStr(payload))
	}
	return workspaceID, nil
}

func (f *Flow) selectWorkspace(consentURL string) (string, error) {
	// Python ignores the result of this GET entirely — it exists only to make
	// the consent page set its cookies. A transport error still propagates
	// (Python would raise out of session.get).
	if _, err := f.transport.Do(&Request{
		Method: "GET",
		URL:    consentURL,
		Header: browserHeaders(
			headerPair{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
			headerPair{"referer", openai.AuthBaseURL + "/email-verification"},
		),
		FollowRedirects: true,
	}); err != nil {
		return "", err
	}
	workspaceID, err := f.resolveWorkspaceID()
	if err != nil {
		return "", err
	}
	resp, err := f.transport.Do(&Request{
		Method: "POST",
		URL:    openai.AuthWorkspaceSelectURL,
		Header: browserHeaders(
			headerPair{"accept", "application/json"},
			headerPair{"content-type", "application/json"},
			headerPair{"origin", openai.AuthBaseURL},
			headerPair{"referer", consentURL},
		),
		Body:            []byte(pyJSONDumps(newOrderedMap("workspace_id", workspaceID), true, true)),
		FollowRedirects: true,
	})
	if err != nil {
		return "", err
	}
	if !resp.OK() {
		return "", fmt.Errorf("WorkspaceSelect请求失败: %s", formatErrorResponse(resp))
	}
	return continueURLField(resp)
}

// ---------------------------------------------------------------------------
// Phone verification (app.py:8419-8514)
//
// HTTP SHAPE ONLY. These three calls sit next to a paid SMS provider; nothing
// in this package rents a number or imports internal/smsbower — the
// PhoneProvider callback is the caller's own, and no test exercises a live
// provider.
// ---------------------------------------------------------------------------

// smsTransientMarkers mirrors SMSBowerClient.TRANSIENT_MARKERS
// (app.py:3720-3736). It is a DIFFERENT list from is_transient_http_error's —
// it adds "temporarily unavailable" / "max retries exceeded" and drops the
// curl:(NN) codes, "eof occurred" and "failed to perform" — so the two must not
// be merged. Duplicated here rather than imported from internal/smsbower: this
// package deliberately has no edge to the paid SMS provider.
var smsTransientMarkers = []string{
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

// isSMSTransientError mirrors SMSBowerClient.is_transient_error
// (app.py:3745-3748), which folds with str.casefold() before scanning.
func isSMSTransientError(text string) bool {
	lowered := pyCasefold(text)
	for _, marker := range smsTransientMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// sendPhoneOTP mirrors _send_phone_otp (app.py:8419-8437).
func (f *Flow) sendPhoneOTP(phoneNumber string) (string, error) {
	resp, err := f.transport.Do(&Request{
		Method: "POST",
		URL:    openai.AuthPhoneSendURL,
		Header: browserHeaders(
			headerPair{"accept", "application/json"},
			headerPair{"content-type", "application/json"},
			headerPair{"origin", openai.AuthBaseURL},
			headerPair{"referer", openai.AuthBaseURL + "/add-phone"},
		),
		Body:            []byte(pyJSONDumps(newOrderedMap("phone_number", phoneNumber), true, true)),
		FollowRedirects: true,
	})
	if err != nil {
		return "", err
	}
	if !resp.OK() {
		detail := formatErrorResponse(resp)
		status, _ := models.ClassifyPhoneRejection(detail)
		if status != "" {
			return "", &models.PhoneRejectedError{Msg: status + ": " + detail, Status: status}
		}
		return "", fmt.Errorf("SendPhoneOtp请求失败: %s", detail)
	}
	return continueURLField(resp)
}

// validatePhoneOTP mirrors _validate_phone_otp (app.py:8439-8453).
func (f *Flow) validatePhoneOTP(code string) (string, error) {
	resp, err := f.transport.Do(&Request{
		Method: "POST",
		URL:    openai.AuthPhoneOTPValidateURL,
		Header: browserHeaders(
			headerPair{"accept", "application/json"},
			headerPair{"content-type", "application/json"},
			headerPair{"origin", openai.AuthBaseURL},
			headerPair{"referer", openai.AuthBaseURL + "/phone-verification"},
		),
		Body:            []byte(pyJSONDumps(newOrderedMap("code", code), true, true)),
		FollowRedirects: true,
	})
	if err != nil {
		return "", err
	}
	if !resp.OK() {
		return "", fmt.Errorf("PhoneOtpValidate请求失败: %s", formatErrorResponse(resp))
	}
	return continueURLField(resp)
}

// handleAddPhone mirrors _handle_add_phone (app.py:8455-8514): drain the phone
// pool, then fall back to manual entry.
func (f *Flow) handleAddPhone() (string, error) {
	if f.phoneProvider != nil {
		lastError := ""
		for {
			// The "next" call is OUTSIDE Python's try: a pool failure here
			// aborts the flow rather than rolling to the next number.
			next, err := f.phoneProvider("next", f.account.Email, "")
			if err != nil {
				return "", err
			}
			if !pyTruthy(next) {
				if !f.allowManualPhone {
					detail := "手机号池为空或没有可用手机号"
					if lastError != "" {
						detail = fmt.Sprintf("手机号池没有可用手机号: %s", lastError)
					}
					f.log.emit(detail)
					return "", &models.PhoneRequiredError{Msg: detail, Status: "需要手机号"}
				}
				if lastError != "" {
					f.log.emitf("手机号池没有可用手机号，改为手动输入: %s", lastError)
				} else {
					f.log.emit("手机号池为空或没有可用手机号，改为手动输入")
				}
				break
			}
			phoneNumber := pyStrip(pyStrOr(objGet(next, "number")))
			f.log.emitf("预验证手机号是否可用于 OpenAI: %s", phoneNumber)
			continueURL, err := f.tryPhoneEntry(next, phoneNumber)
			if err == nil {
				return continueURL, nil
			}
			lastError = err.Error()
			// `SMSBowerClient.is_transient_error(exc) or "smsbower 请求失败" in
			// last_error.casefold()` (app.py:8485). The literal in the second
			// arm is lowercase while the raised text says "SMSBower", which is
			// exactly why Python folds first.
			rejectionStatus := ""
			if isSMSTransientError(lastError) || strings.Contains(pyCasefold(lastError), "smsbower 请求失败") {
				rejectionStatus = "接码网络抖动"
			} else {
				rejectionStatus = models.ExceptionStatus(err, "手机号不可用")
			}
			bad := cloneWithError(next, lastError, rejectionStatus)
			if _, perr := f.phoneProvider("bad", f.account.Email, bad); perr != nil {
				return "", perr
			}
			f.log.emitf("手机号预验证/接码失败 [%s]，切换下一个: %s %s", rejectionStatus, phoneNumber, lastError)
		}
	}

	if !f.allowManualPhone {
		return "", &models.PhoneRequiredError{
			Msg:    "OpenAI 要求手机号验证；协议注册取Session已禁用接码，已跳过（未取号、未扣费）",
			Status: "协议需手机号(未接码)",
		}
	}
	if f.inputCallback == nil {
		return "", errors.New("未配置手机号池，也未配置手动输入回调")
	}
	phoneNumber, err := f.inputCallback("phone", f.account.Email, "请输入手机号（包含国家码，例如 +1xxxxxxxxxx）")
	if err != nil {
		return "", err
	}
	if phoneNumber == "" {
		return "", errors.New("已取消手机号输入")
	}
	f.log.emitf("预验证手动手机号是否可用于 OpenAI: %s", phoneNumber)
	if _, err := f.sendPhoneOTP(phoneNumber); err != nil {
		return "", err
	}
	f.log.emit("手动手机号预验证通过，OpenAI 已发送验证码")
	code, err := f.inputCallback("phone-code", f.account.Email,
		fmt.Sprintf("请输入 %s 收到的短信验证码", phoneNumber))
	if err != nil {
		return "", err
	}
	if code == "" {
		return "", errors.New("已取消短信验证码输入")
	}
	f.log.emit("提交短信验证码")
	return f.validatePhoneOTP(code)
}

// tryPhoneEntry is the body of the `try:` at app.py:8472-8482.
func (f *Flow) tryPhoneEntry(phone any, phoneNumber string) (string, error) {
	if _, err := f.sendPhoneOTP(phoneNumber); err != nil {
		return "", err
	}
	if _, err := f.phoneProvider("sent", f.account.Email, phone); err != nil {
		return "", err
	}
	f.log.emitf("手机号预验证通过，OpenAI 已接受并发送验证码: %s", phoneNumber)
	codeValue, err := f.phoneProvider("code", f.account.Email, phone)
	if err != nil {
		return "", err
	}
	if !pyTruthy(codeValue) {
		return "", errors.New("短信链接未读取到验证码")
	}
	f.log.emitf("读取到短信验证码: %s", pyStr(codeValue))
	continueURL, err := f.validatePhoneOTP(pyStr(codeValue))
	if err != nil {
		return "", err
	}
	if _, err := f.phoneProvider("good", f.account.Email, phone); err != nil {
		return "", err
	}
	return continueURL, nil
}

// cloneWithError is `{**phone, "error": ..., "status": ...}` — a copy that
// keeps the original key order and appends (or overwrites in place) the two.
func cloneWithError(phone any, errText, status string) any {
	out := newOrderedMap()
	if obj, isObj := asObject(phone); isObj {
		for _, k := range obj.Keys() {
			out.Set(k, obj.Get(k))
		}
	}
	out.Set("error", errText)
	out.Set("status", status)
	return out
}

// ---------------------------------------------------------------------------
// _extract_auth_result (app.py:8516-8527)
// ---------------------------------------------------------------------------

// AuthResult is the dict _extract_auth_result returns (app.py:8527).
type AuthResult struct {
	CallbackURL string
	Code        string
	State       string
}

func (f *Flow) extractAuthResult(callbackURL string) (AuthResult, error) {
	// pyURLParse, not url.Parse: url.Parse REJECTS a URL whose path holds a bad
	// percent escape or a raw control character, and this returned that error
	// instead of the code — throwing away an otherwise-completed registration.
	// urlparse never fails, it just splits on ':' '//' '#' '?'.
	query := parseQS(pyURLParse(callbackURL, "").query)
	code := firstOrEmpty(query["code"])
	state := firstOrEmpty(query["state"])
	if code == "" {
		return AuthResult{}, fmt.Errorf("callback 中缺少 code: %s", callbackURL)
	}
	if state == "" {
		return AuthResult{}, fmt.Errorf("callback 中缺少 state: %s", callbackURL)
	}
	// `if self.state and state != self.state` — an unset self.state skips the
	// comparison entirely.
	if f.state != "" && state != f.state {
		return AuthResult{}, fmt.Errorf("callback state 不匹配: expected=%s actual=%s", f.state, state)
	}
	return AuthResult{CallbackURL: callbackURL, Code: code, State: state}, nil
}

// ---------------------------------------------------------------------------
// _follow_oauth_redirects (app.py:8529-8586)
// ---------------------------------------------------------------------------

// followOAuthRedirects walks the post-login redirect chain by hand
// (allow_redirects=False) so each hop can be routed to the step that answers it.
// At most 10 hops; each hop retries 4 times on a transient transport error.
func (f *Flow) followOAuthRedirects(startURL string) (AuthResult, error) {
	currentURL := startURL
	for hop := 0; hop < 10; hop++ {
		var resp *Response
		lastError := ""
		for attempt := 1; attempt < 5; attempt++ {
			r, err := f.transport.Do(&Request{
				Method: "GET",
				URL:    currentURL,
				Header: browserHeaders(
					headerPair{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
				),
				FollowRedirects: false,
			})
			if err == nil {
				resp = r
				break
			}
			lastError = err.Error()
			if attempt < 4 && isTransientErr(err) {
				f.log.emitf("OAuth 跳转网络抖动，重试(%d/3): %s", attempt, lastError)
				f.sleep(backoff(attempt))
				continue
			}
			return AuthResult{}, fmt.Errorf("OAuth跳转请求失败: %s", lastError)
		}
		if resp == nil {
			if lastError == "" {
				lastError = "unknown"
			}
			return AuthResult{}, fmt.Errorf("OAuth跳转请求失败: %s", lastError)
		}
		if location := resp.Location(); location != "" {
			nextURL := urlJoin(currentURL, location)
			handled, next, result, err := f.routeOAuthURL(nextURL)
			if err != nil {
				return AuthResult{}, err
			}
			if handled {
				if result != nil {
					return *result, nil
				}
				currentURL = next
				continue
			}
			currentURL = nextURL
			continue
		}
		handled, next, result, err := f.routeOAuthURL(resp.URL)
		if err != nil {
			return AuthResult{}, err
		}
		if handled {
			if result != nil {
				return *result, nil
			}
			currentURL = next
			continue
		}
		return AuthResult{}, fmt.Errorf("OAuth跳转未到达callback: status=%d url=%s", resp.StatusCode, resp.URL)
	}
	return AuthResult{}, fmt.Errorf("OAuth跳转次数过多，最后停在: %s", currentURL)
}

// routeOAuthURL is the shared prefix ladder of app.py:8555-8568 (Location) and
// app.py:8571-8584 (final URL). ORDER MATTERS: /log-in/password is checked
// before the plain prefixes it shares a stem with, exactly as Python lists them.
func (f *Flow) routeOAuthURL(candidate string) (handled bool, next string, result *AuthResult, err error) {
	switch {
	case strings.HasPrefix(candidate, openai.AuthBaseURL+"/log-in/password"):
		next, err = f.passwordVerify()
	case strings.HasPrefix(candidate, openai.AuthBaseURL+"/create-account/password"):
		next, err = f.usernamePasswordCreate()
	case strings.HasPrefix(candidate, openai.AuthBaseURL+"/about-you"):
		next, err = f.createAccountProfile()
	case strings.HasPrefix(candidate, openai.AuthBaseURL+"/add-phone"):
		next, err = f.handleAddPhone()
	case strings.HasPrefix(candidate, openai.DefaultRedirectURI):
		var r AuthResult
		r, err = f.extractAuthResult(candidate)
		if err != nil {
			return true, "", nil, err
		}
		return true, "", &r, nil
	default:
		return false, "", nil, nil
	}
	if err != nil {
		return true, "", nil, err
	}
	return true, next, nil, nil
}

// backoff is `time.sleep(min(6, attempt * 1.2))`.
func backoff(attempt int) time.Duration {
	seconds := float64(attempt) * 1.2
	if seconds > 6 {
		seconds = 6
	}
	return time.Duration(seconds * float64(time.Second))
}

// ---------------------------------------------------------------------------
// _exchange_code_for_token (app.py:8592-8637)
// ---------------------------------------------------------------------------

// exchangeCodeForToken tries each token endpoint in order, 4 attempts each,
// retrying only on 5xx / empty body / a transient marker — never on a business
// error.
func (f *Flow) exchangeCodeForToken(code string) (openai.AuthRecord, error) {
	lastError := ""
	for _, tokenURL := range openai.AuthOAuthTokenURLs {
		for attempt := 1; attempt < 5; attempt++ {
			form := queryPairs{
				{"grant_type", "authorization_code"},
				{"client_id", openai.DefaultClientID},
				{"code", code},
				{"redirect_uri", openai.DefaultRedirectURI},
				{"code_verifier", f.codeVerifier},
			}
			resp, err := f.transport.Do(&Request{
				Method: "POST",
				URL:    tokenURL,
				Header: browserHeaders(
					headerPair{"accept", "application/json"},
					headerPair{"content-type", "application/x-www-form-urlencoded"},
					headerPair{"auth0-client", openai.Auth0ClientHeader},
					headerPair{"sec-fetch-dest", "empty"},
					headerPair{"sec-fetch-mode", "cors"},
					headerPair{"sec-fetch-site", "same-site"},
				),
				Body:            []byte(form.Encode()),
				FollowRedirects: true,
			})
			if err != nil {
				lastError = fmt.Sprintf("endpoint=%s %s", tokenURL, err.Error())
				if attempt < 4 && isTransientErr(err) {
					f.log.emitf("换 Token 网络抖动，重试(%d/3) %s: %s", attempt, tokenURL, err.Error())
					f.sleep(backoff(attempt))
					continue
				}
				break
			}
			if resp.OK() {
				payload, derr := openai.DecodeOrderedJSON(resp.Body)
				if derr != nil {
					return openai.AuthRecord{}, derr
				}
				return normalizeAuthRecord(f.account.Email, payload)
			}
			body := formatErrorResponse(resp)
			lastError = fmt.Sprintf("endpoint=%s %s", tokenURL, body)
			// 业务错误不重试；5xx / 空响应类可重试
			if attempt < 4 && (resp.StatusCode >= 500 || pyStrip(resp.Text()) == "" || IsTransientHTTPError(body)) {
				f.log.emitf("换 Token HTTP %d，重试(%d/3) %s", resp.StatusCode, attempt, tokenURL)
				f.sleep(backoff(attempt))
				continue
			}
			break
		}
	}
	return openai.AuthRecord{}, fmt.Errorf("Code换Token失败: %s", lastError)
}

// normalizeAuthRecord adapts the ordered payload to
// openai.NormalizeOpenAIAuthRecord, which already ports
// normalize_openai_auth_record (app.py:4943-4974).
func normalizeAuthRecord(email string, payload any) (openai.AuthRecord, error) {
	flat := map[string]any{}
	if obj, isObj := asObject(payload); isObj {
		for _, k := range obj.Keys() {
			flat[k] = obj.Get(k)
		}
	}
	return openai.NormalizeOpenAIAuthRecord(email, flat)
}

// ---------------------------------------------------------------------------
// _open_oauth_url (app.py:8639-8655)
// ---------------------------------------------------------------------------

func (f *Flow) openOAuthURL(oauthURL string) (*Response, error) {
	return f.transport.Do(&Request{
		Method: "GET",
		URL:    oauthURL,
		Header: browserHeaders(
			headerPair{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
			headerPair{"accept-encoding", "gzip, deflate"},
			headerPair{"priority", "u=0, i"},
			headerPair{"referer", "https://platform.openai.com/"},
			headerPair{"sec-fetch-dest", "document"},
			headerPair{"sec-fetch-mode", "navigate"},
			headerPair{"sec-fetch-site", "cross-site"},
			headerPair{"sec-fetch-user", "?1"},
			headerPair{"upgrade-insecure-requests", "1"},
		),
		FollowRedirects: true,
	})
}

// ---------------------------------------------------------------------------
// run (app.py:8669-8743)
// ---------------------------------------------------------------------------

// turnstileChallengeMsg is the identical string raised at app.py:8678 and 8695.
const turnstileChallengeMsg = "当前 OpenAI 登录触发 Turnstile/Cloudflare challenge，协议模式无法自动完成；请切换更干净代理，或使用浏览器注册/登录取Session"

// allowedStartURLs is the set at app.py:8684-8692. It is a Python set, but the
// only use is `any(url.startswith(x) for x in ...)`, so order is irrelevant.
func allowedStartURLs() []string {
	return []string{
		openai.AuthBaseURL + "/log-in",
		openai.AuthBaseURL + "/log-in/password",
		openai.AuthBaseURL + "/create-account/password",
		openai.AuthBaseURL + "/email-verification",
		openai.AuthBaseURL + "/about-you",
		openai.AuthBaseURL + "/sign-in-with-chatgpt/codex/consent",
		openai.AuthBaseURL + "/add-phone",
	}
}

// Run mirrors OpenAIJsonAuthFlow.run (app.py:8669-8743): the whole protocol
// login/registration, returning the normalized OAuth record.
func (f *Flow) Run() (openai.AuthRecord, error) {
	f.log.emitf("开始 OpenAI 邮箱验证码授权: %s", f.account.Email)
	oauthURL := f.PrepareLoginURL()
	resp, err := f.openOAuthURL(oauthURL)
	if err != nil {
		return openai.AuthRecord{}, err
	}
	if !resp.OK() && (resp.StatusCode == 400 || resp.StatusCode == 403) {
		f.log.emitf("Accounts OAuth 入口返回 HTTP %d，回退旧 OAuth 入口重试", resp.StatusCode)
		resp, err = f.openOAuthURL(f.prepareLegacyLoginURL())
		if err != nil {
			return openai.AuthRecord{}, err
		}
	}
	if !resp.OK() {
		if responseHasAuthChallenge(resp) {
			return openai.AuthRecord{}, errors.New(turnstileChallengeMsg)
		}
		return openai.AuthRecord{}, fmt.Errorf("OauthUrl请求失败: %d", resp.StatusCode)
	}
	if strings.HasPrefix(resp.URL, openai.DefaultRedirectURI) {
		result, err := f.extractAuthResult(resp.URL)
		if err != nil {
			return openai.AuthRecord{}, err
		}
		return f.exchangeCodeForToken(result.Code)
	}

	allowed := false
	for _, prefix := range allowedStartURLs() {
		if strings.HasPrefix(resp.URL, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		if responseHasAuthChallenge(resp) {
			return openai.AuthRecord{}, errors.New(turnstileChallengeMsg)
		}
		return openai.AuthRecord{}, fmt.Errorf("OauthUrl重定向到错误的URL: %s", resp.URL)
	}

	// The server may have re-issued the device id; adopt it if so.
	if cookieDeviceID := f.readCookie("https://openai.com", "oai-did"); cookieDeviceID != "" {
		f.deviceID = cookieDeviceID
		f.setDeviceCookie(f.deviceID)
	}

	continueURL := resp.URL
	if strings.HasPrefix(continueURL, openai.AuthBaseURL+"/email-verification") {
		f.emailOTPRequestedAt = f.unixNow() - 10
	}
	// `startswith("/log-in") and not startswith("/log-in/password")` — the
	// password page is a longer prefix of the same stem and must not be treated
	// as the email step.
	if strings.HasPrefix(continueURL, openai.AuthBaseURL+"/log-in") &&
		!strings.HasPrefix(continueURL, openai.AuthBaseURL+"/log-in/password") {
		f.log.emit("提交登录邮箱")
		if continueURL, err = f.authorizeContinue(); err != nil {
			return openai.AuthRecord{}, err
		}
	}
	if strings.HasPrefix(continueURL, openai.AuthBaseURL+"/log-in/password") {
		f.log.emit("进入密码登录页，尝试协议提交密码")
		if continueURL, err = f.passwordVerify(); err != nil {
			return openai.AuthRecord{}, err
		}
	}
	if strings.HasPrefix(continueURL, openai.AuthBaseURL+"/create-account/password") {
		f.log.emit("进入注册密码页，尝试协议创建密码")
		if continueURL, err = f.usernamePasswordCreate(); err != nil {
			return openai.AuthRecord{}, err
		}
	}
	// An EQUALITY test in Python, not a prefix test.
	if continueURL == openai.AuthEmailOTPSendURL {
		f.log.emit("发送邮箱验证码")
		if continueURL, err = f.sendEmailOTP(); err != nil {
			return openai.AuthRecord{}, err
		}
	}
	if strings.HasPrefix(continueURL, openai.AuthBaseURL+"/email-verification") {
		f.log.emit("等待并提交邮箱验证码")
		if continueURL, err = f.emailOTPValidate(); err != nil {
			return openai.AuthRecord{}, err
		}
	}
	if strings.HasPrefix(continueURL, openai.AuthBaseURL+"/about-you") {
		f.log.emit("进入基础资料页，尝试协议创建账号资料")
		if continueURL, err = f.createAccountProfile(); err != nil {
			return openai.AuthRecord{}, err
		}
	}
	if strings.HasPrefix(continueURL, openai.AuthBaseURL+"/add-phone") {
		f.log.emit("遇到 add-phone，等待手动输入手机号和短信验证码")
		if continueURL, err = f.handleAddPhone(); err != nil {
			return openai.AuthRecord{}, err
		}
	}
	if continueURL == openai.AuthBaseURL+"/sign-in-with-chatgpt/codex/consent" {
		f.log.emit("选择默认工作区")
		if continueURL, err = f.selectWorkspace(continueURL); err != nil {
			return openai.AuthRecord{}, err
		}
	}

	// app.py:8731-8739 repeats three of the branches once more, because a
	// workspace/profile step can hand back a page that an earlier branch owns.
	// The repetition is deliberate and reproduced verbatim.
	if strings.HasPrefix(continueURL, openai.AuthBaseURL+"/add-phone") {
		f.log.emit("遇到 add-phone，等待手动输入手机号和短信验证码")
		if continueURL, err = f.handleAddPhone(); err != nil {
			return openai.AuthRecord{}, err
		}
	}
	if strings.HasPrefix(continueURL, openai.AuthBaseURL+"/about-you") {
		f.log.emit("进入基础资料页，尝试协议创建账号资料")
		if continueURL, err = f.createAccountProfile(); err != nil {
			return openai.AuthRecord{}, err
		}
	}
	if continueURL == openai.AuthBaseURL+"/sign-in-with-chatgpt/codex/consent" {
		f.log.emit("选择默认工作区")
		if continueURL, err = f.selectWorkspace(continueURL); err != nil {
			return openai.AuthRecord{}, err
		}
	}

	f.log.emit("交换授权 code 获取 refresh_token")
	result, err := f.followOAuthRedirects(continueURL)
	if err != nil {
		return openai.AuthRecord{}, err
	}
	return f.exchangeCodeForToken(result.Code)
}

// unixNow is time.time().
func (f *Flow) unixNow() float64 {
	return float64(f.now().UnixNano()) / float64(time.Second)
}
