package sessionconv

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// testdata/python_golden.json was produced by exec'ing the real app.py source
// ranges (2649-2674, 4921-4940, 5017-5059, 5062-5446, 5449-5451, 5684-5696)
// under CPython 3.12 with time.time() pinned to goldenUnix and now pinned to
// goldenNow, then dumping every builder's json.dumps(..., ensure_ascii=False,
// indent=2) + "\n". If a shape here ever disagrees, Python is right.
const (
	goldenUnix = int64(1785000000)
)

var goldenNow = time.Date(2026, 7, 26, 12, 34, 56, 789000000, time.UTC)

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

// pinClock pins the wall clock synthetic_codex_id_token reads (app.py:5192).
func pinClock(t *testing.T) {
	t.Helper()
	prev := sessionconvNowUnix
	sessionconvNowUnix = func() int64 { return goldenUnix }
	t.Cleanup(func() { sessionconvNowUnix = prev })
}

func goldenRecords(t *testing.T, g map[string]string) map[string]map[string]any {
	t.Helper()
	access, err := json.Marshal(g["ACCESS"])
	if err != nil {
		t.Fatal(err)
	}
	idTok, err := json.Marshal(g["IDTOK"])
	if err != nil {
		t.Fatal(err)
	}
	a, i := string(access), string(idTok)
	sources := map[string]string{
		"full": `{
			"accessToken": ` + a + `, "idToken": ` + i + `, "refresh_token": "rt_abc123",
			"user": {"email": "user@example.com", "id": "uid-1"},
			"account": {"id": "acct-top", "workspaceId": "ws-1", "planType": "team"},
			"expires": "2026-08-25T09:19:37.911Z",
			"priority": "3", "isActive": false, "disabled": true,
			"createdAt": "2026-01-02T03:04:05.006Z", "updatedAt": 1786000000,
			"account_note": "note <x>", "authProvider": "google"
		}`,
		"no_refresh": `{"access_token": ` + a + `, "email": "norefresh@example.com"}`,
		"codex_9router": `{
			"provider": "codex", "authType": "oauth", "id": "acct-9r",
			"tokens": {"accessToken": ` + a + `, "refreshToken": "rt.xyz"},
			"meta": {"label": "meta@example.com", "chatgpt_account_id": "cga-1"},
			"priority": 0
		}`,
		"minimal": `{"accessToken": ` + a + `}`,
	}
	out := map[string]map[string]any{}
	for name, src := range sources {
		rec, err := ParseSessionRecord([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = rec
	}
	return out
}

// convertedAsPythonDict rebuilds the literal returned by
// convert_chatgpt_session_record (app.py:5412-5424) so the whole intermediate
// can be diffed against Python in one shot.
func convertedAsPythonDict(c Converted) *OrderedMap {
	return NewOrderedMap().
		Set("email", c.Email).
		Set("name", c.Name).
		Set("expiresAt", c.ExpiresAt).
		Set("accessTokenExpiresAt", c.AccessTokenExpiresAt).
		Set("cpa", c.CPA).
		Set("cockpit", c.Cockpit).
		Set("nineRouter", c.NineRouter).
		Set("codexAuthJson", c.CodexAuthJSON).
		Set("axonHub", c.AxonHub).
		Set("codexManager", c.CodexManager).
		Set("sub2apiAccount", c.Sub2APIAccount)
}

func TestConvertMatchesPythonGolden(t *testing.T) {
	pinClock(t)
	g := loadGolden(t)
	records := goldenRecords(t, g)
	for _, name := range []string{"full", "no_refresh", "codex_9router", "minimal"} {
		converted, err := ConvertChatGPTSessionRecord(records[name], "source@example.com", goldenNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := DumpJSON(convertedAsPythonDict(converted))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := g["convert:"+name]; got != want {
			t.Errorf("convert:%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
		}
	}
}

func TestDocumentMatchesPythonGolden(t *testing.T) {
	pinClock(t)
	g := loadGolden(t)
	records := goldenRecords(t, g)
	one, err := ConvertChatGPTSessionRecord(records["full"], "source@example.com", goldenNow)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ConvertChatGPTSessionRecord(records["minimal"], "source@example.com", goldenNow)
	if err != nil {
		t.Fatal(err)
	}
	// "unknown" exercises key_map.get(fmt, "sub2apiAccount") — the bare sub2api
	// account with NO exported_at/proxies envelope.
	for _, format := range []string{"sub2api", "cpa", "cockpit", "9router", "codex", "axonhub", "codexmanager", "unknown"} {
		got1, err := DumpJSON(BuildSessionConversionDocument([]Converted{one}, format, goldenNow))
		if err != nil {
			t.Fatal(err)
		}
		if want := g["doc1:"+format]; got1 != want {
			t.Errorf("doc1:%s mismatch\n--- got ---\n%s\n--- want ---\n%s", format, got1, want)
		}
		got2, err := DumpJSON(BuildSessionConversionDocument([]Converted{one, two}, format, goldenNow))
		if err != nil {
			t.Fatal(err)
		}
		if want := g["doc2:"+format]; got2 != want {
			t.Errorf("doc2:%s mismatch\n--- got ---\n%s\n--- want ---\n%s", format, got2, want)
		}
	}
}

func TestSub2APIExportMatchesPythonGolden(t *testing.T) {
	pinClock(t)
	g := loadGolden(t)
	records := []map[string]any{
		{
			"access_token": g["ACCESS"], "id_token": g["IDTOK"], "refresh_token": "rt_zzz",
			"expired": "2027-04-15T00:00:00Z", "email": "exp@example.com",
			"account_id": "acct-rec", "plan_type": "plus",
		},
		{"access_token": g["ACCESS"]},
	}
	// build_sub2api_export stamps exported_at from datetime.now() internally
	// (app.py:5056) and takes no clock argument, so the golden captured the
	// generator's real run time. Go accepts `now` explicitly; feed it back.
	var envelope struct {
		ExportedAt string `json:"exported_at"`
	}
	if err := json.Unmarshal([]byte(g["sub2api_export"]), &envelope); err != nil {
		t.Fatal(err)
	}
	stamp, err := time.Parse("2006-01-02T15:04:05Z", envelope.ExportedAt)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DumpJSON(BuildSub2APIExport(records, stamp))
	if err != nil {
		t.Fatal(err)
	}
	if want := g["sub2api_export"]; got != want {
		t.Errorf("sub2api_export mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestZipEntryNamesMatchPythonGolden(t *testing.T) {
	g := loadGolden(t)
	var want []string
	if err := json.Unmarshal([]byte(g["zipnames"]), &want); err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	inputs := [][2]string{
		{"A.B@Gmail.com", "9router"},
		{"A.B@Gmail.com", "9router"},
		{"a-b@gmail.com", "9router"},
		{"", "codexmanager"},
		{"x@y.z", ""},
		{"邮箱@例子.中国", "!!!"},
	}
	var got []string
	for _, in := range inputs {
		got = append(got, SessionConversionZipEntryName(in[0], in[1], used))
	}
	// usedNames == nil skips de-duplication entirely, so the first name repeats.
	got = append(got, SessionConversionZipEntryName("A.B@Gmail.com", "9router", nil))
	if len(got) != len(want) {
		t.Fatalf("length %d != %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("zip name[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScalarHelpersMatchPythonGolden(t *testing.T) {
	pinClock(t)
	g := loadGolden(t)
	var want map[string]json.RawMessage
	if err := json.Unmarshal([]byte(g["helpers"]), &want); err != nil {
		t.Fatal(err)
	}
	num := func(s string) json.Number { return json.Number(s) }
	check := func(key string, value any) {
		t.Helper()
		got, err := DumpCompactJSON(value)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		var compact any
		if err := json.Unmarshal(want[key], &compact); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		wantCompact, err := DumpCompactJSON(compact)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got != wantCompact {
			t.Errorf("%s\n got: %s\nwant: %s", key, got, wantCompact)
		}
	}

	// iso/tsu drop the ErrTimestampOutOfRange arm; none of these inputs can
	// reach it (TestTimestampOutOfRange covers the ones that do).
	iso := func(v any) string {
		text, err := NormalizeISOTimestamp(v)
		if err != nil {
			t.Fatalf("NormalizeISOTimestamp(%v): %v", v, err)
		}
		return text
	}
	tsu := func(v any) string {
		text, err := TimestampFromUnixSeconds(v)
		if err != nil {
			t.Fatalf("TimestampFromUnixSeconds(%v): %v", v, err)
		}
		return text
	}
	check("normalize_iso_timestamp", []any{
		iso(nil),
		iso(""),
		iso("2026-08-25T09:19:37.911Z"),
		iso(num("1786000000")),
		iso(num("1786000000123")),
		iso(num("0")),
		iso(num("-5")),
		iso("garbage"),
		iso("2026-08-25"),
		iso("2026-08-25T09:19:37"),
	})
	check("timestamp_from_unix_seconds", []any{
		tsu("1786000000"),
		tsu(num("1786000000")),
		tsu(num("0")),
		tsu(num("-1")),
		tsu("x"),
	})
	check("unix_seconds_from_jwt_exp", []any{
		UnixSecondsFromJWTExp("1786000000.9"),
		UnixSecondsFromJWTExp(num("1786000000")),
		UnixSecondsFromJWTExp(num("0")),
		UnixSecondsFromJWTExp(num("-3")),
		UnixSecondsFromJWTExp(nil),
	})
	check("epoch_seconds_from_value", []any{
		EpochSecondsFromValue(nil),
		EpochSecondsFromValue(""),
		EpochSecondsFromValue("2026-08-25T09:19:37.911Z"),
		EpochSecondsFromValue(num("1786000000123")),
		EpochSecondsFromValue("1786000000"),
		EpochSecondsFromValue("junk"),
	})
	check("classify_chatgpt_plan_text", []any{
		ClassifyPlanText(""),
		ClassifyPlanText("ChatGPT-Plus_Plan"),
		ClassifyPlanText("chatgptfreeplan"),
		ClassifyPlanText("Team"),
		ClassifyPlanText("K12"),
		ClassifyPlanText("weird"),
	})
	check("email_key", []any{
		EmailKey("A.B@Gmail.com"), EmailKey("__a__"), EmailKey("邮箱@例子"), EmailKey(""),
	})
	check("is_openai_refresh_token", []any{
		IsOpenAIRefreshToken("rt_a"), IsOpenAIRefreshToken("rt.a"),
		IsOpenAIRefreshToken(" rt_a"), IsOpenAIRefreshToken("xx"),
	})
	check("get_expires_in", []any{
		GetExpiresIn("", goldenNow),
		GetExpiresIn("2026-07-26T13:34:56.000Z", goldenNow),
		GetExpiresIn("2026-07-26T11:34:56.000Z", goldenNow),
		GetExpiresIn("junk", goldenNow),
	})
	check("get_axonhub_last_refresh", []any{
		GetAxonHubLastRefresh("", goldenNow),
		GetAxonHubLastRefresh("2026-08-25T09:19:37.911Z", goldenNow),
		GetAxonHubLastRefresh("junk", goldenNow),
	})
	check("parse_expired_time", []any{
		ParseExpiredTime(""),
		ParseExpiredTime("2027-04-15T00:00:00Z"),
		ParseExpiredTime("2027-04-15T00:00:00+02:00"),
		ParseExpiredTime("junk"),
	})
	check("synthetic", SyntheticCodexIDToken("e@x.com", "acct-1", "plus", "u-1", "2027-04-15T00:00:00Z"))
	check("synthetic_noacct", SyntheticCodexIDToken("e@x.com", "", "plus", "u-1", ""))
	check("strip_unavailable", StripUnavailable(NewOrderedMap().
		Set("a", "").
		Set("b", 0).
		Set("c", false).
		Set("d", NewOrderedMap().Set("e", "")).
		Set("f", []any{nil, "", "g"}).
		Set("h", nil)))
}
