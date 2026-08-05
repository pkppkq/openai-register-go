package logs

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The "[email] message" convention is the only channel a bare-string producer
// (app.py:17711, 17725-17726, 21989) has for addressing a line, so Prefix and
// the drain-time parse must be exact inverses.
func TestPrefixRoundTrip(t *testing.T) {
	cases := []struct {
		email   string
		message string
		wire    string
		key     string // what the drain path routes it to
		text    string // what survives into the record, pre-module
	}{
		// app.py:11922-11923 / 12667-12668, the amount-check line.
		{
			email:   "A@B.com",
			message: "金额检查通过: 目标 1, 实际 1, 来源 未知",
			wire:    "[A@B.com] 金额检查通过: 目标 1, 实际 1, 来源 未知",
			key:     "a@b.com",
			text:    "金额检查通过: 目标 1, 实际 1, 来源 未知",
		},
		// No address: the ternary yields an empty prefix, nothing to parse back.
		{
			email:   "",
			message: "金额检查通过",
			wire:    "金额检查通过",
			key:     "",
			text:    "金额检查通过",
		},
		// A whitespace-bearing address cannot round-trip: the bracket regex
		// (app.py:18408) excludes \s, so the prefix stays in the text and the
		// line goes global. Python has the same hole; it is pinned, not fixed.
		{
			email:   "a b@c.com",
			message: "金额检查通过",
			wire:    "[a b@c.com] 金额检查通过",
			key:     "",
			text:    "[a b@c.com] 金额检查通过",
		},
	}
	for _, c := range cases {
		wire := Prefix(c.email, c.message)
		if wire != c.wire {
			t.Errorf("Prefix(%q, %q) = %q, want %q", c.email, c.message, wire, c.wire)
		}
		email, text := SplitPrefix(wire)
		if email != c.key || text != c.text {
			t.Errorf("SplitPrefix(%q) = (%q, %q), want (%q, %q)", wire, email, text, c.key, c.text)
		}
		if got := Route(wire, ""); got != c.key {
			t.Errorf("Route(%q) = %q, want %q", wire, got, c.key)
		}
		// A round-tripped line must classify identically to the un-prefixed one.
		if a, b := Normalize(wire), Normalize(c.text); a != b {
			t.Errorf("Normalize disagrees across the prefix: %q vs %q", a, b)
		}
	}

	// app.py:18408 anchors on the RAW message, but app.py:18487 cuts from the
	// STRIPPED one — one leading blank splits the two halves apart.
	if email, text := SplitPrefix(" [a@b] x"); email != "" || text != "x" {
		t.Errorf("leading-space asymmetry lost: (%q, %q)", email, text)
	}
	// Only the first bracket is consumed (app.py:18487 is not global).
	if email, text := SplitPrefix("[a@b] [c@d] x"); email != "a@b" || text != "[c@d] x" {
		t.Errorf("second bracket must survive: (%q, %q)", email, text)
	}
}

// app.py:18444-18456.
func TestCoerce(t *testing.T) {
	// dict branch, "message" wins over "msg" (18448)
	if got := (Payload{Message: "a", Msg: "b", Email: "X@Y"}).Entry(); got != (Entry{Message: "a", Email: "x@y"}) {
		t.Errorf("Payload.Entry = %+v", got)
	}
	// empty "message" falls through to "msg" — Python's `or`, not a key test
	if got := (Payload{Msg: "b"}).Entry(); got != (Entry{Message: "b"}) {
		t.Errorf("msg fallback = %+v", got)
	}
	// no address in the payload: infer from the bracket (18454-18455)
	if got := (Payload{Message: "[a@b] hi"}).Entry(); got != (Entry{Message: "[a@b] hi", Email: "a@b"}) {
		t.Errorf("payload inference = %+v", got)
	}
	// an explicit address beats the bracket
	if got := (Payload{Message: "[a@b] hi", Email: "c@d"}).Entry(); got.Email != "c@d" {
		t.Errorf("explicit email = %+v", got)
	}
	// map form
	if got := CoerceMap(map[string]any{"msg": "hi", "email": " A@B "}); got != (Entry{Message: "hi", Email: "a@b"}) {
		t.Errorf("CoerceMap = %+v", got)
	}
	// falsy values behave like missing ones
	if got := CoerceMap(map[string]any{"message": nil, "msg": "", "email": false}); got != (Entry{}) {
		t.Errorf("CoerceMap falsy = %+v", got)
	}
	if got := CoerceMap(nil); got != (Entry{}) {
		t.Errorf("CoerceMap(nil) = %+v", got)
	}
	// tuple branch: ("log", msg) and ("log", msg, email) collapse (18450-18453)
	if got := CoerceText("[a@b] hi", ""); got != (Entry{Message: "[a@b] hi", Email: "a@b"}) {
		t.Errorf("CoerceText 2-tuple = %+v", got)
	}
	if got := CoerceText("hi", " C@D "); got != (Entry{Message: "hi", Email: "c@d"}) {
		t.Errorf("CoerceText 3-tuple = %+v", got)
	}
}

// REGRESSION (defect: CoerceMap stringified each field and then tested for "").
// app.py:18448 evaluates `payload.get("message") or payload.get("msg")` on the
// RAW values and calls str() on the WINNER, so a numeric 0 loses the `or` — but
// str(0) is "0", which is not empty, so the old code let it win. Same trap on
// the address: {"email": 0} is a GLOBAL line in Python and used to be filed
// under an account literally named "0".
//
// Every `wantMsg`/`wantEmail` below is what the verbatim body of
// _coerce_log_event returns under CPython 3.12 for that dict.
func TestCoerceMapUsesPythonOrSemantics(t *testing.T) {
	cases := []struct {
		payload            map[string]any
		wantMsg, wantEmail string
	}{
		{map[string]any{"message": 0.0, "msg": "x"}, "x", ""},
		{map[string]any{"message": "", "msg": "x"}, "x", ""},
		{map[string]any{"message": false, "msg": "x"}, "x", ""},
		{map[string]any{"message": 1.0, "msg": "x"}, "1", ""},
		{map[string]any{"message": 1.5, "msg": "x"}, "1.5", ""},
		{map[string]any{"message": true, "msg": "x"}, "True", ""},
		{map[string]any{"message": 0.0, "msg": 0.0}, "", ""},
		{map[string]any{"msg": "y", "email": 0.0}, "y", ""},
		{map[string]any{"msg": "y", "email": false}, "y", ""},
		{map[string]any{"msg": "y", "email": 1.0}, "y", "1"},
		{map[string]any{"msg": "y", "email": true}, "y", "true"}, // str(True).lower()
		{map[string]any{"msg": "[a@b] hi", "email": 0.0}, "[a@b] hi", "a@b"},
		{map[string]any{"message": "[a@b] hi", "email": ""}, "[a@b] hi", "a@b"},
		{map[string]any{"email": "A@B.COM"}, "", "a@b.com"},
		{map[string]any{"message": 0.0}, "", ""},
		{map[string]any{"message": nil, "msg": nil, "email": nil}, "", ""},
		{map[string]any{}, "", ""},
	}
	for _, c := range cases {
		got := CoerceMap(c.payload)
		if got.Message != c.wantMsg || got.Email != c.wantEmail {
			t.Errorf("CoerceMap(%v) = (%q,%q), want (%q,%q)",
				c.payload, got.Message, got.Email, c.wantMsg, c.wantEmail)
		}
	}
	// json.Decoder.UseNumber keeps Python's int/float distinction, which the
	// float64 branch above cannot: str(1) is "1" but str(1.0) is "1.0".
	if got := CoerceMap(map[string]any{"message": json.Number("1.0")}); got.Message != "1.0" {
		t.Errorf("json.Number(1.0) = %q, want %q", got.Message, "1.0")
	}
	if got := CoerceMap(map[string]any{"message": json.Number("1")}); got.Message != "1" {
		t.Errorf("json.Number(1) = %q, want %q", got.Message, "1")
	}
	if got := CoerceMap(map[string]any{"message": json.Number("0")}); got.Message != "" {
		t.Errorf("json.Number(0) is falsy in Python's `or`, got %q", got.Message)
	}
}

// DELIBERATE DIVERGENCE, pinned so nobody "fixes" it into a repr port.
// Python's str() of a container is its repr; reproducing that needs the whole
// repr grammar. What this call site actually depends on — which field wins the
// `or`, and whether the line is routed — is exact, so only the rendered text of
// a container differs, and no producer puts one on the queue (app.py:18435 and
// 18442 both str() first).
func TestCoerceMapContainerStandIn(t *testing.T) {
	if got := CoerceMap(map[string]any{"message": []any{1.0}}); got.Message != "[...]" {
		t.Errorf("list stand-in = %q (CPython would say %q)", got.Message, "[1]")
	}
	if got := CoerceMap(map[string]any{"message": map[string]any{"a": 1.0}}); got.Message != "{...}" {
		t.Errorf("dict stand-in = %q (CPython would say %q)", got.Message, "{'a': 1}")
	}
	// Emptiness is still exact, because that is what the `or` reads.
	if got := CoerceMap(map[string]any{"message": []any{}, "msg": "x"}); got.Message != "x" {
		t.Errorf("empty list must lose the `or`, got %q", got.Message)
	}
	if got := CoerceMap(map[string]any{"message": map[string]any{}, "msg": "x"}); got.Message != "x" {
		t.Errorf("empty dict must lose the `or`, got %q", got.Message)
	}
}

// DELIBERATE DIVERGENCE. str.lower() applies the context-sensitive Final_Sigma
// rule; pyLower does not, because Go exports neither the Cased nor the
// Case_Ignorable derived property. CPython: "ΟΔΟΣ@X.GR".lower() is "οδος@x.gr".
//
// Nothing can observe it: an EmailKey is only ever compared against another
// EmailKey (Route against Store.selected, SelectedEmailText against the account
// list), all produced here, and no log key is persisted for the Tk app to read.
// Module classification is unaffected — no keyword at app.py:18496-18512
// contains a sigma. Spelled the same way in internal/accounts and internal/opll.
func TestPyLowerFinalSigmaDivergence(t *testing.T) {
	if got := EmailKey("ΟΔΟΣ@X.GR"); got == "οδος@x.gr" {
		t.Fatal("pyLower now implements Final_Sigma — delete this test and the DIVERGENCE note in classify.go")
	} else if got != "οδοσ@x.gr" {
		t.Fatalf("EmailKey = %q, want %q", got, "οδοσ@x.gr")
	}
	// Self-consistency is what actually matters: both spellings of the address
	// still land in the same pane.
	if EmailKey("ΟΔΟΣ@X.GR") != EmailKey("οδοσ@x.gr") {
		t.Error("routing must be internally consistent")
	}
}

// app.py:18434-18442: the closure carries the address, the key normalisation
// happens on the way onto the queue.
func TestAccountLogger(t *testing.T) {
	var got []Entry
	emit := func(message, email string) { got = append(got, Entry{Message: message, Email: email}) }

	log := AccountLogger(emit, " A@B.com ")
	log("已保存")
	log.Printf("外部 OAuth 自动登录失败: %v", "boom") // app.py:16169

	want := []Entry{
		{Message: "已保存", Email: "a@b.com"},
		{Message: "外部 OAuth 自动登录失败: boom", Email: "a@b.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted %+v, want %+v", got, want)
	}

	// A global logger emits an empty key, which routes to the global pane.
	got = nil
	AccountLogger(emit, "")("任务已手动停止")
	if len(got) != 1 || got[0].Email != "" {
		t.Fatalf("global logger emitted %+v", got)
	}

	// Unwired emitters must not panic a worker goroutine.
	AccountLogger(nil, "a@b")("x")
	LogFunc(nil).Printf("x")

	// End to end: logger -> store.
	s := newTestStore()
	AccountLogger(func(m, e string) { s.Append(m, e) }, "A@B")("已保存")
	recs := s.AccountRecords("a@b")
	if len(recs) != 1 || recs[0].Message != "[系统] 已保存" || recs[0].Level != LevelSuccess {
		t.Fatalf("stored %+v", recs)
	}
}

// app.py:13996 / 13998 / 18584-18586.
func TestPaneTitles(t *testing.T) {
	if GlobalPaneTitle != "全局日志" {
		t.Errorf("GlobalPaneTitle = %q", GlobalPaneTitle)
	}
	if got := AccountPaneTitle(""); got != "选中账户日志：未选择账户" {
		t.Errorf("empty title = %q", got)
	}
	if got := AccountPaneTitle("A@B.com"); got != "选中账户日志：A@B.com" {
		t.Errorf("title = %q", got)
	}
	// app.py:18428-18432 shows the address as the account row spells it.
	emails := []string{"Other@x.com", "A@B.COM"}
	if got := SelectedEmailText("a@b.com", emails); got != "A@B.COM" {
		t.Errorf("SelectedEmailText = %q", got)
	}
	// No matching row: fall back to the key itself (app.py:18432).
	if got := SelectedEmailText("ghost@x", emails); got != "ghost@x" {
		t.Errorf("SelectedEmailText fallback = %q", got)
	}
	if got := SelectedEmailText("", emails); got != "" {
		t.Errorf("SelectedEmailText empty = %q", got)
	}
}
