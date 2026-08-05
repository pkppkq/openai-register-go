// Package alias is a faithful Go port of the account-domain helpers in app.py
// (gap G26): the reserved group/status names, the "+别名" (plus-alias) mailbox
// model and its per-mailbox cap, the domain-mailbox (域名邮箱) cloning helpers,
// the Cloud Mail runtime-config application, and the mailbox email-lock check.
//
// Everything here is pure: nothing touches the UI, the state store or the
// network. Callers own the account slice and decide when to persist.
//
// Python anchors: app.py:295-303 (constants), 1699-1750 (plus alias),
// 1753-1808 (domain alias), 14454-14490 (cloud mail runtime config),
// 14723-14810 (plus-alias generation), 19822-19832 (email lock).
package alias

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// Reserved group and status names.
//
// UI_SPEC §5.4 lists these as missing from internal/models; they live here
// because internal/models is owned by another area during this port. Import
// them as alias.AccountEmailLockedGroup etc. models already owns
// AccountDefaultGroup (未分组), AccountAllGroup (全部) and
// DefaultDomainMailDomain (mail.example.com).
const (
	// MaxPlusAliasesPerMailbox is MAX_PLUS_ALIASES_PER_MAILBOX (app.py:295).
	// UI_SPEC §5.6.6 recorded this value as UNKNOWN; the source says 4.
	MaxPlusAliasesPerMailbox = 4

	AccountEmailLockedGroup     = "邮箱锁定"  // app.py:298
	AccountEmailLockedStatus    = "邮箱锁定"  // app.py:299 — same literal as the group name
	AccountDomainMailMainGroup  = "域名邮箱主" // app.py:300
	AccountDomainMailChildGroup = "域名邮箱分" // app.py:301

	// Statuses stamped on freshly cloned accounts.
	PlusAliasPendingStatus   = "别名待注册"   // app.py:1747
	DomainAliasPendingStatus = "域名邮箱待注册" // app.py:1804, 14885, 15175
)

// Python's `\D` on a str pattern is Unicode-aware: `\d` covers category Nd, so
// `\D` is "not a Unicode decimal digit". Go's RE2 `\D` is ASCII-only ([^0-9]),
// which would strip e.g. U+0663 ARABIC-INDIC DIGIT THREE that Python keeps.
// Python wins: match on \p{Nd}. app.py:1732.
var reNonUnicodeDigit = regexp.MustCompile(`[^\p{Nd}]+`)

// Exported so callers can compare with errors.Is when they need to distinguish
// "not an email" from "suffix had no digits"; the messages are the Python
// ValueError texts verbatim because the UI shows them.
var (
	ErrPlusAliasBadEmail  = errors.New("邮箱格式错误，无法生成 + 别名") // app.py:1729
	ErrPlusAliasNoDigits  = errors.New("别名后缀必须包含数字")       // app.py:1734
	ErrPlusAliasDuplicate = errors.New("随机别名重复过多，未生成")     // app.py:14788
)

// The two folds are NOT interchangeable and app.py uses both, so every call
// site below names the one Python names:
//
//   - the plus-alias generator keys everything with str.lower()
//     (app.py:1718, 1721, 14751-14762, 14783, 14791) -> pyLower
//   - the domain-mailbox and Cloud Mail helpers use str.casefold()
//     (app.py:1754, 1767, 1789, 14474, 14483, 14489, 19823) -> pyCaseFold
//
// See pytext.go: casefold is full folding, lower is not.

// MailboxEmailForPlusAlias mirrors mailbox_email_for_plus_alias (app.py:1699).
// Strips a "+tag" from the local part, returning the mother mailbox address.
// Returns the normalized input unchanged when there is no "@" or no "+".
func MailboxEmailForPlusAlias(emailAddr string) string {
	text := pyNormalizeEmailAddress(emailAddr)
	local, domain, ok := strings.Cut(text, "@")
	if !ok {
		return text
	}
	base, _, ok := strings.Cut(local, "+")
	if !ok {
		return text
	}
	return base + "@" + domain
}

// IsPlusAliasEmail mirrors is_plus_alias_email (app.py:1709).
func IsPlusAliasEmail(emailAddr string) bool {
	text := pyNormalizeEmailAddress(emailAddr)
	local, _, ok := strings.Cut(text, "@")
	if !ok {
		return false
	}
	return strings.Contains(local, "+")
}

// CountPlusAliasesForMailbox mirrors count_plus_aliases_for_mailbox
// (app.py:1717). Counts the existing +aliases that share emailAddr's mother
// mailbox.
func CountPlusAliasesForMailbox(accounts []models.MailAccount, emailAddr string) int {
	base := pyLower(MailboxEmailForPlusAlias(emailAddr))
	n := 0
	for _, account := range accounts {
		if IsPlusAliasEmail(account.Email) && pyLower(MailboxEmailForPlusAlias(account.Email)) == base {
			n++
		}
	}
	return n
}

// PlusAliasEmail mirrors plus_alias_email (app.py:1726): local+<digits>@domain,
// built from the *mother* local part (an existing +tag is replaced, not nested).
func PlusAliasEmail(emailAddr, suffix string) (string, error) {
	text := pyNormalizeEmailAddress(emailAddr)
	local, domain, ok := strings.Cut(text, "@")
	if !ok {
		return "", ErrPlusAliasBadEmail
	}
	baseLocal, _, _ := strings.Cut(local, "+")
	suffixText := reNonUnicodeDigit.ReplaceAllString(suffix, "")
	if suffixText == "" {
		return "", ErrPlusAliasNoDigits
	}
	return baseLocal + "+" + suffixText + "@" + domain, nil
}

// CloneAccountForPlusAlias mirrors clone_account_for_plus_alias (app.py:1738).
// Credentials (password/client_id/refresh_token) and the receive mailbox are
// inherited from the mother account; type/status are reset.
func CloneAccountForPlusAlias(account models.MailAccount, aliasEmail string) models.MailAccount {
	aliasEmail = pyNormalizeEmailAddress(aliasEmail)
	group := account.Group
	// Python: `account.group or ACCOUNT_DEFAULT_GROUP` — falsy means the empty
	// string only. A whitespace-only group is truthy in Python and is kept, so
	// do NOT TrimSpace here. app.py:1749.
	if group == "" {
		group = models.AccountDefaultGroup
	}
	return models.MailAccount{
		Email:          aliasEmail,
		Password:       account.Password,
		ClientID:       account.ClientID,
		RefreshToken:   account.RefreshToken,
		Raw:            strings.Join([]string{aliasEmail, account.Password, account.ClientID, account.RefreshToken}, "----"),
		AccountType:    "free",
		Status:         PlusAliasPendingStatus,
		ReceiveMailbox: account.ReceiveMailbox,
		Group:          group,
	}
}

// plusAliasRandInt returns a random int in [lo, hi] inclusive, mirroring
// Python's random.randint (app.py:14776) — random, not secrets, is what the
// plus-alias generator uses. Swapped out by the tests.
var plusAliasRandInt = func(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + rand.IntN(hi-lo+1)
}

// plusAliasSuffixLengths and plusAliasAttempts mirror the literal `(3, 4, 5, 6)`
// and `range(80)` loops at app.py:14772-14773.
var plusAliasSuffixLengths = [4]int{3, 4, 5, 6}

const plusAliasAttempts = 80

// GeneratePlusAliases is the non-interactive core of create_plus_alias_accounts
// (app.py:14723-14794): for every selected mother mailbox it mints up to count
// new "+<3..6 digits>" alias accounts, capped at MaxPlusAliasesPerMailbox per
// mother mailbox, counting the aliases that already exist in accounts.
//
// accounts is the full account list (used for the uniqueness set and the alias
// census); selected are the rows the user picked. Nothing is mutated: the caller
// appends created to its own list, which is what app.py:14793 does inline.
// errs holds the user-facing skip and failure lines in Python's order and
// wording; the caller shows the first five (app.py:14796/14810).
//
// The dialog clamps count to 1..MaxPlusAliasesPerMailbox before calling
// (askinteger minvalue/maxvalue, app.py:14736-14737); a non-positive count is
// Python's `if not count: return`.
func GeneratePlusAliases(accounts, selected []models.MailAccount, count int) (created []models.MailAccount, errs []string) {
	if count <= 0 {
		return nil, nil
	}
	existing := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		existing[pyLower(account.Email)] = true
	}
	// alias_counts is a dict comprehension over ALL accounts (app.py:14754).
	// Go map iteration order is random where Python's dict is ordered, but the
	// value depends only on the key here, so the result is order-independent.
	aliasCounts := make(map[string]int, len(accounts))
	for _, account := range accounts {
		key := pyLower(MailboxEmailForPlusAlias(account.Email))
		if _, seen := aliasCounts[key]; !seen {
			aliasCounts[key] = CountPlusAliasesForMailbox(accounts, account.Email)
		}
	}

	for _, account := range selected {
		if account.ClientID == "" || account.RefreshToken == "" {
			errs = append(errs, account.Email+": 缺少 client_id/refresh_token")
			continue
		}
		base := MailboxEmailForPlusAlias(account.Email)
		baseKey := pyLower(base)
		remaining := MaxPlusAliasesPerMailbox - aliasCounts[baseKey]
		if remaining <= 0 {
			errs = append(errs, fmt.Sprintf("%s: 已有 %d 个别名，跳过", base, MaxPlusAliasesPerMailbox))
			continue
		}
		createCount := count
		if remaining < createCount {
			createCount = remaining
		}
		if createCount < count {
			errs = append(errs, fmt.Sprintf("%s: 最多 %d 个，本次只生成 %d 个", base, MaxPlusAliasesPerMailbox, createCount))
		}
		for i := 0; i < createCount; i++ {
			aliasEmail := ""
			for _, suffixLen := range plusAliasSuffixLengths {
				lower := 1
				for d := 0; d < suffixLen-1; d++ {
					lower *= 10
				}
				upper := lower*10 - 1
				for attempt := 0; attempt < plusAliasAttempts; attempt++ {
					suffix := fmt.Sprintf("%d", plusAliasRandInt(lower, upper))
					candidate, err := PlusAliasEmail(account.Email, suffix)
					if err != nil {
						// Python only breaks the inner attempt loop here, so a
						// malformed address produces one error per suffix
						// length (4 in total) plus the "重复过多" line below.
						// Faithfully reproduced. app.py:14779-14781.
						errs = append(errs, account.Email+": "+err.Error())
						break
					}
					if !existing[pyLower(candidate)] {
						aliasEmail = candidate
						break
					}
				}
				if aliasEmail != "" {
					break
				}
			}
			if aliasEmail == "" {
				errs = append(errs, account.Email+": "+ErrPlusAliasDuplicate.Error())
				continue
			}
			aliasAccount := CloneAccountForPlusAlias(account, aliasEmail)
			existing[pyLower(aliasEmail)] = true
			aliasCounts[baseKey]++
			created = append(created, aliasAccount)
		}
	}
	return created, errs
}

// AccountMailboxKey mirrors _account_mailbox_key (app.py:19822): the mother
// mailbox address, case-folded, used to group an account with its +aliases.
func AccountMailboxKey(emailAddr string) string {
	return pyCaseFold(MailboxEmailForPlusAlias(pyStrip(emailAddr)))
}

// IsAccountEmailLocked mirrors _is_account_email_locked (app.py:19825): true
// when ANY account sharing this mother mailbox carries the 邮箱锁定 status —
// one locked +alias locks every sibling.
//
// Python compares the status with `==`, not case-insensitively (app.py:19830).
func IsAccountEmailLocked(accounts []models.MailAccount, account models.MailAccount) bool {
	mailboxKey := AccountMailboxKey(account.Email)
	if mailboxKey == "" {
		return false
	}
	for _, candidate := range accounts {
		if AccountMailboxKey(candidate.Email) == mailboxKey && candidate.Status == AccountEmailLockedStatus {
			return true
		}
	}
	return false
}
