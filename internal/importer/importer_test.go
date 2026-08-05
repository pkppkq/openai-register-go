package importer

import "testing"

func TestParseLineMinimal(t *testing.T) {
	a, err := ParseLine("  Foo@Example.COM ---- pw ---- cid ---- rt ")
	if err != nil {
		t.Fatal(err)
	}
	// normalize_email_address (app.py:1610) strips and regex-extracts but does
	// NOT lowercase — case is preserved in the stored address, and callers
	// lowercase separately when they need a dedup key.
	if a.Email != "Foo@Example.COM" {
		t.Errorf("email = %q", a.Email)
	}
	// Raw is rebuilt from the normalized email and only the first four fields
	// (app.py:1634) — it is not the input line.
	if a.Raw != "Foo@Example.COM----pw----cid----rt" {
		t.Errorf("raw = %q", a.Raw)
	}
	if a.AccountType != "free" || a.Status != "" {
		t.Errorf("type/status = %q/%q, want free/empty", a.AccountType, a.Status)
	}
}

func TestParseLineErrors(t *testing.T) {
	for _, line := range []string{"", "a----b----c", "----pw----cid----rt"} {
		if _, err := ParseLine(line); err == nil {
			t.Errorf("expected error for %q", line)
		}
	}
	// Cloud Mail is the one case allowed to omit the OAuth pair (app.py:1626).
	if _, err := ParseLine("a@b.com----pw--------"); err == nil {
		t.Error("missing client_id/refresh_token should fail without cloudmail")
	}
	if _, err := ParseLine("a@b.com----pw------------mail_provider=cloudmail"); err != nil {
		t.Errorf("cloudmail should be exempt: %v", err)
	}
}

// An OpenAI refresh token implies Plus and 已绑定手机号 (app.py:1635-1636).
func TestParseLineDerivedTypeAndStatus(t *testing.T) {
	a, err := ParseLine("a@b.com----pw----cid----rt----openai_rt=XYZ")
	if err != nil {
		t.Fatal(err)
	}
	if a.OpenaiRT != "XYZ" || a.AccountType != "plus" || a.Status != "已绑定手机号" {
		t.Fatalf("got rt=%q type=%q status=%q", a.OpenaiRT, a.AccountType, a.Status)
	}

	// Phone + SMS URL but no RT -> 待获取RT. Needs BOTH.
	b, _ := ParseLine("a@b.com----pw----cid----rt----phone=+12025550100----sms_url=https://s/x")
	if b.Status != "待获取RT" {
		t.Errorf("status = %q, want 待获取RT", b.Status)
	}
	c, _ := ParseLine("a@b.com----pw----cid----rt----phone=+12025550100")
	if c.Status != "" {
		t.Errorf("phone alone must not set a status, got %q", c.Status)
	}
}

func TestExtractExtrasPositionalSniffing(t *testing.T) {
	// Inline "phone immediately followed by URL" (app.py:1681).
	e := ExtractExtras([]string{"+1 (202) 555-0100https://sms.example/abc"})
	if e.AuthPhoneNumber != "+1 (202) 555-0100" || e.AuthPhoneSMSURL != "https://sms.example/abc" {
		t.Fatalf("inline split wrong: %+v", e)
	}
	// Bare positional phone and URL, in either order.
	e = ExtractExtras([]string{"https://sms.example/z", "+12025550100"})
	if e.AuthPhoneNumber != "+12025550100" || e.AuthPhoneSMSURL != "https://sms.example/z" {
		t.Fatalf("bare sniffing wrong: %+v", e)
	}
	// An explicit key=value earlier wins over a later positional.
	e = ExtractExtras([]string{"phone=+15550001111", "+12025550100"})
	if e.AuthPhoneNumber != "+15550001111" {
		t.Errorf("explicit value should win, got %q", e.AuthPhoneNumber)
	}
}

func TestExtractExtrasRejectsUnknownEnums(t *testing.T) {
	// app.py:1672 accepts only cloudmail/outlook; anything else leaves it EMPTY
	// rather than storing the raw text.
	if got := ExtractExtras([]string{"mail_provider=gmail"}).MailProvider; got != "" {
		t.Errorf("unknown provider stored: %q", got)
	}
	// app.py:1677 accepts only free/plus/team here — NOT k12 or pro.
	if got := ExtractExtras([]string{"type=k12"}).AccountType; got != "" {
		t.Errorf("k12 must be rejected by the import parser, got %q", got)
	}
	if got := ExtractExtras([]string{"type=team"}).AccountType; got != "team" {
		t.Errorf("team should be accepted, got %q", got)
	}
}

// Line numbers count only NON-BLANK lines, because Python filters before
// enumerate(lines, 1) (app.py:14687).
func TestParseTextSkipsBlanksAndNumbersFromFiltered(t *testing.T) {
	text := "\n\na@b.com----p----c----r\n\nBAD LINE\n"
	accounts, errs := ParseText(text)
	if len(accounts) != 1 {
		t.Fatalf("got %d accounts, want 1", len(accounts))
	}
	if len(errs) != 1 || errs[0].Line != 2 {
		t.Fatalf("errors = %+v, want one at line 2", errs)
	}
}

// The upsert is deliberately asymmetric (app.py:14701-14717): worker-owned
// fields survive a re-import, while empty imported fields fall back to the old.
func TestMergeIntoPreservesWorkerOwnedFields(t *testing.T) {
	imported, _ := ParseText("A@B.com----newpw----cid----rt")
	existing, _ := ParseText("a@b.com----oldpw----cid----rt----openai_rt=KEEP")
	existing[0].Status = "长链已提取"
	existing[0].Group = "已用11磅"
	existing[0].AccountType = "team"

	out := MergeInto(existing, imported, "未分组")
	if len(out) != 1 {
		t.Fatalf("merge duplicated the account: %d rows", len(out))
	}
	got := out[0]
	if got.Password != "newpw" {
		t.Errorf("password should update, got %q", got.Password)
	}
	for _, c := range []struct{ name, got, want string }{
		{"status", got.Status, "长链已提取"},
		{"group", got.Group, "已用11磅"},
		{"type", got.AccountType, "team"},
		{"openai_rt", got.OpenaiRT, "KEEP"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (must survive re-import)", c.name, c.got, c.want)
		}
	}

	// A genuinely new account lands in the import group.
	fresh, _ := ParseText("new@b.com----p----c----r")
	out = MergeInto(out, fresh, "未分组")
	if len(out) != 2 || out[1].Group != "未分组" {
		t.Fatalf("new account not appended into the import group: %+v", out)
	}
}
