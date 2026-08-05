// Package importer parses the pasted account lines from the 导入账号 box
// (UI_SPEC S14). Format:
//
//	email----password----client_id----refresh_token[----extra...]
//
// where each extra is either a key=value pair or a bare positional value that is
// sniffed by shape. Ported from parse_account_line (app.py:1617) and
// extract_account_extras (app.py:1645).
package importer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// Separator is the field delimiter used by the paste format.
const Separator = "----"

// Extras is the parsed tail of an account line (app.py:1646).
type Extras struct {
	OpenAIRT        string
	AuthPhoneNumber string
	AuthPhoneSMSURL string
	ReceiveMailbox  string
	MailProvider    string
	AccountType     string
}

// Prefix sets, in the exact order app.py tests them. Order matters: the tests
// are a first-match-wins chain, and e.g. "auth_phone=" must be tried before
// "auth_phone_sms_url=" would ever be considered — Python relies on
// startswith("auth_phone=") not matching "auth_phone_sms_url=...".
var (
	prefixOpenAIRT     = []string{"rt_token=", "openai_rt="}
	prefixAuthPhone    = []string{"auth_phone=", "auth_phone_number=", "phone="}
	prefixAuthSMSURL   = []string{"auth_phone_sms_url=", "auth_sms_url=", "phone_sms_url=", "sms_url="}
	prefixReceiveMail  = []string{"receive_mailbox=", "mailbox_email=", "receive_email=", "inbox="}
	prefixMailProvider = []string{"mail_provider=", "mail_type="}
	prefixAccountType  = []string{"account_type=", "type="}
)

// The three sniffing patterns are Python str patterns, so `\d`, `\s` and `\S`
// are all Unicode there and all ASCII in RE2. Both directions produced real
// defects on pasted lines:
//
//   - a phone written in fullwidth or Arabic-Indic digits ("+٩٧٦٥٤٣٢١٠") is a
//     phone to Python and was silently dropped here, which also cost the row its
//     "待获取RT" status (app.py:1636) and so the RT job never picked it up;
//   - a URL followed by a non-breaking space — routinely produced by pasting out
//     of a web page — ends at the NBSP in Python, while RE2's `\S` swallowed it
//     and stored a phone/SMS URL Python would have rejected outright.
var (
	// "+123456789https://sms.example/x" — a phone and its SMS URL concatenated.
	reInlinePhone = regexp.MustCompile(`^([+` + pyDigit + `][` + pyDigit + pyWSClass + `().\-]*)(https?://` + pyNonWS + `+)$`)
	// A bare phone number: at least 6 more chars after the leading + or digit.
	reBarePhone = regexp.MustCompile(`^[+` + pyDigit + `][` + pyDigit + pyWSClass + `().\-]{5,}$`)
	reBareURL   = regexp.MustCompile(`^https?://` + pyNonWS + `+$`)
)

func hasAnyPrefix(lower string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// valueAfterEq is Python's part.split("=", 1)[1].strip().
func valueAfterEq(part string) string {
	_, v, found := strings.Cut(part, "=")
	if !found {
		return ""
	}
	return pyStrip(v)
}

// ExtractExtras ports extract_account_extras (app.py:1645).
func ExtractExtras(parts []string) Extras {
	var out Extras
	for _, raw := range parts {
		part := pyStrip(raw)
		if part == "" {
			continue
		}
		lower := pyLower(part)

		switch {
		case hasAnyPrefix(lower, prefixOpenAIRT):
			out.OpenAIRT = valueAfterEq(part)
		case hasAnyPrefix(lower, prefixAuthPhone):
			out.AuthPhoneNumber = valueAfterEq(part)
		case hasAnyPrefix(lower, prefixAuthSMSURL):
			out.AuthPhoneSMSURL = valueAfterEq(part)
		case hasAnyPrefix(lower, prefixReceiveMail):
			out.ReceiveMailbox = pyNormalizeEmailAddress(valueAfterEq(part))
		case hasAnyPrefix(lower, prefixMailProvider):
			// Python casefolds and accepts ONLY these two; anything else leaves
			// the field empty rather than storing the raw text.
			if p := pyCaseFold(valueAfterEq(part)); p == "cloudmail" || p == "outlook" {
				out.MailProvider = p
			}
		case hasAnyPrefix(lower, prefixAccountType):
			// NOTE: app.py:1677 accepts only free/plus/team here — NOT k12 or pro,
			// even though those are valid account types elsewhere in the app.
			if t := pyLower(valueAfterEq(part)); t == "free" || t == "plus" || t == "team" {
				out.AccountType = t
			}
		default:
			// Positional fallbacks, sniffed by shape. Each only fills a field that
			// is still empty, so an explicit key=value earlier in the line wins.
			if m := reInlinePhone.FindStringSubmatch(part); m != nil {
				if out.AuthPhoneNumber == "" {
					out.AuthPhoneNumber = pyStrip(m[1])
				}
				if out.AuthPhoneSMSURL == "" {
					out.AuthPhoneSMSURL = pyStrip(m[2])
				}
				continue
			}
			if out.AuthPhoneNumber == "" && reBarePhone.MatchString(part) {
				out.AuthPhoneNumber = part
				continue
			}
			if out.AuthPhoneSMSURL == "" && reBareURL.MatchString(part) {
				out.AuthPhoneSMSURL = part
			}
		}
	}
	return out
}

// ParseLine ports parse_account_line (app.py:1617).
func ParseLine(line string) (models.MailAccount, error) {
	raw := pyStrip(line)
	fields := strings.Split(raw, Separator)
	for i := range fields {
		fields[i] = pyStrip(fields[i])
	}
	if len(fields) < 4 {
		return models.MailAccount{}, fmt.Errorf("格式错误，应为 email----password----client_id----refresh_token")
	}

	email := pyNormalizeEmailAddress(fields[0])
	password, clientID, refreshToken := fields[1], fields[2], fields[3]
	extras := ExtractExtras(fields[4:])

	if email == "" {
		return models.MailAccount{}, fmt.Errorf("email 不能为空")
	}
	// Cloud Mail accounts authenticate through the Cloud Mail API, so they are
	// the one case allowed to omit the OAuth pair (app.py:1626).
	if (clientID == "" || refreshToken == "") && extras.MailProvider != "cloudmail" {
		return models.MailAccount{}, fmt.Errorf("非 Cloud Mail 邮箱的 client_id / refresh_token 不能为空")
	}

	accountType := extras.AccountType
	if accountType == "" {
		// An account that already carries an OpenAI refresh token is assumed Plus
		// (app.py:1635).
		accountType = "free"
		if extras.OpenAIRT != "" {
			accountType = "plus"
		}
	}

	// app.py:1636 — a three-way conditional, in this precedence.
	status := ""
	switch {
	case extras.OpenAIRT != "":
		status = "已绑定手机号"
	case extras.AuthPhoneNumber != "" && extras.AuthPhoneSMSURL != "":
		status = "待获取RT"
	}

	return models.MailAccount{
		Email:    email,
		Password: password,
		ClientID: clientID,
		// Raw is rebuilt from the NORMALIZED email and only the first four
		// fields — the extras are deliberately dropped (app.py:1634).
		RefreshToken:    refreshToken,
		Raw:             strings.Join([]string{email, password, clientID, refreshToken}, Separator),
		AccountType:     accountType,
		Status:          status,
		OpenaiRT:        extras.OpenAIRT,
		AuthPhoneNumber: extras.AuthPhoneNumber,
		AuthPhoneSMSURL: extras.AuthPhoneSMSURL,
		ReceiveMailbox:  extras.ReceiveMailbox,
		MailProvider:    extras.MailProvider,
	}, nil
}

// LineError reports which pasted line failed and why.
type LineError struct {
	Line int
	Err  error
}

func (e LineError) Error() string { return fmt.Sprintf("第 %d 行: %v", e.Line, e.Err) }

// ParseText parses the whole paste box, skipping blank lines. Bad lines are
// collected rather than aborting the import — app.py:14698 continues past a
// failure and reports the set at the end.
func ParseText(text string) ([]models.MailAccount, []LineError) {
	var (
		accounts []models.MailAccount
		errs     []LineError
	)
	// Line numbers count only non-blank lines, matching enumerate(lines, 1)
	// over the already-filtered list at app.py:14687.
	//
	// app.py:14687 splits with str.splitlines(), which breaks on eight separators
	// strings.Split(text, "\n") does not see — a paste carrying U+2028 or a lone
	// CR arrived here as ONE line and every account in it was reported as a
	// single "格式错误" instead of being imported.
	n := 0
	for _, rawLine := range pySplitLines(text) {
		if pyStrip(rawLine) == "" {
			continue
		}
		n++
		account, err := ParseLine(rawLine)
		if err != nil {
			errs = append(errs, LineError{Line: n, Err: err})
			continue
		}
		accounts = append(accounts, account)
	}
	return accounts, errs
}

// MergeKey is the identity MergeInto upserts on: app.py:14701 is
//
//	item.email.lower() == account.email.lower()
//
// — a plain lower(), with NO strip. It is exported because the caller counting
// adds vs updates must key rows the same way MergeInto does. accounts.Key is a
// DIFFERENT normalisation (it strips first, for the table's row identity); using
// that one here made an existing row whose stored email has surrounding
// whitespace count as an update while MergeInto appended it as a second row —
// and, worse, skipped pulling its split session file back in.
func MergeKey(email string) string { return pyLower(email) }

// MergeInto applies the import to an existing account list, reproducing the
// upsert rules at app.py:14701-14717.
//
// The merge is deliberately asymmetric: on an existing account the imported row
// does NOT get to change account_type, status or group — those are worker- and
// user-owned — while openai_rt and the phone/mailbox fields fall back to the old
// value only when the imported one is empty.
func MergeInto(existing []models.MailAccount, imported []models.MailAccount, importGroup string) []models.MailAccount {
	out := append([]models.MailAccount(nil), existing...)
	// app.py:14701 is `next((i for i, item in ...), -1)` — the FIRST row whose
	// lowered email matches. Two rows can share one lowered email (accounts.Key
	// strips, MergeKey does not), and overwriting the index entry made the import
	// update the LAST of them, leaving the row the rest of the app resolves to
	// stale.
	index := make(map[string]int, len(out))
	for i, a := range out {
		key := MergeKey(a.Email)
		if _, seen := index[key]; !seen {
			index[key] = i
		}
	}

	for _, account := range imported {
		key := MergeKey(account.Email)
		i, found := index[key]
		if !found {
			account.Group = importGroup
			out = append(out, account)
			index[key] = len(out) - 1
			continue
		}
		old := out[i]
		account.AccountType = old.AccountType
		account.Status = old.Status
		account.Group = old.Group
		account.OpenaiRT = firstNonEmpty(old.OpenaiRT, account.OpenaiRT)
		account.AuthPhoneNumber = firstNonEmpty(account.AuthPhoneNumber, old.AuthPhoneNumber)
		account.AuthPhoneSMSURL = firstNonEmpty(account.AuthPhoneSMSURL, old.AuthPhoneSMSURL)
		account.ReceiveMailbox = firstNonEmpty(account.ReceiveMailbox, old.ReceiveMailbox)
		account.MailProvider = firstNonEmpty(account.MailProvider, old.MailProvider)
		out[i] = account
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
