package alias

import (
	"errors"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// stubRandInt makes GeneratePlusAliases deterministic by replaying values in
// order; once exhausted it returns lo, which always collides with an existing
// alias in these tests.
func stubRandInt(t *testing.T, values ...int) {
	t.Helper()
	prev := plusAliasRandInt
	i := 0
	plusAliasRandInt = func(lo, hi int) int {
		if i < len(values) {
			v := values[i]
			i++
			return v
		}
		return lo
	}
	t.Cleanup(func() { plusAliasRandInt = prev })
}

func acct(email, group string) models.MailAccount {
	return models.MailAccount{
		Email:        email,
		Password:     "pw",
		ClientID:     "cid",
		RefreshToken: "rt",
		Group:        group,
	}
}

// The four reserved names UI_SPEC §5.4 says are missing, plus the cap it lists
// as UNKNOWN. Guards against a typo in a CJK literal that no compiler catches.
func TestReservedConstants(t *testing.T) {
	if MaxPlusAliasesPerMailbox != 4 {
		t.Fatalf("MAX_PLUS_ALIASES_PER_MAILBOX = %d, app.py:295 says 4", MaxPlusAliasesPerMailbox)
	}
	pairs := [][2]string{
		{AccountEmailLockedGroup, "邮箱锁定"},
		{AccountEmailLockedStatus, "邮箱锁定"},
		{AccountDomainMailMainGroup, "域名邮箱主"},
		{AccountDomainMailChildGroup, "域名邮箱分"},
		{PlusAliasPendingStatus, "别名待注册"},
		{DomainAliasPendingStatus, "域名邮箱待注册"},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("constant %q != app.py literal %q", p[0], p[1])
		}
	}
}

func TestMailboxEmailForPlusAlias(t *testing.T) {
	cases := [][2]string{
		{"user+12@example.com", "user@example.com"},
		{"user@example.com", "user@example.com"},
		{"  <User+A+B@Example.com> ", "User@Example.com"}, // first "+" only, case kept
		{"nonsense", "nonsense"},                          // no "@" -> unchanged
	}
	for _, c := range cases {
		if got := MailboxEmailForPlusAlias(c[0]); got != c[1] {
			t.Errorf("MailboxEmailForPlusAlias(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	if IsPlusAliasEmail("nonsense") {
		t.Error("an address without @ is not a plus alias")
	}
	if !IsPlusAliasEmail("user+1@example.com") || IsPlusAliasEmail("user@exam+ple.com") {
		t.Error("the + must be in the local part only")
	}
}

// Python's \D is Unicode-aware; Go's RE2 \D is ASCII-only. The port must keep
// non-ASCII decimal digits. app.py:1732.
func TestPlusAliasEmailUnicodeDigits(t *testing.T) {
	got, err := PlusAliasEmail("user@example.com", "٣٤٥") // ٣٤٥
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "user+٣٤٥@example.com" {
		t.Fatalf("Unicode digits were stripped: %q", got)
	}
	if got, err := PlusAliasEmail("user+99@example.com", "a1b2"); err != nil || got != "user+12@example.com" {
		t.Fatalf("PlusAliasEmail replace-tag = %q, %v", got, err)
	}
	if _, err := PlusAliasEmail("user@example.com", "abc"); !errors.Is(err, ErrPlusAliasNoDigits) {
		t.Fatalf("want ErrPlusAliasNoDigits, got %v", err)
	}
	if _, err := PlusAliasEmail("nonsense", "12"); !errors.Is(err, ErrPlusAliasBadEmail) {
		t.Fatalf("want ErrPlusAliasBadEmail, got %v", err)
	}
}

func TestCloneAccountForPlusAlias(t *testing.T) {
	mother := models.MailAccount{
		Email: "user@example.com", Password: "pw", ClientID: "cid", RefreshToken: "rt",
		AccountType: "plus", Status: "成功", ReceiveMailbox: "box@outlook.com", Group: "",
	}
	got := CloneAccountForPlusAlias(mother, " user+12@example.com ")
	if got.Email != "user+12@example.com" {
		t.Errorf("alias email not normalized: %q", got.Email)
	}
	if got.Raw != "user+12@example.com----pw----cid----rt" {
		t.Errorf("raw = %q", got.Raw)
	}
	if got.AccountType != "free" || got.Status != PlusAliasPendingStatus {
		t.Errorf("type/status not reset: %q/%q", got.AccountType, got.Status)
	}
	if got.ReceiveMailbox != "box@outlook.com" {
		t.Errorf("receive mailbox not inherited: %q", got.ReceiveMailbox)
	}
	if got.Group != models.AccountDefaultGroup {
		t.Errorf("empty group must fall back to 未分组, got %q", got.Group)
	}
	// Python's `or` is falsiness, not TrimSpace: a whitespace group is truthy
	// and survives. app.py:1749.
	mother.Group = " "
	if got := CloneAccountForPlusAlias(mother, "user+13@example.com"); got.Group != " " {
		t.Errorf("whitespace group must be kept verbatim, got %q", got.Group)
	}
}

func TestGeneratePlusAliasesCap(t *testing.T) {
	accounts := []models.MailAccount{
		acct("user@example.com", "g"),
		acct("USER+1@example.com", "g"), // counted case-insensitively
		acct("user+2@example.com", "g"),
		acct("user+3@example.com", "g"),
	}
	stubRandInt(t, 777)
	created, errs := GeneratePlusAliases(accounts, []models.MailAccount{accounts[0]}, 2)
	if len(created) != 1 || created[0].Email != "user+777@example.com" {
		t.Fatalf("created = %+v", created)
	}
	if len(errs) != 1 || errs[0] != "user@example.com: 最多 4 个，本次只生成 1 个" {
		t.Fatalf("errs = %q", errs)
	}
	if created[0].Group != "g" {
		t.Errorf("alias must inherit the mother group, got %q", created[0].Group)
	}

	// A fourth existing alias exhausts the cap entirely.
	full := append(accounts, acct("user+4@example.com", "g"))
	created, errs = GeneratePlusAliases(full, []models.MailAccount{full[0]}, 1)
	if len(created) != 0 {
		t.Fatalf("cap breached: %+v", created)
	}
	if len(errs) != 1 || errs[0] != "user@example.com: 已有 4 个别名，跳过" {
		t.Fatalf("errs = %q", errs)
	}

	// Skip lines name the MOTHER mailbox, not the selected +alias row, and
	// mailbox_email_for_plus_alias preserves the selected row's casing —
	// only the lookup key is folded. app.py:1718/14765.
	created, errs = GeneratePlusAliases(full, []models.MailAccount{full[1]}, 1)
	if len(created) != 0 || len(errs) != 1 || !strings.HasPrefix(errs[0], "USER@example.com: ") {
		t.Fatalf("created=%+v errs=%q", created, errs)
	}
}

func TestGeneratePlusAliasesMissingCredentials(t *testing.T) {
	mother := models.MailAccount{Email: "user@example.com", Password: "pw", ClientID: "cid"}
	created, errs := GeneratePlusAliases([]models.MailAccount{mother}, []models.MailAccount{mother}, 1)
	if len(created) != 0 || len(errs) != 1 || errs[0] != "user@example.com: 缺少 client_id/refresh_token" {
		t.Fatalf("created=%+v errs=%q", created, errs)
	}
	if got, _ := GeneratePlusAliases(nil, []models.MailAccount{mother}, 0); got != nil {
		t.Error("count<=0 is Python's `if not count: return`")
	}
}

// After 80 collisions at 3 digits the generator must widen to 4. app.py:14772.
func TestGeneratePlusAliasesWidensSuffix(t *testing.T) {
	accounts := []models.MailAccount{
		acct("user@example.com", ""),
		acct("user+100@example.com", ""),
	}
	// The stub returns lo for every call: 100 at width 3 (always taken), then
	// 1000 at width 4 (free).
	stubRandInt(t)
	created, errs := GeneratePlusAliases(accounts, []models.MailAccount{accounts[0]}, 1)
	if len(errs) != 0 {
		t.Fatalf("errs = %q", errs)
	}
	if len(created) != 1 || created[0].Email != "user+1000@example.com" {
		t.Fatalf("created = %+v", created)
	}
}

// A malformed mother address yields one error per suffix width plus the
// duplicate line — Python breaks only the inner loop. app.py:14779-14788.
func TestGeneratePlusAliasesBadEmailErrorFanout(t *testing.T) {
	bad := models.MailAccount{Email: "nonsense", Password: "pw", ClientID: "cid", RefreshToken: "rt"}
	created, errs := GeneratePlusAliases([]models.MailAccount{bad}, []models.MailAccount{bad}, 1)
	if len(created) != 0 {
		t.Fatalf("created = %+v", created)
	}
	if len(errs) != 5 {
		t.Fatalf("want 4 format errors + 1 duplicate line, got %d: %q", len(errs), errs)
	}
	if errs[4] != "nonsense: 随机别名重复过多，未生成" {
		t.Errorf("last error = %q", errs[4])
	}
}

func TestGeneratePlusAliasesUniqueWithinRun(t *testing.T) {
	mother := acct("user@example.com", "")
	stubRandInt(t, 111, 111, 222)
	created, errs := GeneratePlusAliases([]models.MailAccount{mother}, []models.MailAccount{mother}, 2)
	if len(errs) != 0 || len(created) != 2 {
		t.Fatalf("created=%+v errs=%q", created, errs)
	}
	if created[0].Email == created[1].Email {
		t.Fatalf("duplicate alias produced: %q", created[0].Email)
	}
}

func TestIsAccountEmailLocked(t *testing.T) {
	accounts := []models.MailAccount{
		{Email: "user@example.com", Status: "成功"},
		{Email: "USER+7@Example.com ", Status: AccountEmailLockedStatus},
		{Email: "other@example.com", Status: "成功"},
	}
	if !IsAccountEmailLocked(accounts, accounts[0]) {
		t.Error("a locked +alias must lock the mother mailbox")
	}
	if IsAccountEmailLocked(accounts, accounts[2]) {
		t.Error("an unrelated mailbox must not be locked")
	}
	if IsAccountEmailLocked(accounts, models.MailAccount{Email: "   "}) {
		t.Error("an empty mailbox key returns false (app.py:19827)")
	}
	// Status comparison is exact, not case/space-insensitive (app.py:19830).
	accounts[1].Status = " 邮箱锁定"
	if IsAccountEmailLocked(accounts, accounts[0]) {
		t.Error("status must be compared with ==")
	}
}
