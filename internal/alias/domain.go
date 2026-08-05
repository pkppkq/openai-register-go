package alias

import (
	crand "crypto/rand"
	"errors"
	"math/big"
	"regexp"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// MicrosoftPersonalMailDomains mirrors MICROSOFT_PERSONAL_MAIL_DOMAINS
// (app.py:302). Used to decide whether a 域名邮箱主 account can act as its own
// receiving mailbox without prompting (app.py:14903).
var MicrosoftPersonalMailDomains = map[string]bool{
	"outlook.com": true,
	"hotmail.com": true,
	"live.com":    true,
	"msn.com":     true,
}

var (
	ErrDomainMailDomainFormat  = errors.New("域名格式错误，例如 mail.example.com")     // app.py:1759
	ErrDomainAliasNoReceiveBox = errors.New("缺少实际接收邮件的主 Outlook 邮箱")    // app.py:1796
	ErrDomainAliasDuplicate    = errors.New("随机域名邮箱重复过多，请重试")           // app.py:1778
	ErrCloudMailBaseURL        = errors.New("Cloud Mail Base URL 格式错误") // app.py:14459
)

// Python re.fullmatch anchors both ends; \A/\z (not ^/$) is the RE2 equivalent
// that also refuses a trailing newline. app.py:1758.
var reDomainMailDomain = regexp.MustCompile(`\A(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}\z`)

// NormalizeDomainMailDomain mirrors normalize_domain_mail_domain (app.py:1753):
// case-folds, drops everything up to the last "@", trims surrounding dots, and
// validates the result as a hostname.
func NormalizeDomainMailDomain(value string) (string, error) {
	text := pyCaseFold(pyStrip(value))
	if i := strings.LastIndex(text, "@"); i >= 0 {
		// Python rsplit("@", 1)[-1] — the part after the LAST @.
		text = text[i+1:]
	}
	text = strings.Trim(pyStrip(text), ".")
	if !reDomainMailDomain.MatchString(text) {
		return "", ErrDomainMailDomainFormat
	}
	return text, nil
}

const (
	domainAliasAlphabet      = "abcdefghijklmnopqrstuvwxyz0123456789"
	domainAliasFirstAlphabet = "abcdefghijklmnopqrstuvwxyz"
	domainAliasAttempts      = 300 // app.py:1771
)

// domainAliasChoice mirrors secrets.choice (app.py:1772) — the domain-alias
// generator uses the CSPRNG, unlike the plus-alias generator which uses random.
// Swapped out by the tests.
var domainAliasChoice = func(alphabet string) byte {
	n, err := crand.Int(crand.Reader, big.NewInt(int64(len(alphabet))))
	if err != nil {
		// crypto/rand cannot fail on any supported platform; Python's
		// secrets.choice has no failure mode at all, so degrade rather than
		// change the signature.
		return alphabet[0]
	}
	return alphabet[n.Int64()]
}

// RandomDomainAliasEmail mirrors random_domain_alias_email (app.py:1763).
// existingEmails is consulted case-insensitively; pass 0 for localLength to get
// Python's default of 12 (`int(local_length or 12)`).
//
// Python also re-checks `if not domain or "." not in domain` after the
// normalize call; that branch is unreachable because the fullmatch above
// already requires a dot, so it is not ported.
func RandomDomainAliasEmail(targetDomain string, existingEmails map[string]bool, localLength int) (string, error) {
	domain, err := NormalizeDomainMailDomain(targetDomain)
	if err != nil {
		return "", err
	}
	existing := make(map[string]bool, len(existingEmails))
	for value := range existingEmails {
		existing[pyCaseFold(value)] = true
	}
	if localLength == 0 {
		localLength = 12
	}
	// Python: min(32, max(6, ...)).
	if localLength < 6 {
		localLength = 6
	}
	if localLength > 32 {
		localLength = 32
	}
	for attempt := 0; attempt < domainAliasAttempts; attempt++ {
		var b strings.Builder
		b.Grow(localLength + 1 + len(domain))
		b.WriteByte(domainAliasChoice(domainAliasFirstAlphabet))
		for i := 0; i < localLength-1; i++ {
			b.WriteByte(domainAliasChoice(domainAliasAlphabet))
		}
		b.WriteString("@")
		b.WriteString(domain)
		candidate := b.String()
		if !existing[pyCaseFold(candidate)] {
			return candidate, nil
		}
	}
	return "", ErrDomainAliasDuplicate
}

// CloneAccountForDomainAlias mirrors clone_account_for_domain_alias
// (app.py:1786): a 域名邮箱分 child that reuses the mother account's Outlook
// credentials but receives its mail either through Cloud Mail or through an
// explicit forwarding mailbox.
//
// mailProvider is stripped+case-folded before it is stored, so the returned
// account carries "cloudmail"/"outlook", never the caller's casing.
func CloneAccountForDomainAlias(account models.MailAccount, aliasEmail, receiveMailbox, mailProvider string) (models.MailAccount, error) {
	aliasEmail = pyNormalizeEmailAddress(aliasEmail)
	receiveMailbox = pyNormalizeEmailAddress(receiveMailbox)
	provider := pyCaseFold(pyStrip(mailProvider))
	if provider != "cloudmail" && receiveMailbox == "" {
		return models.MailAccount{}, ErrDomainAliasNoReceiveBox
	}
	return models.MailAccount{
		Email:          aliasEmail,
		Password:       account.Password,
		ClientID:       account.ClientID,
		RefreshToken:   account.RefreshToken,
		Raw:            strings.Join([]string{aliasEmail, account.Password, account.ClientID, account.RefreshToken}, "----"),
		AccountType:    "free",
		Status:         DomainAliasPendingStatus,
		ReceiveMailbox: receiveMailbox,
		MailProvider:   provider,
		// Always the reserved child group, never the mother's group — unlike
		// CloneAccountForPlusAlias. app.py:1807.
		Group: AccountDomainMailChildGroup,
	}, nil
}

// DomainAliasReceiveMailbox is the auto-resolution half of app.py:14901-14904:
// use the mother account's configured receive_mailbox, else the mother address
// itself when it is a Microsoft personal mailbox. An empty result means the UI
// must prompt the user (app.py:14906); an address without "@" after the prompt
// is the "未设置有效的接收主 Outlook 邮箱" error at app.py:14912.
func DomainAliasReceiveMailbox(source models.MailAccount) string {
	receiveMailbox := pyNormalizeEmailAddress(source.ReceiveMailbox)
	if receiveMailbox != "" {
		return receiveMailbox
	}
	sourceDomain := ""
	if i := strings.LastIndex(source.Email, "@"); i >= 0 {
		sourceDomain = pyCaseFold(source.Email[i+1:])
	}
	if MicrosoftPersonalMailDomains[sourceDomain] {
		return pyNormalizeEmailAddress(source.Email)
	}
	return ""
}

// NewCloudMailDomainAccount builds the Cloud-Mail-mode 域名邮箱分 account of
// app.py:14872-14890 (and the single random account of app.py:15162-15187,
// which is byte-identical once its raw field is rewritten at 15181).
//
// password comes from the caller because generate_openai_compatible_password
// (app.py:1781) is already ported as worker.GeneratePassword; alias must stay a
// leaf package and not import internal/worker.
func NewCloudMailDomainAccount(aliasEmail, password, cloudMailBase, cloudMailToken, group string) models.MailAccount {
	if group == "" {
		group = AccountDomainMailChildGroup
	}
	return models.MailAccount{
		Email:    aliasEmail,
		Password: password,
		// client_id/refresh_token stay empty: Cloud Mail needs no OAuth. The
		// raw line carries the mail_provider marker so a re-import round-trips
		// (parse_account_line requires it, app.py:1626).
		Raw:            strings.Join([]string{aliasEmail, password, "", "", "mail_provider=cloudmail"}, "----"),
		AccountType:    "free",
		Status:         DomainAliasPendingStatus,
		MailProvider:   "cloudmail",
		CloudMailBase:  cloudMailBase,
		CloudMailToken: cloudMailToken,
		Group:          group,
	}
}

// ---------------------------------------------------------------------------
// Cloud Mail runtime config
// ---------------------------------------------------------------------------

// CloudMailSettings mirrors the dict returned by _cloud_mail_settings
// (app.py:14454).
type CloudMailSettings struct {
	Enabled bool
	BaseURL string
	Token   string
	Domain  string
}

var reCloudMailBaseURL = regexp.MustCompile(`(?i)^https?://`)

// CloudMailSettingsFrom mirrors _cloud_mail_settings (app.py:14454): trims the
// base URL and strips every trailing "/", trims the token, and validates the
// scheme.
//
// Domain is ALWAYS models.DefaultDomainMailDomain — app.py:14457 hard-codes
// DEFAULT_DOMAIN_MAIL_DOMAIN and ignores the self.domain_mail_domain entry, so
// this function takes no domain argument.
func CloudMailSettingsFrom(baseURL, token string, enabled bool) (CloudMailSettings, error) {
	base := strings.TrimRight(pyStrip(baseURL), "/")
	if !reCloudMailBaseURL.MatchString(base) {
		return CloudMailSettings{}, ErrCloudMailBaseURL
	}
	return CloudMailSettings{
		Enabled: enabled,
		BaseURL: base,
		Token:   pyStrip(token),
		Domain:  models.DefaultDomainMailDomain,
	}, nil
}

// ApplyCloudMailRuntimeConfig mirrors _apply_cloud_mail_runtime_config
// (app.py:14467): stamps mail_provider/cloud_mail_base/cloud_mail_token onto
// every account whose email domain equals the Cloud Mail domain, and strips
// those three fields from any other account still marked cloudmail.
//
// Accounts are mutated in place, hence the pointer slice; pass a one-element
// slice for Python's target_account form. An invalid base URL makes
// _cloud_mail_settings raise, which the Python method swallows and returns
// from, so NOTHING is changed — not even the strip branch. That is why this
// function parses the settings itself instead of taking a CloudMailSettings.
func ApplyCloudMailRuntimeConfig(accounts []*models.MailAccount, baseURL, token string, enabled bool) {
	settings, err := CloudMailSettingsFrom(baseURL, token, enabled)
	if err != nil {
		return
	}
	for _, account := range accounts {
		if account == nil {
			continue
		}
		emailDomain := ""
		if i := strings.LastIndex(account.Email, "@"); i >= 0 {
			emailDomain = pyCaseFold(account.Email[i+1:])
		}
		// Python compares the case-folded email domain against the settings
		// domain as-is (app.py:14477). That is safe only because the domain is
		// the lower-case DEFAULT_DOMAIN_MAIL_DOMAIN constant; ported verbatim.
		if settings.Enabled && emailDomain == settings.Domain {
			account.MailProvider = "cloudmail"
			account.CloudMailBase = settings.BaseURL
			account.CloudMailToken = settings.Token
			continue
		}
		// app.py:14483 case-folds WITHOUT stripping here, unlike
		// _account_uses_cloud_mail below which strips first. A provider of
		// " cloudmail " therefore survives this branch untouched yet still
		// counts as Cloud Mail. Faithfully reproduced.
		if pyCaseFold(account.MailProvider) == "cloudmail" {
			account.MailProvider = ""
			account.CloudMailBase = ""
			account.CloudMailToken = ""
		}
	}
}

// AccountUsesCloudMail mirrors _account_uses_cloud_mail (app.py:14488). Note
// that Python re-applies the runtime config to the account first, so this is a
// MUTATING read: it can attach or clear the Cloud Mail credentials as a side
// effect. Callers relying on that behaviour (app.py:21031, 21504, 21816, 22679)
// must keep passing a pointer to the live account.
func AccountUsesCloudMail(account *models.MailAccount, baseURL, token string, enabled bool) bool {
	if account == nil {
		return false
	}
	ApplyCloudMailRuntimeConfig([]*models.MailAccount{account}, baseURL, token, enabled)
	return pyCaseFold(pyStrip(account.MailProvider)) == "cloudmail"
}
