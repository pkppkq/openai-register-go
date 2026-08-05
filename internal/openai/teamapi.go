package openai

// teamapi.go ports the three pure-HTTP ChatGPT Team / K12 workspace calls of
// app.py:
//
//	G13 k12_request_workspace_invite   (app.py:3168-3176)
//	G11 chatgpt_team_send_invite       (app.py:3179-3209)
//	G12 chatgpt_team_leave_workspace   (app.py:3329-3448)
//
// plus the private HTTP session they all share, opll_build_chatgpt_session
// (app.py:2808-2844). None of them drive a browser: they are Bearer-token calls
// against chatgpt.com/backend-api through the Chrome-impersonating tls-client,
// used the same way internal/worker/coreauth.go uses it.

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	http "github.com/bogdanfinn/fhttp"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

// teamRequestTimeoutSeconds mirrors the per-call `timeout=30` every Team/K12
// request passes (app.py:3175, 3207, 3346, 3394, 3440). tls-client has no
// per-request timeout, so — exactly like internal/worker/coreauth.go — it is
// applied client-wide at construction time instead.
const teamRequestTimeoutSeconds = 30

// ---------------------------------------------------------------------------
// G13 — 请求邀请 / k12_request_workspace_invite (app.py:3168-3176)
// ---------------------------------------------------------------------------

// K12RequestWorkspaceInvite asks a K12 workspace to invite the caller: an empty
// POST to /backend-api/accounts/{workspace_id}/invites/request signed with the
// account's own Access Token (app.py:3168-3176).
//
// It returns the raw (status, body) pair Python returned; classification is the
// caller's job and matches app.py:22046 — 2xx is "K12请求成功", anything else is
// "K12失败". A transport failure (Python: the requests exception escaping the
// function) is reported as a non-nil error with status 0.
func K12RequestWorkspaceInvite(accessToken, workspaceID, proxyURL string) (int, string, error) {
	// app.py's 4th parameter `mode` defaults to "request" and no caller
	// (app.py:22038, 22138) ever overrides it.
	return teamK12RequestWorkspaceInvite(accessToken, workspaceID, proxyURL, "request")
}

// teamK12RequestWorkspaceInvite is K12RequestWorkspaceInvite with app.py's
// `mode` parameter still reachable (app.py:3168).
func teamK12RequestWorkspaceInvite(accessToken, workspaceID, proxyURL, mode string) (int, string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	// Python: str(mode or "request").strip() or "request" — a whitespace-only
	// mode falls back to "request" too, which strings.TrimSpace alone would not
	// do (the second `or` is what rescues it).
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "request"
	}
	if workspaceID == "" {
		return 0, "", errors.New("请先填写 K12 Workspace ID")
	}
	// The workspace-id check happens BEFORE the session is built, so an empty
	// workspace id wins over a missing Access Token (app.py:3171-3173).
	session, err := teamNewChatGPTSession(accessToken, proxyURL)
	if err != nil {
		return 0, "", err
	}
	// Python posts data="" — requests/curl_cffi turn that into no body at all
	// plus Content-Length: 0, which is what Go emits for a POST with a nil body.
	return session.do("POST", teamK12InviteURL(workspaceID, mode), nil, nil)
}

// teamK12InviteURL builds the G13 endpoint (app.py:3174).
func teamK12InviteURL(workspaceID, mode string) string {
	return ChatGPTBaseURL + "/backend-api/accounts/" + teamQuote(workspaceID) + "/invites/" + teamQuote(mode)
}

// ---------------------------------------------------------------------------
// G11 — 邀请成员 / chatgpt_team_send_invite (app.py:3179-3209)
// ---------------------------------------------------------------------------

// ChatGPTTeamSendInvite invites targetEmail into the Team workspace accountID,
// using the inviter's workspace-scoped Access Token (app.py:3179-3209).
//
// SPENDS MONEY. A successful call adds a BILLABLE Team seat to the inviter's
// workspace and OpenAI charges the workspace owner for it; the invite is also
// emailed to targetEmail immediately. There is no dry-run mode and app.py has
// no un-invite path (see UI_SPEC row 60: "Invite only — no auto-accept, no
// removal"). Never call this from a test, and never call it speculatively.
//
// Returns the raw (status, body); the caller classifies exactly as
// app.py:21388-21397 does — 2xx is "Team邀请已发送", everything else is
// "Team邀请失败" (see TeamInviteFailureHint for the two localized hints).
func ChatGPTTeamSendInvite(accessToken, accountID, targetEmail, proxyURL string) (int, string, error) {
	// app.py's 5th parameter `seat_type` defaults to "" and no caller
	// (app.py:21375) ever passes it.
	return teamSendInvite(accessToken, accountID, targetEmail, "", proxyURL)
}

// teamSendInvite is ChatGPTTeamSendInvite with app.py's `seat_type` parameter
// still reachable (app.py:3184, 3201-3202).
func teamSendInvite(accessToken, accountID, targetEmail, seatType, proxyURL string) (int, string, error) {
	token := teamResolveAccessToken(accessToken)
	accountID = strings.TrimSpace(accountID)
	targetEmail = models.NormalizeEmailAddress(targetEmail)
	if token == "" {
		return 0, "", errors.New("缺少 Team 邀请者 Access Token")
	}
	if accountID == "" {
		return 0, "", errors.New("缺少 Team account_id")
	}
	if !strings.Contains(targetEmail, "@") {
		return 0, "", fmt.Errorf("目标邮箱格式错误: %s", targetEmail)
	}
	body, err := teamInviteBody(targetEmail, seatType)
	if err != nil {
		return 0, "", err
	}
	session, err := teamNewChatGPTSession(token, proxyURL)
	if err != nil {
		return 0, "", err
	}
	return session.do(
		"POST",
		teamInvitesURL(accountID),
		body,
		// The workspace header is what makes the backend accept a
		// workspace-scoped token (app.py:3205).
		map[string]string{"chatgpt-account-id": accountID},
	)
}

// teamInvitesURL builds the G11 endpoint (app.py:3204).
func teamInvitesURL(accountID string) string {
	return ChatGPTBaseURL + "/backend-api/accounts/" + teamQuote(accountID) + "/invites"
}

// teamInvitePayload mirrors the invite dict of app.py:3196-3202. Struct field
// order is the dict's insertion order, which is the order json.dumps emits.
type teamInvitePayload struct {
	EmailAddresses []string `json:"email_addresses"`
	Role           string   `json:"role"`
	ResendEmails   bool     `json:"resend_emails"`
	// seat_type is only present when app.py's `if str(seat_type or "").strip()`
	// passes, hence omitempty (app.py:3201-3202).
	SeatType string `json:"seat_type,omitempty"`
}

// teamInviteBody serializes the invite payload.
func teamInviteBody(targetEmail, seatType string) ([]byte, error) {
	return teamJSONDumps(teamInvitePayload{
		EmailAddresses: []string{targetEmail},
		Role:           "standard-user",
		ResendEmails:   true,
		SeatType:       strings.TrimSpace(seatType),
	})
}

// TeamInviteFailureHint reproduces the two extra log lines app.py emits on a
// failed invite (app.py:21393-21396). Returns "" when no hint applies.
func TeamInviteFailureHint(status int, body string) string {
	if status == 401 && strings.Contains(pyCaseFold(body), "workspace account") {
		return "服务端仍未接受工作空间身份；请刷新 Session 后重试，并确认该账号在 Team 中仍有效"
	}
	if status == 401 || status == 403 {
		return "当前 Team 成员可能没有邀请权限，或该工作空间已限制成员邀请"
	}
	return ""
}

// ---------------------------------------------------------------------------
// G12 — 退出 Team / chatgpt_team_leave_workspace (app.py:3329-3448)
// ---------------------------------------------------------------------------

// TeamLeaveDetail is the third return value of chatgpt_team_leave_workspace
// (app.py:3442-3448). Field and JSON-tag order mirror the Python dict.
type TeamLeaveDetail struct {
	Role          string `json:"role"`
	MemberID      string `json:"member_id"`
	AccountUserID string `json:"account_user_id"`
	TokenUserID   string `json:"token_user_id"`
	UsersStatus   int    `json:"users_status"`
}

// ChatGPTTeamLeaveWorkspace makes a Team MEMBER leave workspace accountID using
// the member's own Access Token (app.py:3329-3448). Three round trips:
//
//  1. GET  /backend-api/accounts/check/v4-2023-04-27      — find this account's
//     membership record and refuse to continue for an Owner.
//  2. GET  /backend-api/accounts/{account_id}/users       — resolve the member id
//     (best effort; failures fall back to the ids in the check payload / JWT).
//  3. DELETE /backend-api/accounts/{account_id}/users/{member_id}
//
// Leaving releases the seat and invalidates the workspace session, so the
// caller must refresh the session afterwards (app.py:21474).
//
// SIGNATURE NOTE: docs/UI_SPEC.md G12 abbreviates this as
// ChatGPTTeamLeaveWorkspace(token, proxy), but app.py:3329 takes account_id and
// member_email as well and its only caller (app.py:21455-21460) passes all
// four; neither can be derived inside the function, so the faithful 4-argument
// form is used.
func ChatGPTTeamLeaveWorkspace(accessToken, accountID, memberEmail, proxyURL string) (int, string, TeamLeaveDetail, error) {
	var detail TeamLeaveDetail

	token := teamResolveAccessToken(accessToken)
	accountID = strings.TrimSpace(accountID)
	memberEmail = models.NormalizeEmailAddress(memberEmail)
	if token == "" {
		return 0, "", detail, errors.New("缺少 Team 成员 Access Token")
	}
	if accountID == "" {
		return 0, "", detail, errors.New("缺少 Team account_id")
	}

	session, err := teamNewChatGPTSession(token, proxyURL)
	if err != nil {
		return 0, "", detail, err
	}

	// --- 1. role check -----------------------------------------------------
	checkStatus, checkBody, err := session.do("GET", teamAccountsCheckURL(), nil, nil)
	if err != nil {
		return 0, "", detail, err
	}
	if !teamHTTPOK(checkStatus) {
		return 0, "", detail, fmt.Errorf("无法确认当前 Team 角色: HTTP %d %s", checkStatus, teamTruncateRunes(checkBody, 500))
	}
	// Python is json.loads(resp.text or "{}"): an empty 2xx body decodes to an empty
	// dict and falls through to the "无法确认角色" branch below, it does not raise.
	checkRaw := checkBody
	if checkRaw == "" {
		checkRaw = "{}"
	}
	checkPayload, err := teamDecodeOrderedJSON([]byte(checkRaw))
	if err != nil {
		return 0, "", detail, errors.New("无法解析当前 Team 角色信息")
	}

	membership := teamMembershipFromCheckPayload(checkPayload, accountID)
	role := teamMembershipRole(membership)
	accountUserID := teamPyStrOrChain(membership.Get("account_user_id"))
	if role == "" {
		return 0, "", detail, errors.New("无法确认当前账号在该 Team 中的角色，已取消退出")
	}
	if teamOwnerRoles[role] {
		return 0, "", detail, errors.New("当前账号是 Team Owner，不能直接退出；请先转移所有权或由其他管理员处理")
	}

	// --- 2. member-id resolution -------------------------------------------
	// Only user_id is needed out of summarize_chatgpt_access_token
	// (app.py:5480-5492, called at app.py:3389).
	tokenUserID := teamAccessTokenUserID(token)

	usersStatus, usersBody, err := session.do(
		"GET",
		teamUsersURL(accountID),
		nil,
		map[string]string{"chatgpt-account-id": accountID},
	)
	if err != nil {
		// Python lets a transport error here escape the function, unlike the
		// JSON/status failures below which degrade to an empty payload
		// (app.py:3391-3404).
		return 0, "", detail, err
	}
	var usersPayload any
	if teamHTTPOK(usersStatus) {
		// A non-JSON body is swallowed: users_payload stays {} (app.py:3400-3404).
		if decoded, decErr := teamDecodeOrderedJSON([]byte(usersBody)); decErr == nil {
			usersPayload = decoded
		}
	}

	memberID := teamResolveMemberID(teamMembersFromUsersPayload(usersPayload), memberEmail, accountUserID, tokenUserID)
	if memberID == "" {
		memberID = FirstNonEmpty(accountUserID, tokenUserID)
	}
	if memberID == "" {
		return 0, "", detail, fmt.Errorf("无法定位当前账号的 Team 成员 ID: HTTP %d", usersStatus)
	}

	// --- 3. leave ----------------------------------------------------------
	detail = TeamLeaveDetail{
		Role:          role,
		MemberID:      memberID,
		AccountUserID: accountUserID,
		TokenUserID:   tokenUserID,
		UsersStatus:   usersStatus,
	}
	deleteStatus, deleteBody, err := session.do(
		"DELETE",
		teamUserURL(accountID, memberID),
		nil,
		map[string]string{"chatgpt-account-id": accountID},
	)
	if err != nil {
		return 0, "", detail, err
	}
	return deleteStatus, deleteBody, detail, nil
}

// teamOwnerRoles is the owner guard set of app.py:3386. Keys are already
// case-folded because teamMembershipRole folds before the lookup.
var teamOwnerRoles = map[string]bool{"owner": true, "account-owner": true, "account_owner": true}

// teamAccountsCheckURL is the backend account/membership probe (app.py:3345).
func teamAccountsCheckURL() string {
	return ChatGPTBaseURL + "/backend-api/accounts/check/v4-2023-04-27"
}

// teamUsersURL lists the workspace members (app.py:3392).
func teamUsersURL(accountID string) string {
	return ChatGPTBaseURL + "/backend-api/accounts/" + teamQuote(accountID) + "/users"
}

// teamUserURL is the seat that DELETE removes (app.py:3438).
func teamUserURL(accountID, memberID string) string {
	return ChatGPTBaseURL + "/backend-api/accounts/" + teamQuote(accountID) + "/users/" + teamQuote(memberID)
}

// TeamLeaveFailureHint reproduces the extra clause app.py appends to the failure
// log (app.py:21478-21479). Returns "" when no hint applies.
func TeamLeaveFailureHint(status int) string {
	if status == 403 {
		return "；服务端拒绝该成员自行退出，可能需要 Team Owner/管理员移除"
	}
	return ""
}

// teamMembershipFromCheckPayload is `collect_memberships` + `memberships[0]`
// (app.py:3358-3381): a depth-first walk of the /accounts/check payload
// collecting every object that IS this account and carries a role/user id.
// Returns an empty object when nothing matches (Python's `else {}`).
//
// ORDER IS LOAD-BEARING — only memberships[0] is used, and Python walks a dict's
// values in insertion order while Go's map iteration is randomized. That is why
// the payload is decoded through teamDecodeOrderedJSON into *teamObject instead
// of map[string]any.
//
// The walk deliberately keeps Python's redundancy: a nested "account" object is
// appended by the explicit nested check AND again when the recursion reaches it
// as a direct match, so it can occupy index 0 ahead of any sibling.
func teamMembershipFromCheckPayload(payload any, accountID string) *teamObject {
	var found []*teamObject
	teamCollectMemberships(payload, accountID, &found)
	if len(found) == 0 {
		return newTeamObject()
	}
	return found[0]
}

func teamCollectMemberships(value any, accountID string, out *[]*teamObject) {
	switch v := value.(type) {
	case *teamObject:
		if teamIsMembershipFor(v, accountID) {
			*out = append(*out, v)
		}
		if nested, ok := v.Get("account").(*teamObject); ok && teamIsMembershipFor(nested, accountID) {
			*out = append(*out, nested)
		}
		for _, key := range v.Keys() {
			teamCollectMemberships(v.Get(key), accountID, out)
		}
	case []any:
		for _, item := range v {
			teamCollectMemberships(item, accountID, out)
		}
	}
}

// teamIsMembershipFor is the predicate of app.py:3362-3365. accountID is
// guaranteed non-empty by the caller, so Python's `str(direct_id or "")`
// degenerating to "" can never produce a false match.
func teamIsMembershipFor(obj *teamObject, accountID string) bool {
	if FirstNonEmpty(obj.Get("account_id"), obj.Get("id")) != accountID {
		return false
	}
	return teamPyTruthy(obj.Get("account_user_role")) || teamPyTruthy(obj.Get("account_user_id"))
}

// teamMembershipRole is app.py:3382:
// str(m["account_user_role"] or m["role"] or "").strip().casefold().
func teamMembershipRole(membership *teamObject) string {
	return pyCaseFold(teamPyStrOrChain(membership.Get("account_user_role"), membership.Get("role")))
}

// teamMembersFromUsersPayload is app.py:3405-3410: the payload itself when it is
// a list, otherwise the first of items/users/members that is a list.
func teamMembersFromUsersPayload(payload any) []any {
	if list, ok := payload.([]any); ok {
		return list
	}
	obj, ok := payload.(*teamObject)
	if !ok {
		return nil
	}
	for _, key := range []string{"items", "users", "members"} {
		if list, ok := obj.Get(key).([]any); ok {
			return list
		}
	}
	return nil
}

// teamResolveMemberID is the member scan of app.py:3412-3431: the first member
// whose email matches memberEmail, or whose ids intersect the ids we already
// know for ourselves. Returns "" when nothing matches.
func teamResolveMemberID(members []any, memberEmail, accountUserID, tokenUserID string) string {
	// {account_user_id, token_user_id} - {""} (app.py:3428).
	want := make(map[string]bool, 2)
	for _, id := range []string{accountUserID, tokenUserID} {
		if id != "" {
			want[id] = true
		}
	}
	for _, item := range members {
		member, ok := item.(*teamObject)
		if !ok {
			continue
		}
		// Python chains raw values with `or` and only then str().strip(), which
		// differs from first_non_empty: a whitespace-only "user_id" is TRUTHY, so
		// it wins the chain and then strips to "" (the member is skipped) instead
		// of falling through to "id".
		candidateID := teamPyStrOrChain(member.Get("user_id"), member.Get("id"))
		nestedUser := teamNestedObject(member, "user")
		candidateEmail := models.NormalizeEmailAddress(FirstNonEmpty(
			member.Get("email"),
			member.Get("email_address"),
			nestedUser.Get("email"),
		))
		emailMatches := memberEmail != "" && candidateEmail != "" &&
			strings.ToLower(candidateEmail) == strings.ToLower(memberEmail)
		idMatches := false
		for _, id := range []string{
			teamPyStrOrChain(member.Get("user_id")),
			teamPyStrOrChain(member.Get("id")),
			teamPyStrOrChain(nestedUser.Get("id")),
		} {
			if id != "" && want[id] {
				idMatches = true
				break
			}
		}
		if candidateID != "" && (emailMatches || idMatches) {
			return candidateID
		}
	}
	return ""
}

// teamAccessTokenUserID is the user_id half of summarize_chatgpt_access_token
// (app.py:5484): the chatgpt_user_id / user_id claim of the access token.
func teamAccessTokenUserID(accessToken string) string {
	auth := GetNestedRecord(DecodeJWTPayload(accessToken), "https://api.openai.com/auth")
	return FirstNonEmpty(auth["chatgpt_user_id"], auth["user_id"])
}

// ---------------------------------------------------------------------------
// shared HTTP session — opll_build_chatgpt_session (app.py:2808-2844)
// ---------------------------------------------------------------------------

// teamSession is the Python requests/curl_cffi Session that all three calls
// share: one Bearer token, one oai-device-id reused across every request of a
// single operation, and one proxy.
type teamSession struct {
	client   *tlsclient.Client
	token    string
	deviceID string
}

// teamNewChatGPTSession ports opll_build_chatgpt_session (app.py:2818-2844).
// The device id is minted once per session and appears both as a header and as
// the only cookie, exactly as Python does.
func teamNewChatGPTSession(accessToken, proxyURL string) (*teamSession, error) {
	token := teamResolveAccessToken(accessToken)
	if token == "" {
		return nil, errors.New("当前账号没有 Access Token，请先注册并获取 Session 信息")
	}
	// requests attaches session.proxies lazily, so Python only discovers a
	// malformed proxy URL when the first request runs; tls-client validates it
	// here. The failure still surfaces on the same call, just with a different
	// message.
	client, err := tlsclient.New(proxyURL, teamRequestTimeoutSeconds)
	if err != nil {
		return nil, err
	}
	return &teamSession{client: client, token: token, deviceID: teamRandomUUID4()}, nil
}

// teamSessionHeaderOrder is the insertion order of the Python session header
// dict (app.py:2824-2841). Exact wire order is not reproducible anyway —
// curl_cffi's chrome136 impersonation rewrites it, and requests would have
// prepended its own defaults — but these are authenticated backend-api calls
// where only the TLS fingerprint is policed, so the dict order is used as-is.
var teamSessionHeaderOrder = []string{
	"user-agent", "accept", "accept-language", "authorization", "origin",
	"referer", "content-type", "oai-device-id", "oai-language", "sec-ch-ua",
	"sec-ch-ua-mobile", "sec-ch-ua-platform", "sec-fetch-dest", "sec-fetch-mode",
	"sec-fetch-site", "cookie",
}

// header builds the session header set. Names are lowercased (Python used mixed
// case such as "User-Agent"/"Authorization"); HTTP header names are
// case-insensitive and HTTP/2 mandates lowercase, so this is a no-op on the
// wire and keeps the ordering key comparable.
func (s *teamSession) header(extra map[string]string) http.Header {
	h := http.Header{
		"user-agent":         {DefaultUserAgent},
		"accept":             {"*/*"},
		"accept-language":    {"en-US,en;q=0.9"},
		"authorization":      {"Bearer " + s.token},
		"origin":             {ChatGPTBaseURL},
		"referer":            {ChatGPTBaseURL + "/"},
		"content-type":       {"application/json"},
		"oai-device-id":      {s.deviceID},
		"oai-language":       {"en-US"},
		"sec-ch-ua":          {`"Google Chrome";v="147", "Not.A/Brand";v="8", "Chromium";v="147"`},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {`"Windows"`},
		"sec-fetch-dest":     {"empty"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-site":     {"same-origin"},
		"cookie":             {"oai-did=" + s.deviceID},
	}
	order := make([]string, len(teamSessionHeaderOrder), len(teamSessionHeaderOrder)+len(extra))
	copy(order, teamSessionHeaderOrder)
	// Python passes per-call headers to session.get/post, which merge over the
	// session defaults (app.py:3205, 3393, 3439).
	//
	// DIVERGENCE: Python appends the new keys in the caller's literal dict order,
	// which a Go map cannot carry. Sorting keeps HeaderOrderKey — and therefore the
	// wire fingerprint — stable across runs instead of reshuffling per request; every
	// current caller passes at most one extra header, so the two agree today.
	extraKeys := make([]string, 0, len(extra))
	for k := range extra {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		v := extra[k]
		key := strings.ToLower(k)
		if _, exists := h[key]; !exists {
			order = append(order, key)
		}
		h[key] = []string{v}
	}
	h[http.HeaderOrderKey] = order
	return h
}

// do issues one request and returns (status, body). Python's response.text is a
// decoded str; every endpoint here answers UTF-8 JSON, so the bytes are taken
// verbatim.
func (s *teamSession) do(method, rawURL string, body []byte, extra map[string]string) (int, string, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	status, raw, err := s.client.Do(method, rawURL, reader, s.header(extra))
	if err != nil {
		return 0, "", err
	}
	return status, string(raw), nil
}

func teamHTTPOK(status int) bool { return status >= 200 && status < 300 }

// ---------------------------------------------------------------------------
// helpers (all team-prefixed: internal/openai is shared with other port agents)
// ---------------------------------------------------------------------------

// teamResolveAccessToken is the
// `extract_access_token_from_session_text(x) or str(x or "").strip()` idiom used
// by every Team entry point (app.py:3186, 3335) and by
// opll_build_chatgpt_session itself (app.py:2819).
func teamResolveAccessToken(value string) string {
	if token := teamExtractAccessTokenFromSessionText(value); token != "" {
		return token
	}
	return pyStrip(value)
}

// teamAccessTokenJSONRe mirrors app.py:2633. Go's RE2 `\s` is ASCII-only while
// Python's str-mode `\s` is Unicode-aware, and pasted session blobs carry NBSP —
// hence the explicit separator classes used elsewhere in this port.
var teamAccessTokenJSONRe = regexp.MustCompile(`"(?:accessToken|access_token|token)"[` + pyWSClass + `]*:[` + pyWSClass + `]*"([^"]+)"`)

// teamExtractAccessTokenFromSessionText ports
// extract_access_token_from_session_text (app.py:2623-2636): accept a bare
// token, a "Bearer x" header, a whole session JSON blob, or a JSON-ish text to
// regex-scrape; otherwise "" unless the input itself looks like a JWT.
func teamExtractAccessTokenFromSessionText(text string) string {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "Bearer ") {
		// Python: raw.split(None, 1)[1].strip() — the trailing .strip() makes a
		// plain TrimSpace of the remainder equivalent.
		return strings.TrimSpace(raw[len("Bearer "):])
	}
	// Ordered decode: find_access_token walks a dict's values in insertion order
	// and returns the FIRST hit, which Go's randomized map iteration would break.
	if decoded, err := teamDecodeOrderedJSON([]byte(raw)); err == nil {
		if token := teamFindAccessToken(decoded); token != "" {
			return token
		}
		// Python's `return find_access_token(...)` short-circuits the function
		// even when it found nothing, so a parseable blob never reaches the
		// regex/JWT fallbacks below.
		return ""
	}
	if m := teamAccessTokenJSONRe.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(m[1])
	}
	// len() in Python counts CHARACTERS, not bytes.
	if strings.Count(raw, ".") >= 2 && utf8.RuneCountInString(raw) > 80 {
		return raw
	}
	return ""
}

// teamFindAccessToken ports find_access_token (app.py:2605-2620).
func teamFindAccessToken(value any) string {
	switch v := value.(type) {
	case *teamObject:
		for _, key := range []string{"accessToken", "access_token", "token"} {
			if token := teamPyStrOrChain(v.Get(key)); token != "" {
				return token
			}
		}
		for _, key := range v.Keys() {
			if token := teamFindAccessToken(v.Get(key)); token != "" {
				return token
			}
		}
	case []any:
		for _, item := range v {
			if token := teamFindAccessToken(item); token != "" {
				return token
			}
		}
	}
	return ""
}

// teamQuote is urllib.parse.quote(value, safe=""): everything outside the
// unreserved set is percent-encoded, including '/'. Go's url.PathEscape is NOT
// equivalent — it leaves $ & + : = @ unescaped.
func teamQuote(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' ||
			c == '_' || c == '.' || c == '-' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

// teamJSONDumps serializes a body the way the Python HTTP layer does.
//
// Two deliberate deviations from a plain json.Marshal: Go escapes <, > and & to
// </>/& which json.dumps does not (SetEscapeHTML(false)), and
// json.dumps defaults to ensure_ascii=True, which Go has no switch for — hence
// the explicit \uXXXX pass. Separators are compact: app.py prefers curl_cffi
// (app.py:2809) whose json= path dumps without spaces, whereas the requests
// fallback would emit ", "/": ". JSON whitespace is insignificant, so only the
// Content-Length differs between the two.
func teamJSONDumps(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode appends a newline that json.dumps does not produce.
	return teamEscapeNonASCII(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// teamEscapeNonASCII reproduces json.dumps(ensure_ascii=True). Operating on the
// encoded bytes is safe: in valid JSON any rune >= U+0080 can only occur inside
// a string literal.
func teamEscapeNonASCII(raw []byte) []byte {
	ascii := true
	for _, c := range raw {
		if c >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return raw
	}
	var b bytes.Buffer
	b.Grow(len(raw))
	for i := 0; i < len(raw); {
		if raw[i] < utf8.RuneSelf {
			b.WriteByte(raw[i])
			i++
			continue
		}
		r, size := utf8.DecodeRune(raw[i:])
		i += size
		if r > 0xFFFF {
			// Python emits a surrogate PAIR for astral planes.
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			continue
		}
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.Bytes()
}

// teamTruncateRunes is Python's str[:n] — a CHARACTER slice, never splitting a
// multi-byte rune (these bodies carry localized error text).
func teamTruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// teamRandomUUID4 is str(uuid.uuid4()) (app.py:2822).
func teamRandomUUID4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		now := uint64(time.Now().UnixNano())
		binary.BigEndian.PutUint64(b[0:8], now)
		binary.BigEndian.PutUint64(b[8:16], now^0x9e3779b97f4a7c15)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------------------------------------------------------------------------
// ordered JSON — Python dicts are ordered, Go maps are not
// ---------------------------------------------------------------------------

// teamObject is a JSON object that remembers its key order. It exists because
// collect_memberships and find_access_token both take the FIRST hit of a
// value-order walk (app.py:3374, 2611), which map[string]any cannot reproduce.
type teamObject struct {
	keys []string
	vals map[string]any
}

func newTeamObject() *teamObject {
	return &teamObject{vals: map[string]any{}}
}

// set mirrors CPython dict assignment: a repeated key keeps its ORIGINAL
// position and only its value is replaced.
func (o *teamObject) set(key string, value any) {
	if _, exists := o.vals[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

// Get is dict.get(key) — nil when absent.
func (o *teamObject) Get(key string) any {
	if o == nil {
		return nil
	}
	return o.vals[key]
}

// Keys returns the keys in insertion order.
func (o *teamObject) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// teamNestedObject is get_nested_record (app.py:2662-2664) over a teamObject.
func teamNestedObject(o *teamObject, key string) *teamObject {
	if nested, ok := o.Get(key).(*teamObject); ok {
		return nested
	}
	return newTeamObject()
}

// teamDecodeOrderedJSON is json.loads with object key order preserved. Numbers
// stay json.Number so that str()-ing an id reproduces the literal Python saw
// (float64 would turn 12345678901234567890 into 1.2345678901234567e+19).
func teamDecodeOrderedJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := teamDecodeOrderedValue(dec)
	if err != nil {
		return nil, err
	}
	// json.loads rejects trailing data; so does this.
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("extra data after JSON value")
	}
	return value, nil
}

func teamDecodeOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		// string / json.Number / bool / nil
		return tok, nil
	}
	switch delim {
	case '{':
		obj := newTeamObject()
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("invalid object key %v", keyTok)
			}
			val, err := teamDecodeOrderedValue(dec)
			if err != nil {
				return nil, err
			}
			obj.set(key, val)
		}
		if _, err := dec.Token(); err != nil { // closing '}'
			return nil, err
		}
		return obj, nil
	case '[':
		list := []any{}
		for dec.More() {
			val, err := teamDecodeOrderedValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, val)
		}
		if _, err := dec.Token(); err != nil { // closing ']'
			return nil, err
		}
		return list, nil
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delim)
}

// teamPyTruthy is Python's bool(value) for decoded JSON: None/False/""/0/empty
// container are falsy. strings.TrimSpace is NOT the test — " " is TRUTHY in
// Python.
func teamPyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		// Any spelling of zero (0, 0.0, 0e5, -0) is falsy.
		if f, err := t.Float64(); err == nil {
			return f != 0
		}
		return t.String() != ""
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case *teamObject:
		return t != nil && len(t.keys) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return v != nil
	}
}

// teamPyStr is Python's str() for decoded JSON scalars. Only ever reached for
// TRUTHY values (the `or ""` chains filter the rest), so None -> "None" and the
// container reprs never come up.
func teamPyStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// teamPyStrOrChain is the `str(a or b or "").strip()` idiom (app.py:3382-3383,
// 3416, 3423-3425, 2608). It is NOT first_non_empty: the `or` chain tests the
// RAW value's truthiness, so a whitespace-only string wins the chain and then
// strips to "", whereas first_non_empty would skip it and try the next value.
func teamPyStrOrChain(values ...any) string {
	for _, v := range values {
		if teamPyTruthy(v) {
			return pyStrip(teamPyStr(v))
		}
	}
	return ""
}
