package phoneprovider

import (
	"strings"
	"sync"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// Phone pool statuses, spelled exactly as the Python literals.
const (
	StatusAvailable  = "可用"
	StatusUnusable   = "不可用"
	StatusFrozen     = "冻结"
	StatusInUse      = "使用中"
	defaultBadStatus = "手机号不可用" // app.py:16605 `payload.get("status") or "手机号不可用"`
)

// blockedStatuses is the `{"不可用", "冻结", "使用中"}` set used both for the
// imported auth phone (app.py:16549) and the manual pool walk (app.py:16571).
func blockedStatus(status string) bool {
	switch status {
	case StatusUnusable, StatusFrozen, StatusInUse:
		return true
	}
	return false
}

// AuthPhoneLookup is the outcome of the account scan at app.py:16539-16558. It
// is returned as one value so the whole lookup happens under a single lock hold,
// like the Python `with self.phone_lock:` block that covered the account scan
// AND the self.phones scan for the saved entry.
type AuthPhoneLookup struct {
	// Found is true only when a matching account exists AND has both
	// auth_phone_number and auth_phone_sms_url (app.py:16542).
	Found  bool
	Number string
	SMSURL string
	// Saved is the pool entry for Number, when one exists (app.py:16545-16548).
	Saved   models.PhoneEntry
	SavedOK bool
}

// AccountPhoneUpdate 是一次号码池变更附带的账号授权手机号写回。
//
// 这里只携带号码池真正拥有的两个字段，避免 UI 持久化回调用一份较早的账号
// 快照覆盖状态、分组、RT 或浏览器指纹等并发变化。
type AccountPhoneUpdate struct {
	Email  string
	Number string
	SMSURL string
}

// PoolUpdate 是一个公开变更方法完成后的稳定快照。
//
// ReserveNext 可能在一次遍历里先冻结多个超限号码，再占用一个号码并绑定
// 账号；这些变化必须作为一个事务保存，否则另一任务或进程可能观察到只有
// 一半完成的状态。Phones 因此始终是完整副本，Accounts 只包含本次改变的
// auth_phone 字段。
type PoolUpdate struct {
	Phones   []models.PhoneEntry
	Accounts []AccountPhoneUpdate
}

// Pool is the shared account/phone state that _phone_provider mutated directly
// under self.phone_lock. It is an interface so the provider can be tested with
// no UI and no network, and so the UI can back it with its own store.
type Pool interface {
	// AccountAuthPhone performs the imported-auth-phone lookup (app.py:16539-16558).
	AccountAuthPhone(email string) AuthPhoneLookup

	// ReserveNext walks the pool in order and takes the first usable entry
	// (app.py:16562-16582): frozen entries are re-stamped 冻结 and skipped,
	// 不可用/冻结/使用中 are skipped, the winner is marked 使用中 and written back
	// onto the account as its auth phone.
	ReserveNext(email, requestedCountry string, receiveLimit int) (models.PhoneEntry, bool)

	// MarkUnusable is the "bad" bookkeeping. createMissing selects the
	// account-bound branch (app.py:16615-16628), which appends a pool entry when
	// the number is unknown and back-fills an empty sms_url; without it only an
	// existing entry is touched (app.py:16629-16635).
	MarkUnusable(number, smsURL, status, errText string, createMissing bool)

	// RecordCode is the success bookkeeping of _wait_for_phone_code
	// (app.py:16651-16659): bump receive_count, then freeze or release.
	RecordCode(number, code string, receiveLimit int)
}

// MemoryPool is the in-process Pool backed by the same slices the Python app
// held (self.accounts / self.phones).
//
// It stores POINTERS because Python mutated the shared MailAccount/PhoneEntry
// objects in place — binding a number to an account has to be visible to
// everything else holding that account.
type MemoryPool struct {
	mu       sync.Mutex
	accounts []*models.MailAccount
	phones   []*models.PhoneEntry

	// OnPhonesUpdated / OnAccountUpdated replace `self.events.put(("phones-updated",))`
	// and `("account-updated", email)`. Python queued them INSIDE the lock, so
	// event order always matches mutation order; they are invoked here under the
	// lock for the same reason and must therefore be non-blocking and must not
	// call back into the pool.
	OnPhonesUpdated  func()
	OnAccountUpdated func(email string)

	// OnStateUpdated 是持久化回调。它与上面两个细粒度 UI 事件不同：每个
	// 会修改状态的公开方法执行完毕后只触发一次，并收到无需回读池的副本。
	//
	// 回调在 mu 内同步执行，后一次占用不能越过前一次磁盘写入并恢复旧的
	// “可用”状态。回调可以进行有界持久化，但绝不能调用 MemoryPool 方法。
	OnStateUpdated func(PoolUpdate)
}

func NewMemoryPool(accounts []*models.MailAccount, phones []*models.PhoneEntry) *MemoryPool {
	return &MemoryPool{accounts: accounts, phones: phones}
}

var _ Pool = (*MemoryPool)(nil)

// Phones returns a snapshot copy for rendering.
func (m *MemoryPool) Phones() []models.PhoneEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.PhoneEntry, 0, len(m.phones))
	for _, phone := range m.phones {
		out = append(out, *phone)
	}
	return out
}

func (m *MemoryPool) phonesUpdated() {
	if m.OnPhonesUpdated != nil {
		m.OnPhonesUpdated()
	}
}

func (m *MemoryPool) accountUpdated(email string) {
	if m.OnAccountUpdated != nil {
		m.OnAccountUpdated(email)
	}
}

func (m *MemoryPool) stateUpdated(accountEmails ...string) {
	if m.OnStateUpdated == nil {
		return
	}
	update := PoolUpdate{
		Phones:   make([]models.PhoneEntry, 0, len(m.phones)),
		Accounts: make([]AccountPhoneUpdate, 0, len(accountEmails)),
	}
	for _, phone := range m.phones {
		if phone != nil {
			update.Phones = append(update.Phones, *phone)
		}
	}
	seen := make(map[string]bool, len(accountEmails))
	for _, email := range accountEmails {
		key := strings.ToLower(strings.TrimSpace(email))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		for _, account := range m.accounts {
			if account == nil || !sameEmail(account.Email, email) {
				continue
			}
			update.Accounts = append(update.Accounts, AccountPhoneUpdate{
				Email:  account.Email,
				Number: account.AuthPhoneNumber,
				SMSURL: account.AuthPhoneSMSURL,
			})
			break
		}
	}
	m.OnStateUpdated(update)
}

// Refresh 用新读取的持久化快照替换内存视图。
//
// loader 在 mu 持有期间运行。这个顺序是刻意的：若先读取 state 再加锁，
// 任务 B 可以先读到“可用”，任务 A 随后保存“使用中”，最后 B 又安装旧的
// “可用”副本。先加锁可让刷新和所有占用形成全序，因此 loader 不能回调
// 此 MemoryPool。
func (m *MemoryPool) Refresh(loader func() ([]*models.MailAccount, []*models.PhoneEntry, error)) error {
	if loader == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	accounts, phones, err := loader()
	if err != nil {
		return err
	}
	m.accounts = accounts
	m.phones = phones
	return nil
}

// sameEmail is Python's `account.email.lower() != email_addr.lower()`
// (app.py:16540, 16576). Note it is .lower(), NOT the .casefold() used for the
// SMSBower attempt-counter key (app.py:16420) — the two keys are intentionally
// computed differently in the original, so they are kept different here.
func sameEmail(a, b string) bool {
	return strings.ToLower(a) == strings.ToLower(b)
}

func (m *MemoryPool) AccountAuthPhone(email string) AuthPhoneLookup {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, account := range m.accounts {
		if !sameEmail(account.Email, email) {
			continue
		}
		// Python `break`s after the first matching account no matter what, so a
		// second account with the same address is never consulted.
		if account.AuthPhoneNumber == "" || account.AuthPhoneSMSURL == "" {
			return AuthPhoneLookup{}
		}
		out := AuthPhoneLookup{Found: true, Number: account.AuthPhoneNumber, SMSURL: account.AuthPhoneSMSURL}
		for _, phone := range m.phones {
			if phone.Number == account.AuthPhoneNumber {
				out.Saved = *phone
				out.SavedOK = true
				break
			}
		}
		return out
	}
	return AuthPhoneLookup{}
}

func (m *MemoryPool) ReserveNext(email, requestedCountry string, receiveLimit int) (models.PhoneEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	phonesChanged := false
	for _, phone := range m.phones {
		if requestedCountry == "US" && !strings.HasPrefix(phone.Number, "+1") {
			continue
		}
		// The freeze check runs BEFORE the status check, so an over-quota entry is
		// re-stamped 冻结 even if it was 不可用 (app.py:16566-16570).
		if IsFrozen(*phone, receiveLimit) {
			if phone.Status != StatusFrozen {
				phone.Status = StatusFrozen
				phonesChanged = true
				m.phonesUpdated()
			}
			continue
		}
		if blockedStatus(phone.Status) {
			continue
		}
		phone.Status = StatusInUse
		phonesChanged = true
		m.phonesUpdated()
		accountChanged := false
		for _, account := range m.accounts {
			if sameEmail(account.Email, email) {
				account.AuthPhoneNumber = phone.Number
				account.AuthPhoneSMSURL = phone.SMSURL
				accountChanged = true
				m.accountUpdated(email)
				break
			}
		}
		if accountChanged {
			m.stateUpdated(email)
		} else {
			m.stateUpdated()
		}
		return *phone, true
	}
	if phonesChanged {
		m.stateUpdated()
	}
	return models.PhoneEntry{}, false
}

func (m *MemoryPool) MarkUnusable(number, smsURL, status, errText string, createMissing bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var target *models.PhoneEntry
	for _, phone := range m.phones {
		if phone.Number == number {
			target = phone
			break
		}
	}

	if createMissing {
		if target == nil {
			target = &models.PhoneEntry{Number: number, SMSURL: smsURL}
			m.phones = append(m.phones, target)
		} else if smsURL != "" && target.SMSURL == "" {
			// elif, not a second if: an existing entry with an sms_url keeps it
			// (app.py:16622-16623).
			target.SMSURL = smsURL
		}
	} else if target == nil {
		// Non-account-bound "bad" for an unknown number is a no-op (app.py:16630-16635).
		return
	}

	target.Status = StatusUnusable
	target.LastError = lastErrorText(status, errText)
	m.phonesUpdated()
	m.stateUpdated()
}

// lastErrorText is `f"{status}: {error}" if status else error` (app.py:16625).
func lastErrorText(status, errText string) string {
	if status != "" {
		return status + ": " + errText
	}
	return errText
}

func (m *MemoryPool) RecordCode(number, code string, receiveLimit int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, phone := range m.phones {
		if phone.Number != number {
			continue
		}
		phone.ReceiveCount++
		// The freeze test runs on the ALREADY incremented count, so the phone that
		// just hit the cap is frozen immediately (app.py:16654-16655).
		if IsFrozen(*phone, receiveLimit) {
			phone.Status = StatusFrozen
		} else {
			phone.Status = StatusAvailable
		}
		phone.LastCode = code
		phone.LastError = ""
		m.phonesUpdated()
		m.stateUpdated()
		return
	}
}
