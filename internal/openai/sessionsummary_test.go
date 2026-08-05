package openai

import "testing"

// Note: the `jwt(payload)` helper lives in auth_test.go (same package) — reused here.

func TestSummarizeChatGPTSessionPayload(t *testing.T) {
	// A token that already carries ids + a (stale) free plan.
	tokenFull := jwt(map[string]any{
		"exp": 1700000000,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc_TOKEN12345678",
			"chatgpt_user_id":    "usr_TOKEN87654321",
			"chatgpt_plan_type":  "free",
		},
	})
	// A token with no auth claim at all: account_id/user_id empty, plan_type "unknown".
	tokenBare := jwt(map[string]any{"exp": 1700000000})
	tokenTeam := jwt(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "team"},
	})

	cases := []struct {
		name    string
		payload any
		token   string
		want    map[string]string
		absent  []string
	}{
		{
			name:    "nil payload and empty token leave every field empty",
			payload: nil,
			token:   "",
			want: map[string]string{
				"plan_type": "unknown", "account_id": "", "account_id_tail": "",
				"user_id": "", "user_id_tail": "", "expires_at": "",
			},
			// infer returns ("", "未发现明确套餐字段") -> apply_inferred_plan_to_summary
			// bails before writing anything.
			absent: []string{"session_plan_type", "session_plan_detail"},
		},
		{
			name:    "empty object payload contributes nothing",
			payload: map[string]any{},
			token:   tokenBare,
			want:    map[string]string{"plan_type": "unknown", "account_id": "", "user_id": ""},
			absent:  []string{"session_plan_type", "session_plan_detail"},
		},
		{
			// DIVERGENCE case: Python raises AttributeError here (app.py:5794 calls
			// .get on the unguarded payload); Go yields no ids but still walks the value.
			name:    "list payload yields no ids but is still walked for a plan",
			payload: []any{map[string]any{"plan_type": "chatgpt_plus_plan"}},
			token:   tokenBare,
			want: map[string]string{
				"account_id":          "",
				"plan_type":           "plus",
				"session_plan_type":   "plus",
				"session_plan_detail": "payload[0].plan_type=chatgpt_plus_plan",
			},
		},
		{
			name:    "string payload yields no ids and no plan",
			payload: "not-a-json-object",
			token:   tokenBare,
			want:    map[string]string{"plan_type": "unknown", "account_id": "", "user_id": ""},
			absent:  []string{"session_plan_type"},
		},
		{
			name:    "token ids win over the session payload",
			payload: map[string]any{"account": map[string]any{"id": "acc_SESSION_IGNORED"}, "user": map[string]any{"id": "usr_SESSION_IGNORED"}},
			token:   tokenFull,
			want: map[string]string{
				"account_id": "acc_TOKEN12345678", "account_id_tail": "12345678",
				"user_id": "usr_TOKEN87654321", "user_id_tail": "87654321",
			},
		},
		{
			name: "account.id outranks both flat aliases",
			payload: map[string]any{
				"account":            map[string]any{"id": "acc_NESTED_1234ABCD"},
				"account_id":         "acc_FLAT",
				"chatgpt_account_id": "acc_CHATGPT",
			},
			token: tokenBare,
			want:  map[string]string{"account_id": "acc_NESTED_1234ABCD", "account_id_tail": "1234ABCD"},
		},
		{
			// first_non_empty strips, so a whitespace-only account.id is falsy.
			name:    "whitespace account.id falls through to the flat account_id",
			payload: map[string]any{"account": map[string]any{"id": "   "}, "account_id": "acct_ZZZZ9999"},
			token:   tokenBare,
			want:    map[string]string{"account_id": "acct_ZZZZ9999", "account_id_tail": "ZZZZ9999"},
		},
		{
			name:    "empty account_id falls through to chatgpt_account_id",
			payload: map[string]any{"account_id": "", "chatgpt_account_id": "cid_87654321"},
			token:   tokenBare,
			want:    map[string]string{"account_id": "cid_87654321", "account_id_tail": "87654321"},
		},
		{
			// get_nested_record ignores a non-object "account", so account.get("id") is absent.
			name:    "non-object account record is ignored",
			payload: map[string]any{"account": "acc_STRING", "account_id": "acct_QQQQ1111"},
			token:   tokenBare,
			want:    map[string]string{"account_id": "acct_QQQQ1111", "account_id_tail": "QQQQ1111"},
		},
		{
			name:    "user.id fills a missing user_id",
			payload: map[string]any{"user": map[string]any{"id": "user_11112222"}},
			token:   tokenBare,
			want:    map[string]string{"user_id": "user_11112222", "user_id_tail": "11112222"},
		},
		{
			name:    "flat user_id is the fallback",
			payload: map[string]any{"user": "not-an-object", "user_id": "u_33334444"},
			token:   tokenBare,
			want:    map[string]string{"user_id": "u_33334444", "user_id_tail": "33334444"},
		},
		{
			// Python str(x)[-8:] does not pad.
			name:    "short id is not padded",
			payload: map[string]any{"account_id": "abc", "user_id": "u"},
			token:   tokenBare,
			want: map[string]string{
				"account_id": "abc", "account_id_tail": "abc",
				"user_id": "u", "user_id_tail": "u",
			},
		},
		{
			// encoding/json decodes JSON numbers to float64; first_non_empty str()-coerces.
			// See the divergence note: large integral ids would render in exponent form.
			name:    "numeric account_id is coerced to text",
			payload: map[string]any{"account_id": float64(42)},
			token:   tokenBare,
			want:    map[string]string{"account_id": "42", "account_id_tail": "42"},
		},
		{
			name:    "a paid session plan overrides the stale free in the token",
			payload: map[string]any{"account": map[string]any{"plan_type": "chatgptplusplan"}},
			token:   tokenFull,
			want: map[string]string{
				"plan_type":           "plus",
				"session_plan_type":   "plus",
				"session_plan_detail": "payload.account.plan_type=chatgptplusplan",
				"account_id":          "acc_TOKEN12345678",
			},
		},
		{
			// A boolean-false paid flag infers "free", which must NOT demote a paid token.
			name:    "a free session never demotes a paid token",
			payload: map[string]any{"is_paid_subscription_active": false},
			token:   tokenTeam,
			want: map[string]string{
				"plan_type":           "team",
				"session_plan_type":   "free",
				"session_plan_detail": "payload.is_paid_subscription_active=false",
			},
		},
		{
			name:    "an unknown token plan is replaced even by a free session",
			payload: map[string]any{"is_paid_subscription_active": false},
			token:   tokenBare,
			want:    map[string]string{"plan_type": "free", "session_plan_type": "free"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SummarizeChatGPTSessionPayload(c.payload, c.token)
			for key, wantValue := range c.want {
				value, ok := got[key].(string)
				if !ok || value != wantValue {
					t.Errorf("summary[%q] = %#v, want %q", key, got[key], wantValue)
				}
			}
			for _, key := range c.absent {
				if _, ok := got[key]; ok {
					t.Errorf("summary[%q] = %#v, want key absent", key, got[key])
				}
			}
		})
	}
}
