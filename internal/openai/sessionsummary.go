package openai

// Session-payload plan summary.
//
// Ported from app.py:
//   - summarize_chatgpt_session_payload  app.py:5789-5805
//
// Every helper it needs already exists in account.go / auth.go; nothing is
// reimplemented here.

// SummarizeChatGPTSessionPayload mirrors summarize_chatgpt_session_payload
// (app.py:5789-5805).
//
// Three layers, in this order:
//  1. the accessToken claims (SummarizeChatGPTAccessToken),
//  2. account_id / user_id salvaged from the /api/auth/session body, but ONLY
//     when the token did not already carry them,
//  3. whatever plan the full payload walk can infer, recorded under the
//     "session" source key.
//
// sessionPayload is `any` because Python's parameter is untyped: the body
// isinstance-guards it for the dict lookups and hands the raw value to
// infer_account_type_from_payload, which walks arbitrary JSON.
//
// DIVERGENCE: Python only isinstance-guards the THIRD first_non_empty argument at
// app.py:5794 — `session_payload.get("account_id")` (the second) is unguarded, so a
// non-dict payload combined with a token that has no account_id raises
// AttributeError there. Go cannot raise, so a non-dict payload simply contributes
// no id fields. Safe: all four call sites (app.py:3314, 22261, 22399, 22894) pass a
// json.loads dict, and 22894 additionally gates on `if session_payload:`.
func SummarizeChatGPTSessionPayload(sessionPayload any, accessToken string) map[string]any {
	summary := SummarizeChatGPTAccessToken(accessToken)
	// accountObjectGet accepts both a plain map and the insertion-order object that
	// DecodeOrderedJSON produces, so a caller can hand either one in. The lookups
	// below are all by key, so order does not change the answer — only the plan walk
	// at the bottom is order-sensitive.
	account := accountObjectGet(sessionPayload, "account")
	user := accountObjectGet(sessionPayload, "user")

	// `not summary.get("account_id")`: accountPyStr is str(v or ""), so "" means falsy.
	if accountPyStr(summary["account_id"]) == "" {
		// Key precedence is load-bearing: the nested account record wins over the two
		// flat aliases, and first_non_empty skips values that strip to "".
		accountID := FirstNonEmpty(accountObjectGet(account, "id"),
			accountObjectGet(sessionPayload, "account_id"),
			accountObjectGet(sessionPayload, "chatgpt_account_id"))
		if accountID != "" {
			summary["account_id"] = accountID
			summary["account_id_tail"] = accountTail(accountID, 8)
		}
	}
	if accountPyStr(summary["user_id"]) == "" {
		userID := FirstNonEmpty(accountObjectGet(user, "id"), accountObjectGet(sessionPayload, "user_id"))
		if userID != "" {
			summary["user_id"] = userID
			summary["user_id_tail"] = accountTail(userID, 8)
		}
	}

	// The walk gets the ORIGINAL value, not `record` — a list payload still gets
	// scanned for plan fields even though it contributed no ids above.
	inferredPlan, inferredDetail := InferAccountTypeFromPayload(sessionPayload)
	// Python discards the return value and relies on the in-place mutation; the map
	// is never nil here, so returning it directly is the same object.
	return ApplyInferredPlanToSummary(summary, inferredPlan, inferredDetail, "session")
}
