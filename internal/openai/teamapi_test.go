package openai

// Tests for the Team/K12 HTTP port. NOTHING here touches the network: every
// case exercises either a pure helper or a validation path that returns before
// a session is built. ChatGPTTeamSendInvite in particular must never reach the
// wire from a test — a successful call bills a Team seat.

import (
	"strings"
	"testing"

	http "github.com/bogdanfinn/fhttp"
)

func teamParse(t *testing.T, raw string) any {
	t.Helper()
	v, err := teamDecodeOrderedJSON([]byte(raw))
	if err != nil {
		t.Fatalf("teamDecodeOrderedJSON(%q): %v", raw, err)
	}
	return v
}

func TestTeamQuoteMatchesPythonQuoteSafeEmpty(t *testing.T) {
	cases := map[string]string{
		"workspace-example": "workspace-example",
		"request":                              "request",
		// url.PathEscape would leave every one of these untouched.
		"a/b":   "a%2Fb",
		"a:b":   "a%3Ab",
		"a@b":   "a%40b",
		"a+b":   "a%2Bb",
		"a&b=c": "a%26b%3Dc",
		"a$b":   "a%24b",
		// Unreserved set is verbatim; everything else is upper-hex UTF-8.
		"-_.~": "-_.~",
		"空间":   "%E7%A9%BA%E9%97%B4",
		"a b":  "a%20b",
	}
	for in, want := range cases {
		if got := teamQuote(in); got != want {
			t.Errorf("teamQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTeamEndpointURLs(t *testing.T) {
	const ws = "workspace-example"
	if got, want := teamK12InviteURL(ws, "request"),
		"https://chatgpt.com/backend-api/accounts/"+ws+"/invites/request"; got != want {
		t.Errorf("teamK12InviteURL = %q, want %q", got, want)
	}
	if got, want := teamInvitesURL(ws),
		"https://chatgpt.com/backend-api/accounts/"+ws+"/invites"; got != want {
		t.Errorf("teamInvitesURL = %q, want %q", got, want)
	}
	if got, want := teamUsersURL(ws),
		"https://chatgpt.com/backend-api/accounts/"+ws+"/users"; got != want {
		t.Errorf("teamUsersURL = %q, want %q", got, want)
	}
	if got, want := teamUserURL(ws, "user-XYZ"),
		"https://chatgpt.com/backend-api/accounts/"+ws+"/users/user-XYZ"; got != want {
		t.Errorf("teamUserURL = %q, want %q", got, want)
	}
	if got, want := teamAccountsCheckURL(),
		"https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"; got != want {
		t.Errorf("teamAccountsCheckURL = %q, want %q", got, want)
	}
}

func TestTeamInviteBodyIsByteExact(t *testing.T) {
	body, err := teamInviteBody("target@example.com", "")
	if err != nil {
		t.Fatalf("teamInviteBody: %v", err)
	}
	// Key order == the Python dict insertion order (app.py:3196-3200); no
	// seat_type when it is blank.
	want := `{"email_addresses":["target@example.com"],"role":"standard-user","resend_emails":true}`
	if string(body) != want {
		t.Errorf("invite body = %s, want %s", body, want)
	}

	// app.py:3201-3202 — a whitespace-only seat_type must NOT appear, a real one
	// is appended last.
	blank, _ := teamInviteBody("a@b.co", "   ")
	if strings.Contains(string(blank), "seat_type") {
		t.Errorf("blank seat_type leaked into body: %s", blank)
	}
	withSeat, _ := teamInviteBody("a@b.co", " business ")
	if !strings.HasSuffix(string(withSeat), `,"seat_type":"business"}`) {
		t.Errorf("seat_type not appended last: %s", withSeat)
	}
}

func TestTeamJSONDumpsMatchesPythonDumps(t *testing.T) {
	// json.dumps does NOT escape <, > or &; Go's default encoder does.
	raw, err := teamJSONDumps(map[string]string{"k": "a<b>c&d"})
	if err != nil {
		t.Fatalf("teamJSONDumps: %v", err)
	}
	if got, want := string(raw), `{"k":"a<b>c&d"}`; got != want {
		t.Errorf("HTML escaping: got %s, want %s", got, want)
	}
	// json.dumps defaults to ensure_ascii=True: BMP -> \uxxxx (lowercase hex),
	// astral -> a surrogate PAIR. Go has no ensure_ascii switch.
	raw, _ = teamJSONDumps(map[string]string{"k": "é漢\U0001F600"})
	want := "{\"k\":\"" + "\\u00e9\\u6f22\\ud83d\\ude00" + "\"}"
	if got := string(raw); got != want {
		t.Errorf("ensure_ascii: got %s, want %s", got, want)
	}
	// No trailing newline (json.Encoder adds one, json.dumps does not).
	if strings.HasSuffix(string(raw), "\n") {
		t.Error("teamJSONDumps left the encoder newline in place")
	}
}

func TestTeamExtractAccessTokenFromSessionText(t *testing.T) {
	longJWT := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.cccccccccccccccccccccccccccccc"

	cases := []struct{ name, in, want string }{
		{"empty", "   ", ""},
		// raw.split(None, 1)[1].strip() collapses the whitespace run.
		{"bearer", "Bearer   abc.def.ghi  ", "abc.def.ghi"},
		{"json top level", `{"accessToken":"tok-1"}`, "tok-1"},
		// find_access_token checks accessToken/access_token/token IN THAT ORDER
		// before recursing.
		{"json key priority", `{"token":"tok-c","access_token":"tok-b","accessToken":"tok-a"}`, "tok-a"},
		{"json nested", `{"user":{"nope":""},"session":{"access_token":"tok-n"}}`, "tok-n"},
		{"json list", `[{"a":1},{"token":"tok-l"}]`, "tok-l"},
		// str(v or "").strip() -> a blank value is skipped, not returned.
		{"json blank token skipped", `{"accessToken":"  ","access_token":"tok-2"}`, "tok-2"},
		// A parseable blob short-circuits: no regex/JWT fallback afterwards.
		{"json without token", `{"a":"` + longJWT + `"}`, ""},
		{"regex fallback", `garbage "access_token" : "tok-r" trailing`, "tok-r"},
		{"jwt heuristic", longJWT, longJWT},
		{"short dotted string is not a jwt", "a.b.c", ""},
	}
	for _, c := range cases {
		if got := teamExtractAccessTokenFromSessionText(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// Python's \s is Unicode-aware; Go's RE2 \s is not. A rendered/pasted blob
	// carries NBSP (U+00A0) around the colon.
	nbsp := "x \"access_token\" : \"tok-nbsp\" y"
	if got := teamExtractAccessTokenFromSessionText(nbsp); got != "tok-nbsp" {
		t.Errorf("NBSP separator: got %q, want %q", got, "tok-nbsp")
	}

	// len() counts characters: 81 multi-byte runes is >80 for Python even though
	// the byte length is far larger.
	multi := "漢." + strings.Repeat("漢", 78) + ".漢"
	if got := teamExtractAccessTokenFromSessionText(multi); got != multi {
		t.Errorf("rune-length JWT heuristic: got %q", got)
	}
}

func TestTeamOrderedJSONPreservesInsertionOrder(t *testing.T) {
	obj, ok := teamParse(t, `{"z":1,"a":2,"m":3}`).(*teamObject)
	if !ok {
		t.Fatal("want *teamObject")
	}
	if got := strings.Join(obj.Keys(), ","); got != "z,a,m" {
		t.Errorf("key order = %q, want %q", got, "z,a,m")
	}
	// CPython: a repeated key keeps its ORIGINAL position, only the value moves.
	dup, _ := teamParse(t, `{"a":1,"b":2,"a":3}`).(*teamObject)
	if got := strings.Join(dup.Keys(), ","); got != "a,b" {
		t.Errorf("duplicate key order = %q, want %q", got, "a,b")
	}
	if got := teamPyStrOrChain(dup.Get("a")); got != "3" {
		t.Errorf("duplicate key value = %q, want %q", got, "3")
	}
	// Numbers keep their literal so str()-ing a big id does not go scientific.
	big, _ := teamParse(t, `{"id":12345678901234567890}`).(*teamObject)
	if got := teamPyStrOrChain(big.Get("id")); got != "12345678901234567890" {
		t.Errorf("number literal = %q", got)
	}
	if _, err := teamDecodeOrderedJSON([]byte(`{"a":1} trailing`)); err == nil {
		t.Error("trailing data should be rejected, like json.loads")
	}
}

func TestTeamMembershipSelectionIsOrderDependent(t *testing.T) {
	const acct = "acct-1"
	// The nested "account" object is appended by the explicit nested check
	// BEFORE the recursion walks the sibling list, so it must win index 0 even
	// though the sibling appears earlier in the document.
	payload := teamParse(t, `{
	  "accounts": {
	    "wrapper": {
	      "siblings": [{"id":"acct-1","account_user_role":"standard-user","tag":"sibling"}],
	      "account": {"account_id":"acct-1","account_user_role":"owner","account_user_id":"u-owner","tag":"nested"}
	    }
	  }
	}`)
	m := teamMembershipFromCheckPayload(payload, acct)
	if got := teamPyStrOrChain(m.Get("tag")); got != "nested" {
		t.Fatalf("memberships[0] tag = %q, want %q", got, "nested")
	}
	if got := teamMembershipRole(m); got != "owner" {
		t.Errorf("role = %q, want %q", got, "owner")
	}
	if !teamOwnerRoles[teamMembershipRole(m)] {
		t.Error("owner role must trip the leave guard")
	}

	// A record for this account with NEITHER account_user_role NOR
	// account_user_id is not a membership (app.py:3363-3365).
	none := teamMembershipFromCheckPayload(teamParse(t, `{"id":"acct-1","name":"x"}`), acct)
	if len(none.Keys()) != 0 {
		t.Errorf("non-membership matched: %v", none.Keys())
	}
	if teamMembershipRole(none) != "" {
		t.Error("empty membership must yield an empty role")
	}

	// account_user_id alone is enough, and role falls back to "role".
	only := teamMembershipFromCheckPayload(
		teamParse(t, `{"id":"acct-1","account_user_id":"u-1","role":"Standard-User"}`), acct)
	if got := teamMembershipRole(only); got != "standard-user" {
		t.Errorf("role fallback/casefold = %q, want %q", got, "standard-user")
	}
	if got := teamPyStrOrChain(only.Get("account_user_id")); got != "u-1" {
		t.Errorf("account_user_id = %q", got)
	}

	// A different workspace never matches.
	if m := teamMembershipFromCheckPayload(payload, "acct-2"); len(m.Keys()) != 0 {
		t.Error("membership matched the wrong account id")
	}
}

func TestTeamMembersFromUsersPayload(t *testing.T) {
	if got := teamMembersFromUsersPayload(teamParse(t, `[{"id":"a"}]`)); len(got) != 1 {
		t.Errorf("bare list: got %d members", len(got))
	}
	// items -> users -> members, first LIST wins (a non-list "items" is skipped).
	obj := teamParse(t, `{"items":{"not":"a list"},"users":[{"id":"u"}],"members":[]}`)
	got := teamMembersFromUsersPayload(obj)
	if len(got) != 1 {
		t.Fatalf("want the users list, got %d entries", len(got))
	}
	if got := teamMembersFromUsersPayload(teamParse(t, `{"other":[1]}`)); got != nil {
		t.Errorf("unknown shape should yield no members, got %v", got)
	}
	if got := teamMembersFromUsersPayload(nil); got != nil {
		t.Errorf("nil payload should yield no members, got %v", got)
	}
}

func TestTeamResolveMemberID(t *testing.T) {
	members := teamParse(t, `[
	  {"user_id":"u-other","email":"someone@else.com"},
	  {"id":"u-me","user":{"email":"<Me@Example.COM>","id":"nested-me"}},
	  {"user_id":"u-byid","email":"third@example.com"}
	]`).([]any)

	// Email match is normalized (angle brackets stripped) and case-insensitive.
	if got := teamResolveMemberID(members, "me@example.com", "", ""); got != "u-me" {
		t.Errorf("email match = %q, want %q", got, "u-me")
	}
	// Id match against either account_user_id or the token's user_id.
	if got := teamResolveMemberID(members, "", "u-byid", ""); got != "u-byid" {
		t.Errorf("account_user_id match = %q, want %q", got, "u-byid")
	}
	if got := teamResolveMemberID(members, "", "", "nested-me"); got != "u-me" {
		t.Errorf("nested user id match = %q, want %q", got, "u-me")
	}
	// No signal at all -> no match (the caller then falls back to the known ids).
	if got := teamResolveMemberID(members, "", "", ""); got != "" {
		t.Errorf("no criteria should not match, got %q", got)
	}
	// Empty member_email must not match a member with no email either.
	noEmail := teamParse(t, `[{"user_id":"u-x"}]`).([]any)
	if got := teamResolveMemberID(noEmail, "", "", ""); got != "" {
		t.Errorf("blank-vs-blank email matched: %q", got)
	}
	// `str(user_id or id or "").strip()`: a whitespace-only user_id is TRUTHY, so
	// it wins the or-chain and strips to "" — the member is skipped even though
	// "id" holds a usable value. first_non_empty would have returned "u-fall".
	blank := teamParse(t, `[{"user_id":"   ","id":"u-fall","email":"me@example.com"}]`).([]any)
	if got := teamResolveMemberID(blank, "me@example.com", "", ""); got != "" {
		t.Errorf("whitespace user_id should blank the candidate, got %q", got)
	}
}

func TestTeamAccessTokenUserID(t *testing.T) {
	tok := jwt(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_user_id": "user-abc"},
	})
	if got := teamAccessTokenUserID(tok); got != "user-abc" {
		t.Errorf("chatgpt_user_id = %q, want %q", got, "user-abc")
	}
	tok = jwt(map[string]any{
		"https://api.openai.com/auth": map[string]any{"user_id": "user-fallback"},
	})
	if got := teamAccessTokenUserID(tok); got != "user-fallback" {
		t.Errorf("user_id fallback = %q, want %q", got, "user-fallback")
	}
	if got := teamAccessTokenUserID("not-a-jwt"); got != "" {
		t.Errorf("garbage token = %q, want empty", got)
	}
}

func TestTeamPyTruthyAndStrOrChain(t *testing.T) {
	// Python falsiness of decoded JSON values.
	for _, raw := range []string{`{"v":null}`, `{"v":false}`, `{"v":""}`, `{"v":0}`, `{"v":0.0}`, `{"v":[]}`, `{"v":{}}`} {
		obj := teamParse(t, raw).(*teamObject)
		if teamPyTruthy(obj.Get("v")) {
			t.Errorf("%s should be falsy", raw)
		}
	}
	for _, raw := range []string{`{"v":true}`, `{"v":" "}`, `{"v":1}`, `{"v":-0.5}`, `{"v":[0]}`, `{"v":{"a":1}}`} {
		obj := teamParse(t, raw).(*teamObject)
		if !teamPyTruthy(obj.Get("v")) {
			t.Errorf("%s should be truthy", raw)
		}
	}
	// str(True) is "True", not Go's "true".
	if got := teamPyStrOrChain(true); got != "True" {
		t.Errorf("str(True) = %q", got)
	}
	// The chain stops at the first TRUTHY value, then strips.
	if got := teamPyStrOrChain(nil, " kept ", "second"); got != "kept" {
		t.Errorf("or-chain = %q, want %q", got, "kept")
	}
	if got := teamPyStrOrChain(nil, "", nil); got != "" {
		t.Errorf("all-falsy chain = %q", got)
	}
}

func TestTeamFailureHints(t *testing.T) {
	if got := TeamInviteFailureHint(401, `{"detail":"Not a WORKSPACE ACCOUNT"}`); !strings.Contains(got, "工作空间身份") {
		t.Errorf("401+workspace hint = %q", got)
	}
	if got := TeamInviteFailureHint(401, "nope"); !strings.Contains(got, "邀请权限") {
		t.Errorf("plain 401 hint = %q", got)
	}
	if got := TeamInviteFailureHint(403, "nope"); !strings.Contains(got, "邀请权限") {
		t.Errorf("403 hint = %q", got)
	}
	if got := TeamInviteFailureHint(500, "workspace account"); got != "" {
		t.Errorf("500 should have no hint, got %q", got)
	}
	if got := TeamLeaveFailureHint(403); !strings.Contains(got, "Owner") {
		t.Errorf("leave 403 hint = %q", got)
	}
	if got := TeamLeaveFailureHint(400); got != "" {
		t.Errorf("leave 400 should have no hint, got %q", got)
	}
	if !teamHTTPOK(200) || !teamHTTPOK(299) || teamHTTPOK(300) || teamHTTPOK(199) {
		t.Error("teamHTTPOK must be 200 <= status < 300")
	}
}

// TestTeamValidationFailsBeforeAnyRequest pins the pre-flight guards. Every case
// must return an error WITHOUT opening a connection — especially the invite,
// which bills a seat when it reaches the server.
func TestTeamValidationFailsBeforeAnyRequest(t *testing.T) {
	tok := jwt(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_user_id": "u"}})

	if _, _, err := ChatGPTTeamSendInvite("", "acct", "a@b.co", ""); err == nil ||
		err.Error() != "缺少 Team 邀请者 Access Token" {
		t.Errorf("missing token: %v", err)
	}
	if _, _, err := ChatGPTTeamSendInvite(tok, "  ", "a@b.co", ""); err == nil ||
		err.Error() != "缺少 Team account_id" {
		t.Errorf("missing account_id: %v", err)
	}
	// normalize_email_address keeps the raw text when no address matches, and the
	// error echoes that normalized text.
	if _, _, err := ChatGPTTeamSendInvite(tok, "acct", "not-an-email", ""); err == nil ||
		err.Error() != "目标邮箱格式错误: not-an-email" {
		t.Errorf("bad email: %v", err)
	}

	if _, _, _, err := ChatGPTTeamLeaveWorkspace("", "acct", "a@b.co", ""); err == nil ||
		err.Error() != "缺少 Team 成员 Access Token" {
		t.Errorf("leave missing token: %v", err)
	}
	if _, _, _, err := ChatGPTTeamLeaveWorkspace(tok, "", "a@b.co", ""); err == nil ||
		err.Error() != "缺少 Team account_id" {
		t.Errorf("leave missing account_id: %v", err)
	}

	// The workspace-id guard runs BEFORE the session is built, so it wins over a
	// missing Access Token (app.py:3171-3173).
	if _, _, err := K12RequestWorkspaceInvite("", "  ", ""); err == nil ||
		err.Error() != "请先填写 K12 Workspace ID" {
		t.Errorf("k12 missing workspace: %v", err)
	}
	if _, _, err := K12RequestWorkspaceInvite("", "ws-1", ""); err == nil ||
		err.Error() != "当前账号没有 Access Token，请先注册并获取 Session 信息" {
		t.Errorf("k12 missing token: %v", err)
	}
}

func TestTeamSessionHeaders(t *testing.T) {
	s := &teamSession{
		client:   nil, // not used by header()
		token:    "tok-1",
		deviceID: "dev-1",
	}
	h := s.header(map[string]string{"chatgpt-account-id": "acct-1"})
	want := map[string]string{
		"authorization":      "Bearer tok-1",
		"content-type":       "application/json",
		"oai-device-id":      "dev-1",
		"cookie":             "oai-did=dev-1", // same uuid as oai-device-id
		"origin":             "https://chatgpt.com",
		"referer":            "https://chatgpt.com/",
		"accept":             "*/*",
		"chatgpt-account-id": "acct-1",
	}
	// Keys are stored lowercase verbatim (fhttp keeps the exact casing given),
	// so the map is indexed directly rather than through the canonicalizing Get.
	for k, v := range want {
		got := h[k]
		if len(got) != 1 || got[0] != v {
			t.Errorf("header %s = %v, want %q", k, got, v)
		}
	}
	// The per-call header must be appended to the order list, not dropped.
	order := h[http.HeaderOrderKey]
	if len(order) != len(teamSessionHeaderOrder)+1 || order[len(order)-1] != "chatgpt-account-id" {
		t.Errorf("header order = %v", order)
	}
	// A per-call override of an existing header must not duplicate its position,
	// and its name is lowercased before merging.
	h2 := s.header(map[string]string{"Referer": "https://chatgpt.com/x"})
	if got := h2["referer"]; len(got) != 1 || got[0] != "https://chatgpt.com/x" {
		t.Errorf("referer override = %v", got)
	}
	if len(h2[http.HeaderOrderKey]) != len(teamSessionHeaderOrder) {
		t.Errorf("override should not extend the order list: %v", h2[http.HeaderOrderKey])
	}
}
