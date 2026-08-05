package export

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/sessionconv"
)

// testdata/python_golden.json is produced by
// scratchpad/pyapp/gen_export_golden.py, which imports a scratchpad COPY of
// app.py under CPython 3.12, stubs only tkinter's messagebox/filedialog and the
// App attributes the handlers read, pins datetime.now / time.time, and then
// runs the real export handlers — including the real
// Path.write_text(..., encoding="utf-8"), whose raw file bytes are captured.
// Nothing here hits the network or a paid API. If a shape disagrees, Python is
// right.

type goldenDoc struct {
	Title            string     `json:"title"`
	DefaultExtension string     `json:"default_extension"`
	FileTypes        [][]string `json:"filetypes"`
	Text             string     `json:"text"`
	FileB64          string     `json:"file_b64"`
	Logs             []string   `json:"logs"`
}

func (g goldenDoc) fileTypes() []FileType {
	out := make([]FileType, 0, len(g.FileTypes))
	for _, ft := range g.FileTypes {
		out = append(out, FileType{Label: ft[0], Pattern: ft[1]})
	}
	return out
}

func (g goldenDoc) fileBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(g.FileB64)
	if err != nil {
		t.Fatalf("decode file_b64: %v", err)
	}
	return raw
}

type goldenAccount struct {
	Email           string `json:"email"`
	Password        string `json:"password"`
	ClientID        string `json:"client_id"`
	RefreshToken    string `json:"refresh_token"`
	Raw             string `json:"raw"`
	OpenaiRT        string `json:"openai_rt"`
	AuthPhoneNumber string `json:"auth_phone_number"`
	AuthPhoneSMSURL string `json:"auth_phone_sms_url"`
	ReceiveMailbox  string `json:"receive_mailbox"`
	MailProvider    string `json:"mail_provider"`
}

type goldenInput struct {
	Prefix         string          `json:"prefix"`
	Accounts       []goldenAccount `json:"accounts"`
	SessionResults map[string]any  `json:"session_results"`
	Now            string          `json:"now"`
	ZipStamp       string          `json:"zip_stamp"`
	Access         string          `json:"access"`
	IDToken        string          `json:"id_token"`
}

func loadGolden(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("testdata/python_golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	return out
}

// decodeJSON uses UseNumber so session payload numbers arrive the way
// sessionconv.ParseSessionRecord delivers them.
func decodeJSON(t *testing.T, raw string, out any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func loadDoc(t *testing.T, g map[string]string, key string) goldenDoc {
	t.Helper()
	raw, ok := g[key]
	if !ok {
		t.Fatalf("golden key %q missing", key)
	}
	var doc goldenDoc
	decodeJSON(t, raw, &doc)
	return doc
}

func loadInput(t *testing.T, g map[string]string) (goldenInput, []models.MailAccount, SessionResults, time.Time) {
	t.Helper()
	var in goldenInput
	decodeJSON(t, g["input"], &in)
	accounts := make([]models.MailAccount, 0, len(in.Accounts))
	for _, a := range in.Accounts {
		accounts = append(accounts, models.MailAccount{
			Email:           a.Email,
			Password:        a.Password,
			ClientID:        a.ClientID,
			RefreshToken:    a.RefreshToken,
			Raw:             a.Raw,
			OpenaiRT:        a.OpenaiRT,
			AuthPhoneNumber: a.AuthPhoneNumber,
			AuthPhoneSMSURL: a.AuthPhoneSMSURL,
			ReceiveMailbox:  a.ReceiveMailbox,
			MailProvider:    a.MailProvider,
		})
	}
	now, err := time.Parse(time.RFC3339Nano, in.Now)
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	return in, accounts, SessionResults(in.SessionResults), now.UTC()
}

func checkDoc(t *testing.T, name string, got Document, want goldenDoc) {
	t.Helper()
	if got.Title != want.Title {
		t.Errorf("%s title = %q, want %q", name, got.Title, want.Title)
	}
	if got.DefaultExtension != want.DefaultExtension {
		t.Errorf("%s defaultextension = %q, want %q", name, got.DefaultExtension, want.DefaultExtension)
	}
	if !reflect.DeepEqual(got.FileTypes, want.fileTypes()) {
		t.Errorf("%s filetypes = %+v, want %+v", name, got.FileTypes, want.fileTypes())
	}
	if got.Text != want.Text {
		t.Errorf("%s text mismatch\n--- got ---\n%q\n--- want ---\n%q", name, got.Text, want.Text)
	}
	if wantBytes := want.fileBytes(t); !bytes.Equal(got.File, wantBytes) {
		t.Errorf("%s file bytes mismatch\n--- got ---\n%q\n--- want ---\n%q", name, got.File, wantBytes)
	}
}

// ---------------------------------------------------------------------------
// 导出选中Raw / 导出已授权邮箱 / 导出邮箱----RT
// ---------------------------------------------------------------------------

func TestRawMatchesPython(t *testing.T) {
	g := loadGolden(t)
	in, accounts, _, _ := loadInput(t, g)
	got, err := Raw(accounts, in.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	checkDoc(t, "raw", got, loadDoc(t, g, "raw"))
	if got.Count != len(accounts) {
		t.Errorf("count = %d, want %d", got.Count, len(accounts))
	}
}

func TestAuthorizedMatchesPython(t *testing.T) {
	g := loadGolden(t)
	in, accounts, _, _ := loadInput(t, g)
	got, err := Authorized(accounts, in.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	checkDoc(t, "authorized", got, loadDoc(t, g, "authorized"))
	// One of the five fixtures has no openai_rt.
	if got.Count != 4 {
		t.Errorf("count = %d, want 4", got.Count)
	}
}

func TestAuthorizedEmailRTMatchesPython(t *testing.T) {
	g := loadGolden(t)
	_, accounts, _, _ := loadInput(t, g)
	got, err := AuthorizedEmailRT(accounts)
	if err != nil {
		t.Fatal(err)
	}
	checkDoc(t, "email_rt", got, loadDoc(t, g, "email_rt"))
}

// ---------------------------------------------------------------------------
// 导出选中Session
// ---------------------------------------------------------------------------

func TestSessionsMatchesPython(t *testing.T) {
	g := loadGolden(t)
	_, accounts, results, _ := loadInput(t, g)
	got, err := Sessions(accounts, results)
	if err != nil {
		t.Fatal(err)
	}
	checkDoc(t, "sessions", got, loadDoc(t, g, "sessions"))
	if got.Count != 3 {
		t.Errorf("count = %d, want 3", got.Count)
	}
	// The whitespace-only session_json and the empty payload are `missing`.
	want := []string{"甘马@例子.中国", "delta@example.com"}
	if !reflect.DeepEqual(got.Skipped, want) {
		t.Errorf("skipped = %v, want %v", got.Skipped, want)
	}
	// The <, > and & of the fixture email must survive verbatim; json.Marshal
	// would have written < / > / & here.
	if !bytes.Contains(got.File, []byte(`"beta&<b>@example.com"`)) {
		t.Error("HTML characters were escaped in the session export")
	}
	// ensure_ascii=False: the non-ASCII note inside session_json stays literal.
	if !bytes.Contains(got.File, []byte("中文")) {
		t.Error("non-ASCII was escaped in the session export")
	}
	// The fixture note carries a U+2028, which encoding/json escapes and
	// json.dumps does not.
	if !bytes.Contains(got.File, []byte("\u2028")) {
		t.Error("U+2028 was escaped in the session export")
	}
	if bytes.Contains(got.File, []byte(`\u2028`)) {
		t.Error("U+2028 escape leaked into the session export")
	}
}

func TestRestoreLineSeparators(t *testing.T) {
	cases := [][2]string{
		{`"a\u2028b"`, "\"a\u2028b\""},
		{`"a\u2029b"`, "\"a\u2029b\""},
		{`"a"`, `"a"`},
		// An escaped backslash must not be re-read as the start of an escape:
		// the input below is a JSON string holding one backslash followed by
		// the five ordinary characters u2028.
		{`"a\\u2028b"`, `"a\\u2028b"`},
		// Neighbouring escapes are left alone.
		{`"x\u2026y\u2027z"`, `"x\u2026y\u2027z"`},
	}
	for i, tc := range cases {
		if got := restoreLineSeparators(tc[0]); got != tc[1] {
			t.Errorf("case %d: restoreLineSeparators(%q) = %q, want %q", i, tc[0], got, tc[1])
		}
	}
}

// ---------------------------------------------------------------------------
// 导出 Session 转换 (all seven formats + an unknown one)
// ---------------------------------------------------------------------------

func TestSessionConversionMatchesPython(t *testing.T) {
	g := loadGolden(t)
	_, accounts, results, now := loadInput(t, g)
	formats := append(append([]string{}, sessionconv.FormatOrder...), "NONSENSE")
	for _, format := range formats {
		got, err := SessionConversion(accounts, results, format, now)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		checkDoc(t, "conv:"+format, got, loadDoc(t, g, "conv:"+format))
		if got.Count != 4 {
			t.Errorf("%s count = %d, want 4", format, got.Count)
		}
		if want := []string{"delta@example.com"}; !reflect.DeepEqual(got.Skipped, want) {
			t.Errorf("%s skipped = %v, want %v", format, got.Skipped, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 导出 Session 转换 ZIP
// ---------------------------------------------------------------------------

type goldenZip struct {
	Dialog struct {
		Title            string     `json:"title"`
		DefaultExtension string     `json:"defaultextension"`
		InitialFile      string     `json:"initialfile"`
		FileTypes        [][]string `json:"filetypes"`
	} `json:"dialog"`
	Entries []struct {
		Name    string `json:"name"`
		DataB64 string `json:"data_b64"`
	} `json:"entries"`
	Logs []string `json:"logs"`
}

func TestSessionConversionZIPMatchesPython(t *testing.T) {
	g := loadGolden(t)
	_, accounts, results, now := loadInput(t, g)
	for _, format := range []string{"sub2api", "codexmanager"} {
		var want goldenZip
		decodeJSON(t, g["zip:"+format], &want)

		got, err := SessionConversionZIP(accounts, results, format, now)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if got.Title != want.Dialog.Title {
			t.Errorf("%s title = %q, want %q", format, got.Title, want.Dialog.Title)
		}
		if got.DefaultExtension != want.Dialog.DefaultExtension {
			t.Errorf("%s ext = %q, want %q", format, got.DefaultExtension, want.Dialog.DefaultExtension)
		}
		if got.SuggestedName != want.Dialog.InitialFile {
			t.Errorf("%s initialfile = %q, want %q", format, got.SuggestedName, want.Dialog.InitialFile)
		}
		wantTypes := make([]FileType, 0, len(want.Dialog.FileTypes))
		for _, ft := range want.Dialog.FileTypes {
			wantTypes = append(wantTypes, FileType{ft[0], ft[1]})
		}
		if !reflect.DeepEqual(got.FileTypes, wantTypes) {
			t.Errorf("%s filetypes = %+v, want %+v", format, got.FileTypes, wantTypes)
		}
		if len(got.Entries) != len(want.Entries) {
			t.Fatalf("%s entries = %d, want %d", format, len(got.Entries), len(want.Entries))
		}
		for i, entry := range got.Entries {
			if entry.Name != want.Entries[i].Name {
				t.Errorf("%s entry[%d] name = %q, want %q", format, i, entry.Name, want.Entries[i].Name)
			}
			wantData, err := base64.StdEncoding.DecodeString(want.Entries[i].DataB64)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(entry.Data, wantData) {
				t.Errorf("%s entry[%d] %s bytes mismatch\n--- got ---\n%q\n--- want ---\n%q",
					format, i, entry.Name, entry.Data, wantData)
			}
			// zipfile.writestr does not translate newlines: LF even on Windows.
			if bytes.Contains(entry.Data, []byte("\r\n")) {
				t.Errorf("%s entry[%d] must stay LF inside the archive", format, i)
			}
		}
	}
}

func TestArchiveBytesRoundTrips(t *testing.T) {
	g := loadGolden(t)
	_, accounts, results, now := loadInput(t, g)
	archive, err := SessionConversionZIP(accounts, results, "codexmanager", now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := archive.Bytes(now)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != len(archive.Entries) {
		t.Fatalf("archive has %d files, want %d", len(reader.File), len(archive.Entries))
	}
	for i, file := range reader.File {
		if file.Name != archive.Entries[i].Name {
			t.Errorf("file[%d] = %q, want %q", i, file.Name, archive.Entries[i].Name)
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
		if !bytes.Equal(buf.Bytes(), archive.Entries[i].Data) {
			t.Errorf("file[%d] %s content differs after round trip", i, file.Name)
		}
	}
}

func TestZipSuggestedNameUsesLocalClock(t *testing.T) {
	g := loadGolden(t)
	in, _, _, now := loadInput(t, g)
	want := "session-conversion-cpa-" + in.ZipStamp + ".zip"
	if got := ZipSuggestedName("cpa", now); got != want {
		t.Errorf("ZipSuggestedName = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// 导出 sub2api JSON
// ---------------------------------------------------------------------------

type goldenSub2API struct {
	Records []map[string]any `json:"records"`
	Text    string           `json:"text"`
	FileB64 string           `json:"file_b64"`
}

func TestSub2APIMatchesPython(t *testing.T) {
	g := loadGolden(t)
	in, accounts, _, now := loadInput(t, g)
	var want goldenSub2API
	decodeJSON(t, g["sub2api"], &want)

	// app.py:24519-24529 with the network refresh replaced by the same fixed
	// payload the generator used.
	selected, err := Sub2APISelection(accounts)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, account := range selected {
		payload := map[string]any{
			"access_token":  in.Access,
			"id_token":      in.IDToken,
			"refresh_token": "rt_from_server",
		}
		refreshed := pyStrOr(payload["refresh_token"])
		if sessionconv.IsOpenAIRefreshToken(refreshed) {
			account.OpenaiRT = refreshed
		}
		payload["refresh_token"] = account.OpenaiRT
		record, err := RecordFromRefreshPayload(Sub2APIExportEmail(in.Prefix, account.Email), payload, now)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) != len(want.Records) {
		t.Fatalf("records = %d, want %d", len(records), len(want.Records))
	}
	for i, record := range records {
		wantRecord := map[string]any{}
		for k, v := range want.Records[i] {
			wantRecord[k] = v
		}
		if !reflect.DeepEqual(record, wantRecord) {
			t.Errorf("record[%d] mismatch\n got: %#v\nwant: %#v", i, record, wantRecord)
		}
	}

	got, err := Sub2API(records, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != want.Text {
		t.Errorf("sub2api text mismatch\n--- got ---\n%s\n--- want ---\n%s", got.Text, want.Text)
	}
	wantBytes, err := base64.StdEncoding.DecodeString(want.FileB64)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.File, wantBytes) {
		t.Errorf("sub2api file bytes mismatch\n--- got ---\n%q\n--- want ---\n%q", got.File, wantBytes)
	}
	if got.Title != "导出 sub2api JSON" || got.DefaultExtension != ".sub2api.json" {
		t.Errorf("dialog = %q / %q", got.Title, got.DefaultExtension)
	}
	wantTypes := []FileType{{"sub2api JSON", "*.sub2api.json"}, {"JSON", "*.json"}, {"All", "*.*"}}
	if !reflect.DeepEqual(got.FileTypes, wantTypes) {
		t.Errorf("filetypes = %+v", got.FileTypes)
	}
}

// ---------------------------------------------------------------------------
// Warnings, prompts and skip notes
// ---------------------------------------------------------------------------

func TestMessagesMatchPython(t *testing.T) {
	g := loadGolden(t)
	var want map[string]string
	decodeJSON(t, g["messages"], &want)

	cases := map[string]error{
		"no_selection":             ErrNoSelection,
		"no_authorized_rt":         ErrNoAuthorizedRT,
		"no_authorized_rt_email":   ErrNoAuthorizedRT,
		"no_session_json":          ErrNoSessionJSON,
		"no_convertible_token":     ErrNoConvertibleToken,
		"no_convertible_token_zip": ErrNoConvertibleToken, // gitleaks:allow，测试用例名不是凭据
		"no_sub2api_records":       ErrNoSub2APIRecords,   // gitleaks:allow，测试用例名不是凭据
		"no_authorized_selection":  ErrNoAuthorizedSelection,
	}
	for key, err := range cases {
		if err.Error() != want[key] {
			t.Errorf("%s = %q, want %q", key, err.Error(), want[key])
		}
	}

	var missing []models.MailAccount
	for i := 0; i < 15; i++ {
		missing = append(missing, models.MailAccount{Email: fmt.Sprintf("m%02d@example.com", i)})
	}
	if got := MissingRTPrompt(missing); got != want["missing_rt_prompt_15"] {
		t.Errorf("missing prompt (15)\n got: %q\nwant: %q", got, want["missing_rt_prompt_15"])
	}
	if got := MissingRTPrompt(missing[:3]); got != want["missing_rt_prompt_3"] {
		t.Errorf("missing prompt (3)\n got: %q\nwant: %q", got, want["missing_rt_prompt_3"])
	}
	if got := MissingRTPrompt(nil); got != "" {
		t.Errorf("empty missing list must produce no prompt, got %q", got)
	}
}

func TestSkipNotesMatchPython(t *testing.T) {
	g := loadGolden(t)
	var want map[string]string
	decodeJSON(t, g["skip_notes"], &want)
	var many []string
	for i := 0; i < 7; i++ {
		many = append(many, fmt.Sprintf("s%d@example.com", i))
	}
	if got := SkippedNote("Session 转换", many); got != want["many"] {
		t.Errorf("many\n got: %q\nwant: %q", got, want["many"])
	}
	if got := SkippedNote("Session 转换", many[:2]); got != want["few"] {
		t.Errorf("few\n got: %q\nwant: %q", got, want["few"])
	}
	if got := SkippedNote("Session 转换 ZIP", many); got != want["zip_many"] {
		t.Errorf("zip\n got: %q\nwant: %q", got, want["zip_many"])
	}
	if got := SkippedNote("Session 转换", nil); got != "" {
		t.Errorf("empty skip list must produce no note, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Guards
// ---------------------------------------------------------------------------

func TestEmptySelectionIsRefused(t *testing.T) {
	for name, call := range map[string]func() error{
		"raw":        func() error { _, err := Raw(nil, ""); return err },
		"authorized": func() error { _, err := Authorized(nil, ""); return err },
		"email_rt":   func() error { _, err := AuthorizedEmailRT(nil); return err },
		"sessions":   func() error { _, err := Sessions(nil, nil); return err },
		"conversion": func() error { _, err := SessionConversion(nil, nil, "cpa", time.Time{}); return err },
		"zip":        func() error { _, err := SessionConversionZIP(nil, nil, "cpa", time.Time{}); return err },
		"sub2api":    func() error { _, err := Sub2APISelection(nil); return err },
	} {
		if err := call(); !errors.Is(err, ErrNoSelection) {
			t.Errorf("%s: err = %v, want ErrNoSelection", name, err)
		}
	}
	if _, err := Sub2API(nil, time.Time{}); !errors.Is(err, ErrNoSub2APIRecords) {
		t.Errorf("Sub2API(nil) = %v, want ErrNoSub2APIRecords", err)
	}
}

func TestUnauthorizedSelectionIsRefused(t *testing.T) {
	accounts := []models.MailAccount{{Email: "x@y.z"}}
	if _, err := Authorized(accounts, ""); !errors.Is(err, ErrNoAuthorizedRT) {
		t.Errorf("Authorized = %v", err)
	}
	if _, err := AuthorizedEmailRT(accounts); !errors.Is(err, ErrNoAuthorizedRT) {
		t.Errorf("AuthorizedEmailRT = %v", err)
	}
	if _, err := Sub2APISelection(accounts); !errors.Is(err, ErrNoAuthorizedRT) {
		t.Errorf("Sub2APISelection = %v", err)
	}
	// Raw exports the unauthorized account anyway (app.py:24406 has no filter).
	if doc, err := Raw(accounts, ""); err != nil || doc.Count != 1 {
		t.Errorf("Raw = %+v, %v", doc, err)
	}
}

func TestNoConvertibleTokenIsRefused(t *testing.T) {
	accounts := []models.MailAccount{{Email: "x@y.z", OpenaiRT: "rt_x"}}
	if _, err := SessionConversion(accounts, nil, "cpa", time.Time{}); !errors.Is(err, ErrNoConvertibleToken) {
		t.Errorf("SessionConversion = %v", err)
	}
	if _, err := SessionConversionZIP(accounts, nil, "cpa", time.Time{}); !errors.Is(err, ErrNoConvertibleToken) {
		t.Errorf("SessionConversionZIP = %v", err)
	}
	if _, err := Sessions(accounts, nil); !errors.Is(err, ErrNoSessionJSON) {
		t.Errorf("Sessions = %v", err)
	}
}

func TestRecordFromRefreshPayloadRejects(t *testing.T) {
	if _, err := RecordFromRefreshPayload("e@x.com", map[string]any{}, time.Time{}); !errors.Is(err, ErrRefreshMissingAccessToken) {
		t.Errorf("missing access token: %v", err)
	}
	g := loadGolden(t)
	in, _, _, now := loadInput(t, g)
	payload := map[string]any{"access_token": in.Access, "refresh_token": "not-a-token"}
	if _, err := RecordFromRefreshPayload("e@x.com", payload, now); !errors.Is(err, ErrRefreshMissingRefreshToken) {
		t.Errorf("bad refresh token: %v", err)
	}
}

// NativeNewlineBytes is the one place where the port must be OS-aware, so pin
// both branches rather than only the one this machine exercises.
func TestNativeNewlineBytes(t *testing.T) {
	prev := nativeLineSeparator
	t.Cleanup(func() { nativeLineSeparator = prev })

	nativeLineSeparator = "\n"
	if got := string(NativeNewlineBytes("a\nb\n")); got != "a\nb\n" {
		t.Errorf("posix: %q", got)
	}
	nativeLineSeparator = "\r\n"
	if got := string(NativeNewlineBytes("a\nb\n")); got != "a\r\nb\r\n" {
		t.Errorf("windows: %q", got)
	}
	// io.TextIOWrapper translates every \n unconditionally, so an existing CRLF
	// becomes CR CRLF. Documented, not desirable.
	if got := string(NativeNewlineBytes("a\r\n")); got != "a\r\r\n" {
		t.Errorf("windows CRLF passthrough: %q", got)
	}
}

func TestPyStripMatchesPythonStrip(t *testing.T) {
	// U+001C..U+001F are whitespace to Python's str.strip() but not to
	// strings.TrimSpace.
	if got := pyStrip("\x1c\x1f x \x1d"); got != "x" {
		t.Errorf("pyStrip = %q", got)
	}
	if got := pyStrip(" 　x\n"); got != "x" {
		t.Errorf("pyStrip unicode = %q", got)
	}
}
