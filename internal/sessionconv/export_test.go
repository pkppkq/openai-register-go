package sessionconv

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// Expected values produced by exec'ing app.py:1878-1898 under CPython 3.12.
func TestAccountExportLineMatchesPython(t *testing.T) {
	cases := []struct {
		account models.MailAccount
		prefix  string
		want    string
	}{
		{models.MailAccount{Email: "a@b.com", Password: "pw", ClientID: "cid", RefreshToken: "rft"}, "",
			"a@b.com----pw----cid----rft"},
		// Python strips the prefix, then wraps it in parentheses.
		{models.MailAccount{Email: "a@b.com", Password: "pw", ClientID: "cid", RefreshToken: "rft"}, " Tag ",
			"(Tag)a@b.com----pw----cid----rft"},
		// Trailing empty fields are removed by rstrip("-").
		{models.MailAccount{Email: "a@b.com"}, "T", "(T)a@b.com"},
		// rt_token already present in raw -> not appended twice.
		{models.MailAccount{Email: "a@b.com", Raw: "a@b.com----pw----cid----rft----rt_token=rt_old"}, "",
			"a@b.com----pw----cid----rft----rt_token=rt_old"},
		{models.MailAccount{Email: "a@b.com", Raw: "a@b.com----pw----cid----rft----rt_token=rt_old", OpenaiRT: "rt_new"}, "P",
			"(P)a@b.com----pw----cid----rft----rt_token=rt_old"},
		{models.MailAccount{
			Email: "a@b.com", OpenaiRT: "rt_x", AuthPhoneNumber: "+1555", AuthPhoneSMSURL: "http://s",
			ReceiveMailbox: "r@b.com", MailProvider: "hotmail",
		}, "", "a@b.com----rt_token=rt_x----auth_phone=+1555----auth_phone_sms_url=http://s----receive_mailbox=r@b.com----mail_provider=hotmail"},
		{models.MailAccount{}, "", ""},
		// An empty first field still receives the prefix.
		{models.MailAccount{Password: "pw", ClientID: "cid", RefreshToken: "rft"}, "Z", "(Z)----pw----cid----rft"},
		{models.MailAccount{Email: "a@b.com", Raw: "a@b.com"}, "Q", "(Q)a@b.com"},
	}
	for i, tc := range cases {
		if got := AccountExportLine(tc.account, tc.prefix); got != tc.want {
			t.Errorf("case %d: got %q, want %q", i, got, tc.want)
		}
	}
}

func TestAccountExportTextTrailingNewline(t *testing.T) {
	got := AccountExportText([]models.MailAccount{{Email: "a@b.com"}, {Email: "c@d.com"}}, "")
	if got != "a@b.com\nc@d.com\n" {
		t.Errorf("got %q", got)
	}
}

func TestCPARefreshGate(t *testing.T) {
	accounts := []models.MailAccount{
		{Email: "a@b.com"},
		{Email: "c@d.com", OpenaiRT: "rt_live"},
	}
	if gate := CPARefreshGate("sub2api", accounts, nil); gate.Refresh || gate.Note != "" {
		t.Errorf("non-cpa format must not gate: %+v", gate)
	}
	// Format matching is case/space insensitive (Python does .strip().lower()).
	gate := CPARefreshGate("  CPA ", accounts, nil)
	if !gate.Refresh || len(gate.Refreshable) != 1 || gate.Refreshable[0].Email != "c@d.com" {
		t.Errorf("expected one refreshable account, got %+v", gate)
	}
	// The session payload's openai_rt is the fallback when the account has none.
	gate = CPARefreshGate("cpa", []models.MailAccount{{Email: "a@b.com"}}, map[string]string{"a@b.com": "rt.session"})
	if !gate.Refresh || len(gate.Refreshable) != 1 {
		t.Errorf("session RT should count: %+v", gate)
	}
	// No usable RT: proceed with the existing access tokens, but log a note.
	gate = CPARefreshGate("cpa", []models.MailAccount{{Email: "a@b.com", OpenaiRT: "legacy"}}, nil)
	if gate.Refresh || gate.Note == "" {
		t.Errorf("expected pass-through with note, got %+v", gate)
	}
	// Empty selection aborts the action (Python returns True).
	if gate := CPARefreshGate("cpa", nil, nil); !gate.Refresh || len(gate.Refreshable) != 0 {
		t.Errorf("empty selection must abort: %+v", gate)
	}
}

func TestFormatHelpers(t *testing.T) {
	if len(FormatOrder) != 7 {
		t.Fatalf("expected 7 formats, got %d", len(FormatOrder))
	}
	for _, key := range FormatOrder {
		if _, ok := FormatLabels[key]; !ok {
			t.Errorf("%q missing from FormatLabels", key)
		}
	}
	if NormalizeFormat(" CoCkPit ") != "cockpit" {
		t.Error("NormalizeFormat should trim and lowercase")
	}
	if NormalizeFormat("nope") != "sub2api" {
		t.Error("unknown format falls back to sub2api")
	}
	if FormatLabel("codexmanager") != "Codex-Manager" {
		t.Error("label mismatch")
	}
}

// json.dumps(..., ensure_ascii=False) leaves <, > and & alone; json.Marshal
// escapes them. Every dump in this package must behave like Python.
func TestDumpJSONDoesNotEscapeHTML(t *testing.T) {
	doc := NewOrderedMap().
		Set("id", "acct-<a&b>-111").
		Set("nested", NewOrderedMap().Set("v", "x>y"))
	got, err := DumpJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, `\u003c`) || strings.Contains(got, `\u0026`) || strings.Contains(got, `\u003e`) {
		t.Errorf("HTML escaping leaked: %s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("DumpJSON must end with the newline json.dumps(...) + \"\\n\" produces")
	}
	// Non-ASCII must stay literal (ensure_ascii=False).
	unicoded, err := DumpCompactJSON(NewOrderedMap().Set("k", "邮箱"))
	if err != nil {
		t.Fatal(err)
	}
	if unicoded != `{"k":"邮箱"}` {
		t.Errorf("got %s", unicoded)
	}
}

// Python dicts are ordered; Go maps are not. Guard the invariant directly.
func TestOrderedMapKeepsInsertionOrder(t *testing.T) {
	m := NewOrderedMap().Set("zeta", 1).Set("alpha", 2).Set("mid", 3)
	m.Set("zeta", 9) // re-assignment keeps the original position
	got, err := DumpCompactJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"zeta":9,"alpha":2,"mid":3}` {
		t.Errorf("got %s", got)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal([]byte(got), &roundTrip); err != nil {
		t.Fatal(err)
	}
}

func TestConvertRejectsRecordWithoutAccessToken(t *testing.T) {
	if _, err := ConvertChatGPTSessionRecord(map[string]any{"email": "a@b.com"}, "", goldenNow); err != ErrMissingAccessToken {
		t.Errorf("expected ErrMissingAccessToken, got %v", err)
	}
	// The UI prints the error verbatim into the skip list (app.py:24327).
	if ErrMissingAccessToken.Error() != "缺少 accessToken" {
		t.Errorf("message drifted: %q", ErrMissingAccessToken.Error())
	}
}

// json.Number keeps Python's str(int) shape; float64 would render 1.7e+09.
func TestParseSessionRecordUsesJSONNumber(t *testing.T) {
	rec, err := ParseSessionRecord([]byte(`{"updatedAt": 1786000000}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rec["updatedAt"].(json.Number); !ok {
		t.Fatalf("expected json.Number, got %T", rec["updatedAt"])
	}
	got, err := NormalizeISOTimestamp(rec["updatedAt"])
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-08-06T07:06:40.000Z" {
		t.Errorf("got %q", got)
	}
}

// strip_unavailable collapses an emptied dict to None but leaves an emptied
// list as [] (app.py:5155-5167).
func TestStripUnavailableEmptyContainers(t *testing.T) {
	if got := StripUnavailable(NewOrderedMap().Set("a", "").Set("b", nil)); got != nil {
		t.Errorf("emptied object should be nil, got %#v", got)
	}
	got := StripUnavailable([]any{nil, ""})
	list, ok := got.([]any)
	if !ok || len(list) != 0 {
		t.Errorf("emptied list should stay [], got %#v", got)
	}
}
