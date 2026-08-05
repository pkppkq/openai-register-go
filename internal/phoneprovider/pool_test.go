package phoneprovider

import (
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestReserveNextWalkOrderAndFreezing(t *testing.T) {
	phones := []*models.PhoneEntry{
		{Number: "+819011112222", Status: StatusAvailable},                 // not +1
		{Number: "+15550000001", Status: StatusUnusable},                   // burned
		{Number: "+15550000002", Status: StatusAvailable, ReceiveCount: 2}, // at the cap
		{Number: "+15550000003", Status: StatusInUse},                      // taken
		{Number: "+15550000004", SMSURL: "u4", Status: StatusAvailable},    // <- winner
		{Number: "+15550000005", SMSURL: "u5", Status: StatusAvailable},    // untouched
	}
	account := &models.MailAccount{Email: "A@B.c"}
	events := 0
	accountEvents := 0
	pool := NewMemoryPool([]*models.MailAccount{account}, phones)
	pool.OnPhonesUpdated = func() { events++ }
	pool.OnAccountUpdated = func(string) { accountEvents++ }

	got, ok := pool.ReserveNext("a@b.c", "US", 2)
	if !ok {
		t.Fatal("expected a reservation")
	}
	if got.Number != "+15550000004" {
		t.Fatalf("reserved %q, want +15550000004", got.Number)
	}
	if phones[2].Status != StatusFrozen {
		t.Fatalf("an over-quota entry must be re-stamped 冻结, got %q", phones[2].Status)
	}
	if phones[4].Status != StatusInUse {
		t.Fatalf("the winner must be marked 使用中, got %q", phones[4].Status)
	}
	if phones[5].Status != StatusAvailable {
		t.Fatalf("the walk must stop at the first usable entry")
	}
	if account.AuthPhoneNumber != "+15550000004" || account.AuthPhoneSMSURL != "u4" {
		t.Fatalf("account binding missing: %+v", account)
	}
	// One freeze + one reservation.
	if events != 2 || accountEvents != 1 {
		t.Fatalf("events = %d phones / %d account, want 2 / 1", events, accountEvents)
	}
}

func TestReserveNextEmitsOneAtomicStateUpdate(t *testing.T) {
	phones := []*models.PhoneEntry{
		{Number: "+15550000001", Status: StatusAvailable, ReceiveCount: 1},
		{Number: "+15550000002", SMSURL: "https://sms.test/2", Status: StatusAvailable},
	}
	account := &models.MailAccount{Email: "User@example.com"}
	pool := NewMemoryPool([]*models.MailAccount{account}, phones)

	var updates []PoolUpdate
	pool.OnStateUpdated = func(update PoolUpdate) {
		updates = append(updates, update)
	}

	got, ok := pool.ReserveNext("user@example.com", "US", 1)
	if !ok || got.Number != "+15550000002" {
		t.Fatalf("ReserveNext = %+v, %v", got, ok)
	}
	if len(updates) != 1 {
		t.Fatalf("组合持久化回调次数=%d，期望一次", len(updates))
	}
	update := updates[0]
	if len(update.Phones) != 2 ||
		update.Phones[0].Status != StatusFrozen ||
		update.Phones[1].Status != StatusInUse {
		t.Fatalf("号码池事务快照不完整: %+v", update.Phones)
	}
	if len(update.Accounts) != 1 ||
		update.Accounts[0].Email != "User@example.com" ||
		update.Accounts[0].Number != "+15550000002" ||
		update.Accounts[0].SMSURL != "https://sms.test/2" {
		t.Fatalf("账号授权手机号快照不完整: %+v", update.Accounts)
	}

	// 回调收到的必须是副本；后续变更不能倒改已经排队的持久化数据。
	pool.RecordCode("+15550000002", "654321", 2)
	if update.Phones[1].LastCode != "" || update.Phones[1].ReceiveCount != 0 {
		t.Fatalf("历史回调快照被后续变更污染: %+v", update.Phones[1])
	}
}

// TestReserveNextFreezeOverridesUnusable pins a non-obvious ordering: the freeze
// check runs BEFORE the status filter, so an already-不可用 entry that is over
// quota gets re-labelled 冻结 (app.py:16566-16570).
func TestReserveNextFreezeOverridesUnusable(t *testing.T) {
	phone := &models.PhoneEntry{Number: "+15550000001", Status: StatusUnusable, ReceiveCount: 5}
	pool := NewMemoryPool(nil, []*models.PhoneEntry{phone})

	if _, ok := pool.ReserveNext("a@b.c", "US", 3); ok {
		t.Fatal("nothing is reservable here")
	}
	if phone.Status != StatusFrozen {
		t.Fatalf("status = %q, want 冻结", phone.Status)
	}
}

func TestReserveNextIgnoresCountryFilterWhenNotUS(t *testing.T) {
	phone := &models.PhoneEntry{Number: "+819011112222", Status: StatusAvailable}
	pool := NewMemoryPool(nil, []*models.PhoneEntry{phone})

	got, ok := pool.ReserveNext("a@b.c", "", 0)
	if !ok || got.Number != "+819011112222" {
		t.Fatalf("a non-US request must accept any number, got %v / %v", got, ok)
	}
}

func TestAccountAuthPhoneLookup(t *testing.T) {
	saved := &models.PhoneEntry{Number: "+15550000009", Status: StatusUnusable, LastError: "手机号已使用: x"}
	accounts := []*models.MailAccount{
		{Email: "other@b.c", AuthPhoneNumber: "+1999", AuthPhoneSMSURL: "u"},
		{Email: "A@B.c", AuthPhoneNumber: "+15550000009", AuthPhoneSMSURL: "u9"},
	}
	pool := NewMemoryPool(accounts, []*models.PhoneEntry{saved})

	got := pool.AccountAuthPhone("a@b.c")
	if !got.Found || got.Number != "+15550000009" || got.SMSURL != "u9" {
		t.Fatalf("lookup = %+v", got)
	}
	if !got.SavedOK || got.Saved.Status != StatusUnusable {
		t.Fatalf("saved pool entry not reported: %+v", got)
	}

	// An account with a number but no sms_url is treated as "no auth phone"
	// (app.py:16542 requires both).
	pool2 := NewMemoryPool([]*models.MailAccount{{Email: "a@b.c", AuthPhoneNumber: "+1555"}}, nil)
	if got := pool2.AccountAuthPhone("a@b.c"); got.Found {
		t.Fatalf("expected Found=false, got %+v", got)
	}

	// No matching account at all.
	if got := pool2.AccountAuthPhone("zz@b.c"); got.Found {
		t.Fatalf("expected Found=false, got %+v", got)
	}
}

func TestMarkUnusableAccountBoundCreatesEntry(t *testing.T) {
	pool := NewMemoryPool(nil, nil)
	pool.MarkUnusable("+15550000001", "u1", "手机号已使用", "already used", true)

	entries := pool.Phones()
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want one appended", entries)
	}
	got := entries[0]
	if got.Number != "+15550000001" || got.SMSURL != "u1" || got.Status != StatusUnusable {
		t.Fatalf("entry = %+v", got)
	}
	if got.LastError != "手机号已使用: already used" {
		t.Fatalf("last_error = %q", got.LastError)
	}
}

func TestMarkUnusableKeepsExistingSMSURL(t *testing.T) {
	phone := &models.PhoneEntry{Number: "+1555", SMSURL: "original", Status: StatusAvailable}
	pool := NewMemoryPool(nil, []*models.PhoneEntry{phone})
	pool.MarkUnusable("+1555", "replacement", "", "boom", true)

	if phone.SMSURL != "original" {
		t.Fatalf("sms_url = %q, want the original (Python only back-fills an empty one)", phone.SMSURL)
	}
	// An empty status means the error text is stored bare, with no "status: " prefix.
	if phone.LastError != "boom" {
		t.Fatalf("last_error = %q, want %q", phone.LastError, "boom")
	}
}

func TestMarkUnusableWithoutCreateIgnoresUnknownNumber(t *testing.T) {
	pool := NewMemoryPool(nil, nil)
	pool.MarkUnusable("+1555", "u", "手机号不可用", "boom", false)
	if entries := pool.Phones(); len(entries) != 0 {
		t.Fatalf("nothing should be appended for a plain pool number, got %v", entries)
	}
}

func TestBadRoutesToTheRightPoolBranch(t *testing.T) {
	phone := &models.PhoneEntry{Number: "+1555", SMSURL: "u", Status: StatusInUse}
	h := newHarness(t, Raw{}, nil, nil, []*models.PhoneEntry{phone})

	if err := h.provider.Bad("a@b.c", map[string]string{"number": "+1555", "error": "boom"}); err != nil {
		t.Fatal(err)
	}
	if phone.Status != StatusUnusable {
		t.Fatalf("status = %q, want 不可用", phone.Status)
	}
	// The default status is applied when the payload carries none (app.py:16605).
	if phone.LastError != "手机号不可用: boom" {
		t.Fatalf("last_error = %q", phone.LastError)
	}
}

func TestBadAccountBoundLogsAndCreates(t *testing.T) {
	h := newHarness(t, Raw{}, nil, nil, nil)
	err := h.provider.Bad("a@b.c", map[string]string{
		"number":        "+1555",
		"sms_url":       "u",
		"account_bound": "true",
		"status":        "手机号已使用",
		"error":         "already used",
	})
	if err != nil {
		t.Fatal(err)
	}
	if entries := h.pool.Phones(); len(entries) != 1 || entries[0].Status != StatusUnusable {
		t.Fatalf("entries = %v", entries)
	}
	if !h.log.contains("导入授权手机号已标记不可用 [手机号已使用]: +1555 already used") {
		t.Fatalf("missing log:\n%s", h.log.dump())
	}
}

// TestAccountBoundTruthiness pins Python's `bool(payload.get("account_bound"))`:
// any non-empty string is true, including "false".
func TestAccountBoundTruthiness(t *testing.T) {
	cases := map[string]bool{"": false, "true": true, "false": true, "0": true}
	for value, want := range cases {
		if got := accountBound(map[string]string{"account_bound": value}); got != want {
			t.Errorf("accountBound(%q) = %v, want %v", value, got, want)
		}
	}
	if accountBound(map[string]string{}) {
		t.Error("a missing key is false")
	}
}
