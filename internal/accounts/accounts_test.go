package accounts

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"unicode"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func acct(email string) models.MailAccount {
	return models.MailAccount{Email: email, AccountType: "free"}
}

// ---------------------------------------------------------------------------
// Derived 状态 precedence (app.py:19057-19068, UI_SPEC §1.6/§7.1)
// ---------------------------------------------------------------------------

func TestStatusTextPrecedence(t *testing.T) {
	session := func(kv map[string]any) map[string]any { return kv }

	tests := []struct {
		name string
		acc  models.MailAccount
		lk   Lookups
		want string
	}{
		{
			name: "link beats everything",
			acc:  models.MailAccount{Email: "a@x.com", Status: "登录失败"},
			lk: Lookups{
				Results:        map[string]any{"a@x.com": "https://pay.openai.com/c/x"},
				SessionResults: map[string]any{"a@x.com": session(map[string]any{"access_token": "tok", "k12_status": 200})},
			},
			want: StatusLinkExtracted,
		},
		{
			// app.py:19058 strips before testing; NBSP is whitespace to Python
			// (and to pyStrip), so a blank link is not a link — but the email
			// is still PRESENT in results, which is 成功, not 待处理.
			name: "blank link falls through to membership 成功",
			acc:  acct("a@x.com"),
			lk:   Lookups{Results: map[string]any{"a@x.com": "  　"}},
			want: StatusSuccess,
		},
		{
			name: "account status beats session",
			acc:  models.MailAccount{Email: "a@x.com", Status: "处理中"},
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"access_token": "tok"})}},
			want: "处理中",
		},
		{
			name: "session_json alone is a session",
			acc:  acct("a@x.com"),
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"session_json": "{}"})}},
			want: StatusSessionAcquired,
		},
		{
			name: "blank access_token is not a session",
			acc:  acct("a@x.com"),
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"access_token": "  "})}},
			want: StatusPending,
		},
		{
			name: "non-dict session payload is ignored, not a crash",
			acc:  acct("a@x.com"),
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": "corrupt"}},
			want: StatusPending,
		},
		{
			name: "nothing known",
			acc:  acct("a@x.com"),
			lk:   Lookups{},
			want: StatusPending,
		},
		{
			name: "k12 2xx overlays 待处理",
			acc:  acct("a@x.com"),
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"k12_status": "200"})}},
			want: StatusK12Success,
		},
		{
			name: "k12 2xx as a JSON number overlays 待处理",
			acc:  acct("a@x.com"),
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"k12_status": float64(204)})}},
			want: StatusK12Success,
		},
		{
			name: "k12 2xx over Session已刷新 keeps the refresh marker",
			acc:  models.MailAccount{Email: "a@x.com", Status: StatusSessionRefreshed},
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"k12_status": 299})}},
			want: StatusK12SuccessRefreshed,
		},
		{
			name: "k12 2xx does not overlay a non-whitelisted status",
			acc:  models.MailAccount{Email: "a@x.com", Status: "K12失败"},
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"k12_status": 200})}},
			want: "K12失败",
		},
		{
			name: "k12 3xx is not success",
			acc:  acct("a@x.com"),
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"k12_status": 302})}},
			want: StatusPending,
		},
		{
			name: "non-numeric k12_status is not success",
			acc:  acct("a@x.com"),
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"k12_status": "2xx"})}},
			want: StatusPending,
		},
		{
			name: "auth phone overlay on 待处理",
			acc:  models.MailAccount{Email: "a@x.com", AuthPhoneNumber: "+1555", AuthPhoneSMSURL: "https://sms/1"},
			lk:   Lookups{},
			want: StatusNeedRTWithAuthPhone,
		},
		{
			name: "auth phone overlay needs both phone fields",
			acc:  models.MailAccount{Email: "a@x.com", AuthPhoneNumber: "+1555"},
			lk:   Lookups{},
			want: StatusPending,
		},
		{
			name: "auth phone overlay suppressed once an RT exists",
			acc:  models.MailAccount{Email: "a@x.com", OpenaiRT: "rt", AuthPhoneNumber: "+1555", AuthPhoneSMSURL: "https://sms/1"},
			lk:   Lookups{},
			want: StatusPending,
		},
		{
			// Overlay ORDER matters: K12 rewrites 待处理 first, so the auth
			// phone overlay (which only fires on an exact 待处理) never sees it.
			name: "k12 overlay runs before the auth phone overlay",
			acc:  models.MailAccount{Email: "a@x.com", AuthPhoneNumber: "+1555", AuthPhoneSMSURL: "https://sms/1"},
			lk:   Lookups{SessionResults: map[string]any{"a@x.com": session(map[string]any{"k12_status": 200})}},
			want: StatusK12Success,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusText(tc.acc, tc.lk); got != tc.want {
				t.Fatalf("StatusText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRefreshStatusText(t *testing.T) {
	k12 := Lookups{SessionResults: map[string]any{"a@x.com": map[string]any{"k12_status": 200}}}
	tests := []struct {
		name   string
		lk     Lookups
		result map[string]any
		want   string
	}{
		{"nil result", Lookups{}, nil, StatusSessionRefreshed},
		{"workspace without k12 success", Lookups{}, map[string]any{"target_workspace_id": "ws_1"}, StatusK12SessionRefreshed},
		{"workspace with k12 success", k12, map[string]any{"target_workspace_id": "ws_1"}, StatusK12SuccessRefreshed},
		{"blank workspace is no workspace", Lookups{}, map[string]any{"target_workspace_id": "  "}, StatusSessionRefreshed},
		{"plus plan", Lookups{}, map[string]any{"access_summary": map[string]any{"plan_type": " PLUS "}}, StatusPlusSessionRefreshed},
		{"k12 plan", Lookups{}, map[string]any{"access_summary": map[string]any{"plan_type": "K12"}}, StatusK12SessionRefreshed},
		{"unknown plan is no plan", Lookups{}, map[string]any{"access_summary": map[string]any{"plan_type": "unknown"}}, StatusSessionRefreshed},
		{"non-dict access_summary", Lookups{}, map[string]any{"access_summary": "plus"}, StatusSessionRefreshed},
		{"workspace wins over plan", k12, map[string]any{"target_workspace_id": "ws", "access_summary": map[string]any{"plan_type": "plus"}}, StatusK12SuccessRefreshed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RefreshStatusText("a@x.com", tc.result, tc.lk); got != tc.want {
				t.Fatalf("RefreshStatusText = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 失败 is a substring rule, not an enum (app.py:19153, UI_SPEC §7.1)
// ---------------------------------------------------------------------------

func TestFailureFilterIsSubstringMatch(t *testing.T) {
	// Real strings from the UI_SPEC §1.6 status vocabulary, one per word.
	failing := []string{
		"授权失败", "提取长链失败", "K12失败", // 失败
		"支付错误", "接口错误", // 错误
		"代理耗尽", "协议代理耗尽", // 耗尽
		"账号已停用",         // 停用
		"疑似已封禁", "查封禁中", // 封禁
		"当前不可用",   // 不可用
		"手机号被拒绝",  // 拒绝
		"等待验证码超时", // 超时
	}
	for _, status := range failing {
		acc := models.MailAccount{Email: "a@x.com", Status: status}
		if !MatchesStatusFilter(acc, StatusFilterFailed, Lookups{}) {
			t.Errorf("status %q should match the 失败 filter", status)
		}
		if MatchesStatusFilter(acc, StatusFilterPending, Lookups{}) {
			t.Errorf("status %q must be excluded from the 待处理 filter", status)
		}
	}

	passing := []string{
		StatusPending, StatusSuccess, StatusSessionAcquired, StatusLinkExtracted,
		StatusK12Success, "处理中", "已登录", "Free RT已获取", "未见封禁邮件是安全的",
	}
	for _, status := range passing {
		// 未见封禁邮件 does contain 封禁 — assert only on the ones that must not.
		if status == "未见封禁邮件是安全的" {
			if !MatchesStatusFilter(models.MailAccount{Email: "a@x.com", Status: status}, StatusFilterFailed, Lookups{}) {
				t.Errorf("substring rule: %q contains 封禁 so it DOES match 失败 (a known false positive of the Python rule, app.py:19153)", status)
			}
			continue
		}
		if MatchesStatusFilter(models.MailAccount{Email: "a@x.com", Status: status}, StatusFilterFailed, Lookups{}) {
			t.Errorf("status %q should not match the 失败 filter", status)
		}
	}
}

func TestPendingFilterNeedsNoSessionNoLink(t *testing.T) {
	acc := acct("a@x.com")
	if !MatchesStatusFilter(acc, StatusFilterPending, Lookups{}) {
		t.Fatal("a bare account is 待处理")
	}
	withSession := Lookups{SessionResults: map[string]any{"a@x.com": map[string]any{"access_token": "t"}}}
	if MatchesStatusFilter(acc, StatusFilterPending, withSession) {
		t.Fatal("an account with a session is not 待处理")
	}
	withLink := Lookups{Results: map[string]any{"a@x.com": "https://link"}}
	if MatchesStatusFilter(acc, StatusFilterPending, withLink) {
		t.Fatal("an account with a link is not 待处理")
	}
	if !MatchesStatusFilter(acc, StatusFilterLinked, withLink) {
		t.Fatal("提链成功 must see the link")
	}
}

func TestTypeFilters(t *testing.T) {
	cases := map[string]struct{ plus, team bool }{
		" Plus ": {true, false},
		"PRO":    {true, false},
		"team":   {false, true},
		"free":   {false, false},
		"":       {false, false},
	}
	for accountType, want := range cases {
		acc := models.MailAccount{Email: "a@x.com", AccountType: accountType}
		if got := MatchesStatusFilter(acc, StatusFilterPlus, Lookups{}); got != want.plus {
			t.Errorf("type %q Plus filter = %v, want %v", accountType, got, want.plus)
		}
		if got := MatchesStatusFilter(acc, StatusFilterTeam, Lookups{}); got != want.team {
			t.Errorf("type %q Team filter = %v, want %v", accountType, got, want.team)
		}
	}
	if !MatchesStatusFilter(acct("a@x.com"), "not a filter", Lookups{}) {
		t.Fatal("an unknown filter passes everything (app.py:19158)")
	}
}

// ---------------------------------------------------------------------------
// Search / visibility (app.py:19108-19134)
// ---------------------------------------------------------------------------

func TestSearchTerms(t *testing.T) {
	// U+00A0 NBSP and U+3000 ideographic space are whitespace to Python's \s
	// but not to Go's RE2 \s — the whole point of reWhitespace.
	got := SearchTerms("  Alpha BETA　\tgamma \n")
	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchTerms = %#v, want %#v", got, want)
	}
	if SearchTerms(" 　 ") != nil {
		t.Fatal("whitespace-only search yields no terms")
	}
}

func TestVisibleFiltersAndSearch(t *testing.T) {
	accs := []models.MailAccount{
		{Email: "Alice@x.com", AccountType: "plus", Group: "vip"},
		{Email: "bob@x.com", AccountType: "team"},
		{Email: "carol@y.com", AccountType: "free", Group: "vip", Status: "登录失败"},
	}
	lk := Lookups{Results: map[string]any{"bob@x.com": "https://link"}}

	keys := func(f Filter) []string { return VisibleKeys(accs, f, lk) }

	if got, want := keys(Filter{}), []string{"alice@x.com", "bob@x.com", "carol@y.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("zero Filter = %v, want %v", got, want)
	}
	if got, want := keys(Filter{Group: "vip"}), []string{"alice@x.com", "carol@y.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group filter = %v, want %v", got, want)
	}
	if got, want := keys(Filter{Group: GroupDefault}), []string{"bob@x.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("未分组 must catch the empty group = %v, want %v", got, want)
	}
	// AND-ed terms across different source columns: "vip" is the group,
	// "@x" the email, and both must hold.
	if got, want := keys(Filter{Search: "VIP @x"}), []string{"alice@x.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("multi-term AND = %v, want %v", got, want)
	}
	// The derived status is searchable, not just the stored one.
	if got, want := keys(Filter{Search: "长链已提取"}), []string{"bob@x.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("derived status search = %v, want %v", got, want)
	}
	if got := keys(Filter{Search: "vip nothing-matches-this"}); len(got) != 0 {
		t.Fatalf("one failing term fails the row: %v", got)
	}
	if got, want := keys(Filter{Status: StatusFilterFailed}), []string{"carol@y.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("status filter = %v, want %v", got, want)
	}
	if got, want := VisibleIndices(accs, Filter{Group: "vip"}, lk), []int{0, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("VisibleIndices = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Sorting (app.py:19098-19106, deviation documented on SortKeyOf)
// ---------------------------------------------------------------------------

func TestSortKeyOfUsesStableIdentity(t *testing.T) {
	acc := models.MailAccount{Email: "MiXeD@X.com", AccountType: "Plus", Status: "已登录"}
	lk := Lookups{LinkAttempts: map[string]any{"MiXeD@X.com": float64(3)}}

	if got := SortKeyOf(acc, ColumnEmail, lk); got.Text != "mixed@x.com" {
		t.Fatalf("email key = %q", got.Text)
	}
	if got := SortKeyOf(acc, "no such column", lk); got.Text != "mixed@x.com" {
		t.Fatalf("unknown column must fall back to email, got %q", got.Text)
	}
	if got := SortKeyOf(acc, ColumnType, lk); got.Text != "plus" {
		t.Fatalf("type key = %q", got.Text)
	}
	if got := SortKeyOf(acc, ColumnStatus, lk); got.Text != "已登录" {
		t.Fatalf("status key = %q", got.Text)
	}
	if got := SortKeyOf(acc, ColumnAttempts, lk); got.Num != 3 {
		t.Fatalf("attempts key = %d", got.Num)
	}
	// The key must not depend on the account's position in any slice.
	reordered := SortKeyOf(acc, ColumnEmail, lk)
	if reordered != SortKeyOf(acc, ColumnEmail, lk) {
		t.Fatal("SortKeyOf must be a pure function of the account")
	}
}

func TestSortAccounts(t *testing.T) {
	accs := []models.MailAccount{
		{Email: "c@x.com", AccountType: "team"},
		{Email: "A@x.com", AccountType: "team"},
		{Email: "b@x.com", AccountType: "free"},
	}
	lk := Lookups{LinkAttempts: map[string]any{"c@x.com": 5, "b@x.com": "2"}}

	emails := func(list []models.MailAccount) []string {
		out := make([]string, len(list))
		for i, a := range list {
			out[i] = a.Email
		}
		return out
	}

	if got, want := emails(SortAccounts(accs, ColumnEmail, SortCustom, lk)), []string{"c@x.com", "A@x.com", "b@x.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("custom direction keeps list order: %v", got)
	}
	if got, want := emails(SortAccounts(accs, ColumnEmail, SortAsc, lk)), []string{"A@x.com", "b@x.com", "c@x.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("asc email (casefolded) = %v, want %v", got, want)
	}
	if got, want := emails(SortAccounts(accs, ColumnEmail, SortDesc, lk)), []string{"c@x.com", "b@x.com", "A@x.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("desc email = %v, want %v", got, want)
	}
	// attempts: b=2, c=5, A=0 (missing key)
	if got, want := emails(SortAccounts(accs, ColumnAttempts, SortAsc, lk)), []string{"A@x.com", "b@x.com", "c@x.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("asc attempts = %v, want %v", got, want)
	}
	// Ties keep list order in BOTH directions (Python's sorted is stable even
	// with reverse=True): the two "team" rows stay c, A.
	if got, want := emails(SortAccounts(accs, ColumnType, SortDesc, lk)), []string{"c@x.com", "A@x.com", "b@x.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("desc type must be stable within ties = %v, want %v", got, want)
	}
	if emails(accs)[0] != "c@x.com" {
		t.Fatal("SortAccounts must not mutate its input")
	}
}

func TestDisplayAndRow(t *testing.T) {
	accs := []models.MailAccount{
		{Email: "b@x.com", AccountType: "free"},
		{Email: "a@x.com", AccountType: "plus"},
	}
	lk := Lookups{
		Results:      map[string]any{"a@x.com": "https://link"},
		LinkAttempts: map[string]any{"a@x.com": float64(2)},
	}
	rows := Display(accs, Filter{Status: StatusFilterLinked}, ColumnEmail, SortAsc, lk)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	want := Row{Key: "a@x.com", Email: "a@x.com", Type: "plus", Status: StatusLinkExtracted, Attempts: 2}
	if got := RowOf(rows[0], lk); got != want {
		t.Fatalf("RowOf = %#v, want %#v", got, want)
	}
}

func TestAttemptCountClampsAndCoerces(t *testing.T) {
	lk := Lookups{LinkAttempts: map[string]any{
		"neg@x.com":   float64(-4),
		"str@x.com":   "7",
		"junk@x.com":  "n/a",
		"float@x.com": 3.9,
		"nil@x.com":   nil,
	}}
	cases := map[string]int{"neg@x.com": 0, "str@x.com": 7, "junk@x.com": 0, "float@x.com": 3, "nil@x.com": 0, "missing@x.com": 0}
	for email, want := range cases {
		if got := lk.AttemptCount(email); got != want {
			t.Errorf("AttemptCount(%q) = %d, want %d", email, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Regressions
// ---------------------------------------------------------------------------

// REGRESSION (defect: ndDigitValue walked across block boundaries). Unicode's
// decimal-digit runs are ten codepoints long but are NOT separated by gaps —
// U+1D7CE..U+1D7FF is one unbroken span of five ten-digit Mathematical blocks.
// The old "walk down at most nine places while the previous rune is a digit"
// returned 9 for every codepoint after the first block, so a k12_status written
// in mathematical digits scored 999 instead of 200 and HasK12Success said no.
// Expected values are Python's int(chr(cp)) for the same codepoints.
func TestNdDigitValueSpansAdjacentBlocks(t *testing.T) {
	cases := map[rune]int{
		'0': 0, '9': 9,
		0x0660: 0, 0x0669: 9, // Arabic-Indic
		0x0966: 0, 0x096F: 9, // Devanagari
		0xFF10: 0, 0xFF15: 5, 0xFF19: 9, // fullwidth
		0x1D7CE: 0, 0x1D7D7: 9, // mathematical bold        (run 1)
		0x1D7D8: 0, 0x1D7E1: 9, // mathematical double-struck (run 2, no gap)
		0x1D7E2: 0, 0x1D7E9: 7, 0x1D7EB: 9, // sans-serif    (run 3)
		0x1D7EC: 0, 0x1D7EE: 2, 0x1D7EF: 3, 0x1D7F0: 4, // sans-serif bold (run 4)
		0x1D7F3: 7, 0x1D7F6: 0, 0x1D7F7: 1, 0x1D7FF: 9, // monospace (run 5)
	}
	for r, want := range cases {
		got, ok := ndDigitValue(r)
		if !ok || got != want {
			t.Errorf("ndDigitValue(U+%04X) = %d,%v want %d", r, got, ok, want)
		}
	}
	// End to end: the value actually reaches the 2xx test.
	if n, ok := pyDigitInt("\U0001D7E4\U0001D7E2\U0001D7E2"); !ok || n != 200 {
		t.Errorf(`pyDigitInt("𝟤𝟢𝟢") = %d,%v want 200`, n, ok)
	}
	lk := Lookups{SessionResults: map[string]any{
		"a@x.com": map[string]any{"k12_status": "\U0001D7E4\U0001D7E2\U0001D7E2"},
	}}
	if !lk.HasK12Success("a@x.com") {
		t.Error("a 2xx k12_status in mathematical digits must count as success")
	}
}

// REGRESSION (defect: SortAccounts short-circuited on any unknown direction).
// app.py:19728 short-circuits on EXACTLY ACCOUNT_SORT_CUSTOM and app.py:19730
// reverses on EXACTLY ACCOUNT_SORT_DESC, so anything else sorts ascending.
// Go's zero value "" is the one addition, matching the Filter.Group convention.
func TestSortAccountsDirectionIsLiteral(t *testing.T) {
	accs := []models.MailAccount{{Email: "c@x.io"}, {Email: "a@x.io"}, {Email: "b@x.io"}}
	emails := func(list []models.MailAccount) []string {
		out := make([]string, len(list))
		for i, a := range list {
			out[i] = a.Email
		}
		return out
	}
	listOrder := []string{"c@x.io", "a@x.io", "b@x.io"}
	ascOrder := []string{"a@x.io", "b@x.io", "c@x.io"}

	for _, dir := range []string{SortCustom, ""} {
		if got := emails(SortAccounts(accs, ColumnEmail, dir, Lookups{})); !reflect.DeepEqual(got, listOrder) {
			t.Errorf("direction %q must keep list order, got %v", dir, got)
		}
	}
	for _, dir := range []string{SortAsc, "junk", "CUSTOM", "Custom", "ASC"} {
		if got := emails(SortAccounts(accs, ColumnEmail, dir, Lookups{})); !reflect.DeepEqual(got, ascOrder) {
			t.Errorf("direction %q must sort ascending (app.py:19730), got %v", dir, got)
		}
	}
	if got := emails(SortAccounts(accs, ColumnEmail, SortDesc, Lookups{})); !reflect.DeepEqual(got, []string{"c@x.io", "b@x.io", "a@x.io"}) {
		t.Errorf("desc: %v", got)
	}
}

// Ties must keep list order in BOTH directions and the tie must be OBSERVABLE:
// these rows differ only in OpenaiRT, which no sort key reads.
func TestSortIsStableWithObservableTies(t *testing.T) {
	accs := []models.MailAccount{
		{Email: "a@x.io", AccountType: "plus", OpenaiRT: "r1"},
		{Email: "a@x.io", AccountType: "plus", OpenaiRT: "r2"},
		{Email: "b@x.io", AccountType: "plus", OpenaiRT: "r3"},
		{Email: "a@x.io", AccountType: "plus", OpenaiRT: "r4"},
	}
	rts := func(list []models.MailAccount) []string {
		out := make([]string, len(list))
		for i, a := range list {
			out[i] = a.OpenaiRT
		}
		return out
	}
	// Every row ties on type, so both directions must be a no-op.
	for _, dir := range []string{SortAsc, SortDesc} {
		if got := rts(SortAccounts(accs, ColumnType, dir, Lookups{})); !reflect.DeepEqual(got, []string{"r1", "r2", "r3", "r4"}) {
			t.Errorf("type/%s: sorted() is stable in both directions, got %v", dir, got)
		}
	}
	// Descending by email: the b row leads, then the three tied a rows in list order.
	if got := rts(SortAccounts(accs, ColumnEmail, SortDesc, Lookups{})); !reflect.DeepEqual(got, []string{"r3", "r1", "r2", "r4"}) {
		t.Errorf("email/desc ties: got %v want [r3 r1 r2 r4]", got)
	}
}

// The email sort key is Python's casefold(email) — NOT Key, which also strips.
// A leading space therefore sorts first, exactly as it does in the Tk table.
func TestEmailSortKeyDoesNotStrip(t *testing.T) {
	padded := models.MailAccount{Email: " a@x.io"}
	if got := SortKeyOf(padded, ColumnEmail, Lookups{}); got.Text != " a@x.io" {
		t.Fatalf("sort key = %q, want %q (app.py:19106 casefolds but does not strip)", got.Text, " a@x.io")
	}
	if got := Key(padded); got != "a@x.io" {
		t.Fatalf("Key = %q, want %q (Key does strip)", got, "a@x.io")
	}
}

// pythonWhitespace is the complete set of codepoints for which CPython 3.12's
// str.isspace() is true — and, identically, the set its `\s` regex class
// matches for a str pattern (verified by scanning all 1,112,064 codepoints).
// Go's unicode.IsSpace omits U+001C..U+001F and RE2's \s omits those plus \v,
// U+0085 and all of \p{Z}; rendered Chinese UI text routinely carries U+00A0
// and U+3000, so getting this set wrong silently breaks search tokenising.
var pythonWhitespace = []rune{
	0x0009, 0x000A, 0x000B, 0x000C, 0x000D, 0x001C, 0x001D, 0x001E, 0x001F,
	0x0020, 0x0085, 0x00A0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004,
	0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200A, 0x2028, 0x2029, 0x202F,
	0x205F, 0x3000,
}

func TestWhitespaceSetMatchesPythonExactly(t *testing.T) {
	want := make(map[rune]bool, len(pythonWhitespace))
	for _, r := range pythonWhitespace {
		want[r] = true
	}
	// Scan every codepoint: the set must be exactly right in both directions.
	for cp := 0; cp < 0x110000; cp++ {
		if cp >= 0xD800 && cp <= 0xDFFF {
			continue
		}
		r := rune(cp)
		if got := pyIsSpace(r); got != want[r] {
			t.Fatalf("pyIsSpace(U+%04X) = %v, python str.isspace() = %v", cp, got, want[r])
		}
		c := string(r)
		if got := reWhitespace.FindString(c) == c; got != want[r] {
			t.Fatalf("reWhitespace matches U+%04X = %v, python regex backslash-s = %v", cp, got, want[r])
		}
		if got := pyStrip(c) == ""; got != want[r] {
			t.Fatalf("pyStrip strips U+%04X = %v, python str.strip() = %v", cp, got, want[r])
		}
	}
	// And each one really does separate two search terms.
	for _, r := range pythonWhitespace {
		if got := SearchTerms("ab" + string(r) + "cd"); !reflect.DeepEqual(got, []string{"ab", "cd"}) {
			t.Errorf("U+%04X did not split search terms: %v", r, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Full case folding (app.py:19101/19103/19106/19116/19130/19143 casefold())
// ---------------------------------------------------------------------------

// pythonCasefold pins str.casefold() as CPython 3.12 / Unicode 15.0.0 computes
// it. Every `want` below was PRINTED BY CPython, not reasoned out. The set is a
// representative of each class in which full folding leaves strings.ToLower
// behind: multi-rune expansions, the Greek symbol variants, the code points
// with no simple lowercase at all (µ, ſ), and Cherokee, which folds UPWARDS.
//
// REGRESSION (defect: caseFold was strings.ToLower plus a nine-entry replacer).
// A sweep of all 1,112,064 code points against CPython found 298 disagreements.
// The user-visible ones: searching "s" missed an address written with ſ,
// searching "i̇" missed "İ@x.com", and every Cherokee row sorted under the wrong
// key. Do not "simplify" caseFold back to ToLower.
var pythonCasefold = []struct{ in, want string }{
	{"a@b.c", "a@b.c"},
	{"A@B.C", "a@b.c"},
	{"中文状态", "中文状态"},
	{"PLUS", "plus"},
	{"Ｐlus", "ｐlus"},
	{"ＴＥＡＭ", "ｔｅａｍ"},
	{"ß", "ss"},
	{"ẞ", "ss"}, // U+1E9E capital sharp s
	{"ς", "σ"},  // final sigma folds to sigma; casefold has no context rule
	{"Σ", "σ"},
	{"ΟΔΟΣ", "οδοσ"}, // ... so unlike .lower() this is σ, not ς
	{"İ", "i̇"},
	{"ı", "ı"},
	{"İSTANBUL", "i̇stanbul"},
	{"ſ", "s"}, // U+017F long s: unicode.ToLower leaves it alone
	{"ſharp@x.io", "sharp@x.io"},
	{"µ", "μ"}, // U+00B5 micro sign -> U+03BC greek small mu
	{"ŉ", "ʼn"},
	{"ǰ", "ǰ"},
	{"ΐ", "ΐ"},
	{"ΰ", "ΰ"},
	{"ϐ", "β"}, {"ϑ", "θ"}, {"ϕ", "φ"}, {"ϖ", "π"},
	{"ϰ", "κ"}, {"ϱ", "ρ"}, {"ϵ", "ε"},
	{"ͅ", "ι"}, // combining ypogegrammeni
	{"և", "եւ"},
	{"ﬀ", "ff"}, {"ﬁ", "fi"}, {"ﬂ", "fl"}, {"ﬃ", "ffi"}, {"ﬄ", "ffl"},
	{"ﬅ", "st"}, {"ﬆ", "st"},
	{"ﬓ", "մն"}, {"ﬔ", "մե"}, {"ﬕ", "մի"}, {"ﬖ", "վն"}, {"ﬗ", "մխ"},
	{"ẖ", "ẖ"},
	{"ẗ", "ẗ"},
	{"ẘ", "ẘ"},
	{"ẙ", "ẙ"},
	{"ẚ", "aʾ"},
	{"ẛ", "ṡ"},
	{"ᾀ", "ἀι"},
	{"ᾈ", "ἀι"},
	{"ᾼ", "αι"},
	{"ῌ", "ηι"},
	{"ῼ", "ωι"},
	{"ᲀ", "в"}, {"ᲁ", "д"}, {"ᲈ", "ꙋ"}, // Cyrillic historic variants
	{"Ꭰ", "Ꭰ"}, {"ꭰ", "Ꭰ"}, // Cherokee folds to the UPPERCASE form
	{"Ᏸ", "Ᏸ"}, {"ᏸ", "Ᏸ"},
	{"Ᏼ", "Ᏼ"}, {"ᏼ", "Ᏼ"},
	{"𐐧", "𐑏"}, // Deseret (astral): ToLower already agrees
	{"𐐿", "𐐿"},
	{"K", "k"}, // U+212A kelvin sign
	{"Å", "å"}, // U+212B angstrom sign
	{"Ω", "ω"}, // U+2126 ohm sign
	{"ﬁle@x.io", "file@x.io"},
	{"MIXEDßſµ中文", "mixedsssμ中文"},
	{"", ""},
}

func TestCaseFoldMatchesPythonCasefold(t *testing.T) {
	for _, c := range pythonCasefold {
		if got := caseFold(c.in); got != c.want {
			t.Errorf("caseFold(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The table must stay minimal and the two mechanisms must not overlap: an entry
// that already equals unicode.ToLower is dead weight that hides a real one, and
// a Cherokee code point in pyFoldExtra would shadow the range rule. The count is
// pinned because it is a fact about Unicode 15.0.0, not a style choice — if a Go
// toolchain upgrade moves the tables, this is where it surfaces.
func TestCaseFoldTableShape(t *testing.T) {
	if len(pyFoldExtra) != 126 {
		t.Errorf("pyFoldExtra has %d entries, want 126 (Unicode 15.0.0, Cherokee excluded)", len(pyFoldExtra))
	}
	for r, mapped := range pyFoldExtra {
		if mapped == string(unicode.ToLower(r)) {
			t.Errorf("pyFoldExtra[U+%04X] = %q is what ToLower already gives; drop the row", r, mapped)
		}
		if _, ok := cherokeeFold(r); ok {
			t.Errorf("U+%04X is covered by cherokeeFold; two mechanisms for one rune", r)
		}
		if !needsFullFold(r) {
			t.Errorf("needsFullFold(U+%04X) = false, so the fast path skips the table", r)
		}
	}
	// The three Cherokee ranges, spot-checked at both ends (CPython values).
	for r, want := range map[rune]rune{
		0x13A0: 0x13A0, 0x13F5: 0x13F5, // fold is the identity here
		0x13F8: 0x13F0, 0x13FD: 0x13F5,
		0xAB70: 0x13A0, 0xABBF: 0x13EF,
	} {
		got, ok := cherokeeFold(r)
		if !ok || got != want {
			t.Errorf("cherokeeFold(U+%04X) = U+%04X,%v want U+%04X", r, got, ok, want)
		}
	}
	if _, ok := cherokeeFold('a'); ok {
		t.Error("cherokeeFold must not claim ASCII")
	}
}

// ---------------------------------------------------------------------------
// str.lower() (app.py:19025/19082/19750) — Key identity and plan_type
// ---------------------------------------------------------------------------

// REGRESSION (defect: KeyOf used strings.ToLower). app.py:19025 and 19750 call
// str.lower(), whose FULL mapping expands U+0130 to "i" + U+0307. Simple
// lowercasing collapses it to a bare "i", which gave "İ@x.com" and "i@x.com" the
// same row identity — and Key is exactly what _restore_account_selection matches
// on, so a selection re-applied after a sort would land on the other account.
func TestKeyOfUsesPythonFullLowercase(t *testing.T) {
	if got := KeyOf("  İ@X.COM  "); got != "i̇@x.com" {
		t.Errorf("KeyOf = %q, want %q", got, "i̇@x.com")
	}
	if KeyOf("İ@x.com") == KeyOf("i@x.com") {
		t.Error("İ@x.com and i@x.com must not share a row identity")
	}
	if got := KeyOf("\x1cA@B.C\x1f"); got != "a@b.c" {
		t.Errorf("KeyOf with C0 separators = %q", got)
	}
	// plan_type takes the same .lower(), not casefold: app.py:19082.
	if got := pyLower("PLUS"); got != "plus" {
		t.Errorf("pyLower(PLUS) = %q", got)
	}
}

// DELIBERATE DIVERGENCE, pinned so it is not mistaken for an oversight.
// str.lower() applies the context-sensitive Final_Sigma rule, so CPython gives
// "ΟΔΟΣ".lower() == "οδος" while pyLower gives "οδοσ" (casefold, which the
// search and sort paths use, gives "οδοσ" on BOTH sides — only .lower() differs).
// Reproducing it needs the Cased and Case_Ignorable derived properties plus
// three Word_Break classes, none of which Go exports.
//
// Nothing can observe the difference: every consumer of pyLower here compares
// one Go-produced string against another Go-produced one (Key against Key, a
// lower-cased plan_type against an ASCII literal), and no key crosses into the
// Python state file. If that ever stops being true — if a Python-written key is
// compared against one of these — this becomes a real bug.
func TestPyLowerFinalSigmaDivergence(t *testing.T) {
	const cpython = "οδος" // CPython: "ΟΔΟΣ".lower()
	if got := pyLower("ΟΔΟΣ"); got == cpython {
		t.Fatal("pyLower now implements Final_Sigma — delete this test and the DIVERGENCE note in pyvalue.go")
	} else if got != "οδοσ" {
		t.Fatalf("pyLower(ΟΔΟΣ) = %q, want %q", got, "οδοσ")
	}
	// casefold agrees with CPython here, which is what the filter/sort use.
	if got := caseFold("ΟΔΟΣ"); got != "οδοσ" {
		t.Errorf("caseFold(ΟΔΟΣ) = %q, want %q", got, "οδοσ")
	}
}

// ---------------------------------------------------------------------------
// int() (app.py:19093 max(0, int(... or 0)))
// ---------------------------------------------------------------------------

// pythonInt pins int(s) as CPython 3.12 evaluates it. `raises` marks the inputs
// where CPython throws ValueError; app.py:19093 has no handler, so pyInt answers
// 0 there rather than taking the render pass down with it.
var pythonInt = []struct {
	in     string
	want   int
	raises bool
}{
	{in: "", raises: true},
	{in: " ", raises: true},
	{in: "0", want: 0},
	{in: "7", want: 7},
	{in: " 7 ", want: 7},
	{in: "+9", want: 9},
	{in: "-9", want: -9},
	// Underscores are legal digit separators between digits, and only there.
	{in: "1_0", want: 10},
	{in: "1_0_0", want: 100},
	{in: "_10", raises: true},
	{in: "10_", raises: true},
	{in: "1__0", raises: true},
	{in: "1_", raises: true},
	// ... including with non-ASCII decimal digits.
	{in: "٩", want: 9},
	{in: "٩_٩", want: 99},
	{in: "０７", want: 7},
	{in: "１_０", want: 10},
	{in: "٠٠٧", want: 7},
	{in: "\U0001D7DD", want: 5}, // MATHEMATICAL DOUBLE-STRUCK DIGIT FIVE
	{in: "abc", raises: true},
	{in: "3.9", raises: true},
	{in: "- 5", raises: true},
	{in: "12345678901234", want: 12345678901234},
	// int()'s padding set is str.isspace() MINUS U+001C..U+001F, even though
	// str.strip() would have removed them — so this raises where "\x1c8\x1f"
	// .strip() would have yielded "8".
	{in: "\x1c8\x1f", raises: true},
	{in: " 1 ", want: 1},
	{in: "　1　", want: 1},
	{in: "\v9\v", want: 9},
	{in: "1​0", raises: true}, // ZWSP is not whitespace to Python
	{in: "²", raises: true},   // isdigit() but not a decimal digit
	{in: "½", raises: true},
}

func TestPyIntMatchesPythonInt(t *testing.T) {
	for _, c := range pythonInt {
		want := c.want
		if c.raises {
			want = 0
		}
		if got := pyInt(c.in); got != want {
			t.Errorf("pyInt(%q) = %d, want %d (python: %s)", c.in, got, want,
				map[bool]string{true: "ValueError", false: "value"}[c.raises])
		}
	}
	// AttemptCount is int() behind max(0, ...), so negatives clamp.
	lk := Lookups{LinkAttempts: map[string]any{"a@x.io": "1_0", "b@x.io": "-9"}}
	if got := lk.AttemptCount("a@x.io"); got != 10 {
		t.Errorf(`AttemptCount("1_0") = %d, want 10`, got)
	}
	if got := lk.AttemptCount("b@x.io"); got != 0 {
		t.Errorf(`AttemptCount("-9") = %d, want 0`, got)
	}
}

// REGRESSION (defect: pyDigitInt stopped accumulating past 1e9, so
// int("12345678901234") answered 1234567890). Python ints are unbounded and
// Go's are not, so an overflow has to go SOMEWHERE; it goes to math.MaxInt,
// which at least keeps the ordering of the attempts column monotone. It cannot
// affect HasK12Success: anything that overflows is far above 300.
func TestPyDigitIntSaturatesInsteadOfTruncating(t *testing.T) {
	if got, ok := pyDigitInt("12345678901234"); !ok || got != 12345678901234 {
		t.Errorf("pyDigitInt = %d,%v want 12345678901234", got, ok)
	}
	got, ok := pyDigitInt("9999999999999999999999999") // CPython: exact, 25 digits
	if !ok || got != math.MaxInt {
		t.Errorf("pyDigitInt(25 nines) = %d,%v want MaxInt", got, ok)
	}
	lk := Lookups{SessionResults: map[string]any{
		"a@x.io": map[string]any{"k12_status": "9999999999999999999999999"},
	}}
	if lk.HasK12Success("a@x.io") {
		t.Error("a saturated status code must not read as 2xx")
	}
}

// DELIBERATE DIVERGENCE, and the only one a differential sweep of 4,284 random
// account tables still finds. encoding/json decodes every JSON number to
// float64, which erases Python's int/float distinction: CPython's str(200) is
// "200" (isdigit, a 2xx) but str(200.0) is "200.0" (not isdigit, rejected), and
// Go cannot tell the two apart once they are both float64(200).
//
// pyStr resolves the ambiguity towards int, because that is the only shape that
// occurs: every writer of k12_status stores a str (app.py:18665, 18766, 20803),
// and a Go worker that puts a raw code in the map puts an int. Decode the state
// file with json.Decoder.UseNumber and the json.Number branch makes it exact —
// see the note on pyStr; that decoder lives outside this package.
func TestK12StatusIntFloatAmbiguity(t *testing.T) {
	intish := Lookups{SessionResults: map[string]any{"a@x.io": map[string]any{"k12_status": float64(200)}}}
	if !intish.HasK12Success("a@x.io") {
		t.Error("an integral float must read as the int CPython would have had")
	}
	// A genuinely fractional code is rejected on both sides (str(200.7) is not
	// a digit string), so only the integral case is ambiguous at all.
	frac := Lookups{SessionResults: map[string]any{"a@x.io": map[string]any{"k12_status": 200.7}}}
	if frac.HasK12Success("a@x.io") {
		t.Error("200.7 is not a digit string in CPython either")
	}
	// With UseNumber the distinction survives and both answers are CPython's.
	exact := Lookups{SessionResults: map[string]any{
		"i@x.io": map[string]any{"k12_status": json.Number("200")},
		"f@x.io": map[string]any{"k12_status": json.Number("200.0")},
	}}
	if !exact.HasK12Success("i@x.io") {
		t.Error(`json.Number("200") must be a 2xx`)
	}
	if exact.HasK12Success("f@x.io") {
		t.Error(`json.Number("200.0") is str "200.0", which is not isdigit`)
	}
}

// ---------------------------------------------------------------------------
// The fold reaching the filter/sort it exists for
// ---------------------------------------------------------------------------

// End to end: each pair below is one account and one search term that CPython's
// _account_visible_indices (app.py:19108) matches — verified by running the
// verbatim body — and that a ToLower-only fold would have missed.
func TestSearchUsesFullCaseFolding(t *testing.T) {
	cases := []struct{ search, email, group string }{
		{"ss", "a@b.c", "ß"},
		{"ß", "a@b.c", "SS"},
		{"s", "ſharp@x.io", ""},
		{"i̇", "İ@x.com", ""},
		{"ꭰ", "Ꭰ@x.io", ""},
		{"Ꭰ", "ꭰ@x.io", ""},
		{"μ", "µ@x.io", ""},
		{"fi", "ﬁle@x.io", ""},
	}
	for _, c := range cases {
		acc := models.MailAccount{Email: c.email, AccountType: "free", Group: c.group}
		f := Filter{Group: GroupAll, Search: c.search}
		if !Matches(acc, f, Lookups{}) {
			t.Errorf("search %q must match email=%q group=%q", c.search, c.email, c.group)
		}
	}
}

// The sort key is the folded string, so the two spellings of one address collide
// on the key while staying distinct rows. Values are CPython's casefold().
func TestSortKeyUsesFullCaseFolding(t *testing.T) {
	cases := []struct{ email, want string }{
		{"ſharp@x.io", "sharp@x.io"},
		{"sharp@x.io", "sharp@x.io"},
		{"İ@x.com", "i̇@x.com"},
		{"Ꭰ@x.io", "Ꭰ@x.io"},
		{"ꭰ@x.io", "Ꭰ@x.io"},
		{"ẞ@x.io", "ss@x.io"},
	}
	for _, c := range cases {
		if got := SortKeyOf(models.MailAccount{Email: c.email}, ColumnEmail, Lookups{}); got.Text != c.want {
			t.Errorf("SortKeyOf(%q).Text = %q, want %q", c.email, got.Text, c.want)
		}
	}
}
