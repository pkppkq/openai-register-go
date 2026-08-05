package alias

import (
	"errors"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// stubDomainAliasChoice replays letters in order, then repeats the last one, so
// the generated local part is predictable.
func stubDomainAliasChoice(t *testing.T, fixed byte) {
	t.Helper()
	prev := domainAliasChoice
	domainAliasChoice = func(string) byte { return fixed }
	t.Cleanup(func() { domainAliasChoice = prev })
}

func TestNormalizeDomainMailDomain(t *testing.T) {
	ok := [][2]string{
		{"mail.example.com", "mail.example.com"},
		{"  USER@MAIL.EXAMPLE.COM \n", "mail.example.com"},
		{".mail.example.com.", "mail.example.com"},
		{"a@b@sub.mail.example.com", "sub.mail.example.com"}, // rsplit takes the LAST @
	}
	for _, c := range ok {
		got, err := NormalizeDomainMailDomain(c[0])
		if err != nil || got != c[1] {
			t.Errorf("NormalizeDomainMailDomain(%q) = %q, %v; want %q", c[0], got, err, c[1])
		}
	}
	for _, bad := range []string{"", "localhost", "-bad.com", "mail.example.c", "mail example.com", "mail.example.com\nevil.com"} {
		if _, err := NormalizeDomainMailDomain(bad); !errors.Is(err, ErrDomainMailDomainFormat) {
			t.Errorf("NormalizeDomainMailDomain(%q) must fail, got %v", bad, err)
		}
	}
}

func TestRandomDomainAliasEmail(t *testing.T) {
	stubDomainAliasChoice(t, 'a')
	got, err := RandomDomainAliasEmail("mail.example.com", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != strings.Repeat("a", 12)+"@mail.example.com" {
		t.Fatalf("default local length must be 12 (app.py:1763), got %q", got)
	}
	// min(32, max(6, n)) — app.py:1770.
	if got, _ := RandomDomainAliasEmail("mail.example.com", nil, 3); got != strings.Repeat("a", 6)+"@mail.example.com" {
		t.Errorf("short length must clamp to 6, got %q", got)
	}
	if got, _ := RandomDomainAliasEmail("mail.example.com", nil, 99); got != strings.Repeat("a", 32)+"@mail.example.com" {
		t.Errorf("long length must clamp to 32, got %q", got)
	}
	// Collision check is case-insensitive, and 300 collisions raise.
	taken := map[string]bool{strings.ToUpper(strings.Repeat("a", 12)) + "@MAIL.EXAMPLE.COM": true}
	if _, err := RandomDomainAliasEmail("mail.example.com", taken, 12); !errors.Is(err, ErrDomainAliasDuplicate) {
		t.Fatalf("want ErrDomainAliasDuplicate, got %v", err)
	}
	if _, err := RandomDomainAliasEmail("not a domain", nil, 12); !errors.Is(err, ErrDomainMailDomainFormat) {
		t.Fatalf("want ErrDomainMailDomainFormat, got %v", err)
	}
}

func TestRandomDomainAliasEmailFirstCharIsALetter(t *testing.T) {
	// Unstubbed: the real CSPRNG path must never start the local part with a
	// digit (app.py:1772 uses a letters-only first alphabet).
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		got, err := RandomDomainAliasEmail("mail.example.com", seen, 12)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got[0] < 'a' || got[0] > 'z' {
			t.Fatalf("local part must start with a letter: %q", got)
		}
		if seen[got] {
			t.Fatalf("duplicate despite the existing set: %q", got)
		}
		seen[got] = true
	}
}

func TestCloneAccountForDomainAlias(t *testing.T) {
	source := models.MailAccount{
		Email: "main@outlook.com", Password: "pw", ClientID: "cid", RefreshToken: "rt",
		AccountType: "plus", Status: "成功", Group: AccountDomainMailMainGroup,
	}
	got, err := CloneAccountForDomainAlias(source, " child@mail.example.com ", "  <Main@outlook.com> ", "Outlook")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Email != "child@mail.example.com" || got.ReceiveMailbox != "Main@outlook.com" {
		t.Errorf("addresses not normalized: %+v", got)
	}
	if got.MailProvider != "outlook" {
		t.Errorf("provider must be stripped+case-folded, got %q", got.MailProvider)
	}
	if got.Raw != "child@mail.example.com----pw----cid----rt" {
		t.Errorf("raw = %q", got.Raw)
	}
	if got.AccountType != "free" || got.Status != DomainAliasPendingStatus {
		t.Errorf("type/status = %q/%q", got.AccountType, got.Status)
	}
	// Unlike the plus-alias clone, the group is ALWAYS the reserved child
	// group, never the mother's. app.py:1807.
	if got.Group != AccountDomainMailChildGroup {
		t.Errorf("group = %q, want %q", got.Group, AccountDomainMailChildGroup)
	}

	if _, err := CloneAccountForDomainAlias(source, "child@mail.example.com", "", "outlook"); !errors.Is(err, ErrDomainAliasNoReceiveBox) {
		t.Fatalf("a forwarding alias needs a receive mailbox, got %v", err)
	}
	// Cloud Mail needs no forwarding mailbox.
	got, err = CloneAccountForDomainAlias(source, "child@mail.example.com", "", " CloudMail ")
	if err != nil || got.MailProvider != "cloudmail" || got.ReceiveMailbox != "" {
		t.Fatalf("cloudmail clone = %+v, %v", got, err)
	}
}

func TestDomainAliasReceiveMailbox(t *testing.T) {
	if got := DomainAliasReceiveMailbox(models.MailAccount{Email: "a@corp.com", ReceiveMailbox: " box@outlook.com "}); got != "box@outlook.com" {
		t.Errorf("configured mailbox wins, got %q", got)
	}
	if got := DomainAliasReceiveMailbox(models.MailAccount{Email: "a@HOTMAIL.com"}); got != "a@HOTMAIL.com" {
		t.Errorf("a Microsoft personal mailbox receives its own mail, got %q", got)
	}
	if got := DomainAliasReceiveMailbox(models.MailAccount{Email: "a@corp.com"}); got != "" {
		t.Errorf("an unknown domain must fall through to the prompt, got %q", got)
	}
}

func TestNewCloudMailDomainAccount(t *testing.T) {
	got := NewCloudMailDomainAccount("x@mail.example.com", "Pw13chars!A7", "https://cm.dev", "tok", "")
	if got.ClientID != "" || got.RefreshToken != "" {
		t.Error("cloud mail accounts carry no OAuth credentials")
	}
	// Five fields joined by "----" with two empty ones in the middle.
	if got.Raw != "x@mail.example.com----Pw13chars!A7------------mail_provider=cloudmail" {
		t.Errorf("raw = %q", got.Raw)
	}
	if got.MailProvider != "cloudmail" || got.CloudMailBase != "https://cm.dev" || got.CloudMailToken != "tok" {
		t.Errorf("cloud mail fields = %+v", got)
	}
	if got.Status != DomainAliasPendingStatus || got.Group != AccountDomainMailChildGroup {
		t.Errorf("status/group = %q/%q", got.Status, got.Group)
	}
}

func TestCloudMailSettingsFrom(t *testing.T) {
	got, err := CloudMailSettingsFrom("  https://cm.dev//  ", "  tok  ", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.BaseURL != "https://cm.dev" || got.Token != "tok" || !got.Enabled {
		t.Errorf("settings = %+v", got)
	}
	// app.py:14457 hard-codes DEFAULT_DOMAIN_MAIL_DOMAIN and ignores the entry.
	if got.Domain != models.DefaultDomainMailDomain {
		t.Errorf("domain = %q, want %q", got.Domain, models.DefaultDomainMailDomain)
	}
	if got, _ := CloudMailSettingsFrom("HTTP://CM.DEV/", "", false); got.BaseURL != "HTTP://CM.DEV" {
		t.Errorf("the scheme check is case-insensitive and must not rewrite the URL, got %q", got.BaseURL)
	}
	for _, bad := range []string{"", "cm.dev", "ftp://cm.dev", " http s://cm.dev"} {
		if _, err := CloudMailSettingsFrom(bad, "tok", true); !errors.Is(err, ErrCloudMailBaseURL) {
			t.Errorf("CloudMailSettingsFrom(%q) must fail, got %v", bad, err)
		}
	}
}

func TestApplyCloudMailRuntimeConfig(t *testing.T) {
	const base = "https://cm.dev"

	match := &models.MailAccount{Email: "a@MAIL.EXAMPLE.COM"}
	other := &models.MailAccount{Email: "b@corp.com", MailProvider: "CloudMail", CloudMailBase: "old", CloudMailToken: "old"}
	outlook := &models.MailAccount{Email: "c@corp.com", MailProvider: "outlook", ReceiveMailbox: "c@outlook.com"}
	ApplyCloudMailRuntimeConfig([]*models.MailAccount{match, other, outlook}, base+"/", "tok", true)

	if match.MailProvider != "cloudmail" || match.CloudMailBase != base || match.CloudMailToken != "tok" {
		t.Errorf("matching domain not stamped: %+v", match)
	}
	if other.MailProvider != "" || other.CloudMailBase != "" || other.CloudMailToken != "" {
		t.Errorf("non-matching cloudmail account not stripped: %+v", other)
	}
	if outlook.MailProvider != "outlook" {
		t.Errorf("a non-cloudmail provider must be left alone: %+v", outlook)
	}

	// Disabling clears the previously stamped account.
	ApplyCloudMailRuntimeConfig([]*models.MailAccount{match}, base, "tok", false)
	if match.MailProvider != "" || match.CloudMailBase != "" {
		t.Errorf("disabled config must strip: %+v", match)
	}

	// An invalid base URL makes _cloud_mail_settings raise; the Python method
	// swallows it and returns, so NOTHING changes. app.py:14468-14471.
	frozen := &models.MailAccount{Email: "d@corp.com", MailProvider: "cloudmail", CloudMailBase: "keep", CloudMailToken: "keep"}
	ApplyCloudMailRuntimeConfig([]*models.MailAccount{frozen}, "cm.dev", "tok", true)
	if frozen.MailProvider != "cloudmail" || frozen.CloudMailBase != "keep" {
		t.Errorf("an invalid base URL must be a no-op: %+v", frozen)
	}
}

// app.py:14483 case-folds the provider WITHOUT stripping, while app.py:14490
// strips first. " cloudmail " therefore survives the strip branch yet still
// counts as Cloud Mail. Documented Python quirk, ported verbatim.
func TestCloudMailProviderStripAsymmetry(t *testing.T) {
	account := &models.MailAccount{Email: "e@corp.com", MailProvider: " cloudmail ", CloudMailBase: "keep"}
	ApplyCloudMailRuntimeConfig([]*models.MailAccount{account}, "https://cm.dev", "tok", true)
	if account.MailProvider != " cloudmail " || account.CloudMailBase != "keep" {
		t.Fatalf("padded provider must not be stripped by apply: %+v", account)
	}
	if !AccountUsesCloudMail(account, "https://cm.dev", "tok", true) {
		t.Fatal("a padded provider still counts as cloud mail")
	}
}

// _account_uses_cloud_mail re-applies the runtime config, so it mutates.
func TestAccountUsesCloudMailMutates(t *testing.T) {
	account := &models.MailAccount{Email: "f@mail.example.com"}
	if !AccountUsesCloudMail(account, "https://cm.dev", "tok", true) {
		t.Fatal("an account on the cloud domain uses cloud mail")
	}
	if account.CloudMailToken != "tok" {
		t.Errorf("credentials must be attached as a side effect: %+v", account)
	}
	if AccountUsesCloudMail(account, "https://cm.dev", "tok", false) {
		t.Fatal("disabled config means no cloud mail")
	}
	if account.MailProvider != "" || account.CloudMailToken != "" {
		t.Errorf("credentials must be cleared as a side effect: %+v", account)
	}
	if AccountUsesCloudMail(nil, "https://cm.dev", "tok", true) {
		t.Fatal("nil account must be false, not a panic")
	}
}
