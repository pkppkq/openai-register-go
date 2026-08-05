package openai

// pyparity_test.go — differential parity with app.py.
//
// EVERY expectation in this file was COMPUTED by executing the verbatim app.py
// line slice for the function under test over the input beside it (CPython
// 3.12, Unicode 15.0.0); none of it is hand-derived. The inputs are the ones
// that separated CPython from a natural Go spelling: non-ASCII decimal digits,
// NBSP and the other 24 Unicode spaces, the C0 information separators
// U+001C..U+001F, CJK text abutting ASCII (Python's `\b` sees no boundary
// there), Turkish dotted I, long s, sharp s, and percent-escapes net/url
// rejects.
//
// Regenerate by re-running the slices, not by editing an expectation: a
// "wrong-looking" value here is app.py's answer and the port must match it.

import (
	"encoding/json"
	"testing"
)

// classify_chatgpt_plan_text lowercases with str.lower(), which maps U+0130 to
// TWO runes: "BUS\u0130NESS".lower() is "busi\u0307ness" and does NOT contain
// "business", so Python answers "" where strings.ToLower answered "team".
func TestPyParityClassifyChatGPTPlanText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{" ", ""},
		{"plus", "plus"},
		{"PLUS", "plus"},
		{"Plus ", "plus"},
		{"chatgpt_plus_plan", "plus"},
		{"chatgpt-plus-plan", "plus"},
		{"chatgpt plus plan", "plus"},
		{"team", "team"},
		{"Enterprise", "team"},
		{"business", "team"},
		{"k12", "k12"},
		{"teacher", "k12"},
		{"school", "k12"},
		{"school team", "team"},
		{"pro", "plus"},
		{"product", "plus"},
		{"professional", "plus"},
		{"free", "free"},
		{"none", "free"},
		{"noplan", "free"},
		{"chatgpt_free_plan", "free"},
		{"unknown", ""},
		{"\x1fplus\x1f", "plus"},
		{"BUS\u0130NESS", ""},
		{"\u00a0team\u00a0", "team"},
		{"\u3000team\u3000", "team"},
		{"PLU\u017f", ""},
		{"\u212a12", "k12"},
		{"plu s", "plus"},
	}
	for _, c := range cases {
		if got := ClassifyChatGPTPlanText(c.in); got != c.want {
			t.Errorf("ClassifyChatGPTPlanText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPyParityNormalizePayloadKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"plan_type", "plantype"},
		{"PLAN-TYPE", "plantype"},
		{"Plan Type", "plantype"},
		{"chatgpt_plan_type", "chatgptplantype"},
		{"is_paid", "ispaid"},
		{"\u0130SPA\u0130D", "ispaid"},
		{"isPaid", "ispaid"},
		{"is-paid!", "ispaid"},
		{" plan ", "plan"},
		{"\u00a0plan\u00a0", "plan"},
		{"\u017fku", "ku"},
		{"SKU", "sku"},
		{"\u540d\u524d", ""},
		{"___", ""},
		{"123", "123"},
	}
	for _, c := range cases {
		if got := AccountNormalizePayloadKey(c.in); got != c.want {
			t.Errorf("AccountNormalizePayloadKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The payload walk and the workspace tie-break are order-sensitive, so the
// fixtures go in as JSON TEXT and are decoded through DecodeOrderedJSON — a
// map[string]any would visit members in sorted order and can pick a different
// workspace, which is the one a billable Team seat is created in.
func TestPyParityInferAccountTypeFromPayload(t *testing.T) {
	cases := []struct {
		payload             string
		plan, detail        string
		wsAccountID, wsRole string
	}{
		{"{}", "", "\u672a\u53d1\u73b0\u660e\u786e\u5957\u9910\u5b57\u6bb5", "", ""},
		{"[]", "", "\u672a\u53d1\u73b0\u660e\u786e\u5957\u9910\u5b57\u6bb5", "", ""},
		{"{\"plan_type\":\"plus\"}", "plus", "payload.plan_type=plus", "", ""},
		{"{\"plan\":\"team\",\"tier\":\"plus\"}", "team", "payload.plan=team", "", ""},
		{"{\"tier\":\"plus\",\"plan\":\"team\"}", "team", "payload.plan=team", "", ""},
		{"{\"is_paid\":true}", "plus", "payload.is_paid=true", "", ""},
		{"{\"is_paid\":false}", "free", "payload.is_paid=false", "", ""},
		{"{\"is_paid\":\"true\"}", "", "\u672a\u53d1\u73b0\u660e\u786e\u5957\u9910\u5b57\u6bb5", "", ""},
		{"{\"isPlusUser\":true,\"plan\":\"free\"}", "plus", "payload.isPlusUser=true", "", ""},
		{"{\"accounts\":{\"w1\":{\"account\":{\"account_id\":\"A1\",\"role\":\"member\"},\"plan\":\"team\"},\"w2\":{\"account\":{\"account_id\":\"A2\",\"role\":\"owner\"},\"plan\":\"team\"}}}", "team", "payload.accounts.w2.plan=team", "A2", "owner"},
		{"{\"accounts\":{\"w1\":{\"account\":{\"account_id\":\"A1\",\"role\":\"owner\"},\"plan\":\"team\"},\"w2\":{\"account\":{\"account_id\":\"A2\",\"role\":\"admin\"},\"plan\":\"team\"}}}", "team", "payload.accounts.w2.plan=team", "A1", "owner"},
		{"{\"accounts\":[{\"account\":{\"id\":\"L1\"},\"plan\":\"team\"},{\"account\":{\"id\":\"L2\"},\"plan\":\"team\"}]}", "team", "payload.accounts[1].plan=team", "L1", ""},
		{"{\"accounts\":{\"K1\":{\"plan\":\"team\"},\"K2\":{\"plan\":\"team\"}}}", "team", "payload.accounts.K2.plan=team", "K1", ""},
		{"{\"name\":\"plus\"}", "", "\u672a\u53d1\u73b0\u660e\u786e\u5957\u9910\u5b57\u6bb5", "", ""},
		{"{\"user\":{\"name\":\"plus\"}}", "", "\u672a\u53d1\u73b0\u660e\u786e\u5957\u9910\u5b57\u6bb5", "", ""},
		{"{\"plan\":{\"name\":\"plus\"}}", "plus", "payload.plan.name=plus", "", ""},
		{"{\"nested\":{\"deep\":{\"subscription_plan\":\"ChatGPT Plus\"}}}", "plus", "payload.nested.deep.subscription_plan=ChatGPT Plus", "", ""},
		{"{\"a\":[{\"sku\":\"team\"},{\"sku\":\"k12\"}]}", "team", "payload.a[0].sku=team", "", ""},
		{"{\"a\":[{\"sku\":\"k12\"},{\"sku\":\"team\"}]}", "team", "payload.a[1].sku=team", "", ""},
		{"{\"is_paid\":true,\"plan\":\"free\"}", "plus", "payload.is_paid=true", "", ""},
		{"{\"has_active_subscription\":false,\"plan_name\":\"none\"}", "free", "payload.has_active_subscription=false", "", ""},
		{"{\"product_name\":\"ChatGPT\\u00a0Plus\"}", "plus", "payload.product_name=ChatGPT\u00a0Plus", "", ""},
		{"{\"product_name\":\"ChatGPT\\u001fPlus\"}", "plus", "payload.product_name=ChatGPT\x1fPlus", "", ""},
		{"{\"plan_type\":\"BUS\\u0130NESS\"}", "", "\u672a\u53d1\u73b0\u660e\u786e\u5957\u9910\u5b57\u6bb5", "", ""},
		{"{\"accounts\":{\"w1\":{\"account\":{\"account_id\":\" A1 \",\"role\":\" Owner \"},\"plan\":\"team\"}}}", "team", "payload.accounts.w1.plan=team", "A1", "Owner"},
		{"{\"accounts\":{\"w1\":{\"plan\":\"team\"}}}", "team", "payload.accounts.w1.plan=team", "w1", ""},
		{"{\"accounts\":\"nope\"}", "", "\u672a\u53d1\u73b0\u660e\u786e\u5957\u9910\u5b57\u6bb5", "", ""},
		{"{\"is_pro\":true,\"is_plus\":false}", "plus", "payload.is_pro=true", "", ""},
	}
	for _, c := range cases {
		value, err := DecodeOrderedJSON([]byte(c.payload))
		if err != nil {
			t.Fatalf("DecodeOrderedJSON(%s): %v", c.payload, err)
		}
		plan, detail := InferAccountTypeFromPayload(value)
		if plan != c.plan || detail != c.detail {
			t.Errorf("InferAccountTypeFromPayload(%s) = (%q, %q), want (%q, %q)", c.payload, plan, detail, c.plan, c.detail)
		}
		workspace := accountTeamWorkspaceFromBackendPayload(value)
		if workspace.AccountID != c.wsAccountID || workspace.Role != c.wsRole {
			t.Errorf("accountTeamWorkspaceFromBackendPayload(%s) = (%q, %q), want (%q, %q)",
				c.payload, workspace.AccountID, workspace.Role, c.wsAccountID, c.wsRole)
		}
	}
}

func TestPyParityMergeChatGPTBackendPlanSummary(t *testing.T) {
	cases := []struct {
		results     string
		planType    string
		backendPlan string
		detail      string
		workspaceID string
	}{
		{"[]", "unknown", "", "", ""},
		{"[{\"endpoint\":\"/a\",\"status\":200,\"payload\":{\"plan\":\"plus\"}}]", "plus", "plus", "/a HTTP 200 -> payload.plan=plus", ""},
		{"[{\"endpoint\":\"\",\"status\":0,\"payload\":{\"plan\":\"plus\"}}]", "plus", "plus", "backend HTTP  -> payload.plan=plus", ""},
		{"[{\"status\":404,\"payload\":{\"plan\":\"free\"}},{\"endpoint\":\"/b\",\"status\":200,\"payload\":{\"plan\":\"team\"}}]", "team", "team", "/b HTTP 200 -> payload.plan=team", ""},
		{"[{\"endpoint\":\"/a\",\"status\":\"200\",\"payload\":\"nope\"},{\"endpoint\":\"/b\",\"status\":200,\"payload\":{\"plan\":\"free\"}}]", "free", "free", "/b HTTP 200 -> payload.plan=free", ""},
		{"[{\"endpoint\":\"/a\",\"status\":200,\"payload\":{\"accounts\":{\"w1\":{\"account\":{\"account_id\":\"A1\",\"role\":\"owner\"},\"plan\":\"team\"}}}}]", "team", "team", "/a HTTP 200 -> payload.accounts.w1.plan=team", "A1"},
		{"[{\"endpoint\":\"/a\",\"status\":200,\"payload\":[{\"plan\":\"team\"}]}]", "team", "team", "/a HTTP 200 -> payload[0].plan=team", ""},
		{"[1,2,{\"endpoint\":\"/c\",\"status\":200,\"payload\":{\"plan\":\"k12\"}}]", "k12", "k12", "/c HTTP 200 -> payload.plan=k12", ""},
	}
	for _, c := range cases {
		value, err := DecodeOrderedJSON([]byte(c.results))
		if err != nil {
			t.Fatalf("DecodeOrderedJSON(%s): %v", c.results, err)
		}
		list, _ := value.([]any)
		summary := MergeChatGPTBackendPlanSummary(map[string]any{"plan_type": "unknown"}, list)
		check := func(key, want string) {
			got, _ := summary[key].(string)
			if got != want {
				t.Errorf("MergeChatGPTBackendPlanSummary(%s)[%q] = %q, want %q", c.results, key, got, want)
			}
		}
		check("plan_type", c.planType)
		check("backend_plan_type", c.backendPlan)
		check("backend_plan_detail", c.detail)
		check("backend_workspace_id", c.workspaceID)
	}
}

// first_non_empty is str(value).strip(), NOT str(value or ""): False and 0
// stringify to "False"/"0" and WIN the chain. A list or dict claim renders as a
// Python repr, which is what ends up in an account_id tail.
func TestPyParityFirstNonEmpty(t *testing.T) {
	cases := []struct{ values, want string }{
		{"[null]", ""},
		{"[\"\"]", ""},
		{"[\"  \"]", ""},
		{"[0]", "0"},
		{"[false]", "False"},
		{"[true]", "True"},
		{"[\"\", \"x\"]", "x"},
		{"[\"\\u00a0\", \"x\"]", "x"},
		{"[\"\\u001f\", \"x\"]", "x"},
		{"[1234567890123]", "1234567890123"},
		{"[1.5]", "1.5"},
		{"[[1,2]]", "[1, 2]"},
		{"[{\"a\":1}]", "{'a': 1}"},
		{"[null,false,\"y\"]", "False"},
	}
	for _, c := range cases {
		var values []any
		if err := json.Unmarshal([]byte(c.values), &values); err != nil {
			t.Fatal(err)
		}
		if got := FirstNonEmpty(values...); got != c.want {
			t.Errorf("FirstNonEmpty(%s) = %q, want %q", c.values, got, c.want)
		}
	}
}
