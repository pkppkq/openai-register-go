// Package accounts ports the account-table derived-state layer of the Tkinter
// app (app.py 19030-19158, specified by UI_SPEC §1.6 as corrected by §7.1):
// the 状态 column, the 状态 filter, the multi-term search and the column sort.
//
// Everything here is pure: the three app-level lookup tables (self.results,
// self.session_results, self.link_attempt_counts) are passed in as a Lookups
// value instead of being reached for as global state, so the whole file is
// testable without a UI, a state store or a network.
package accounts

import (
	"sort"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// ---------------------------------------------------------------------------
// Constants (app.py:296-323)
// ---------------------------------------------------------------------------

const (
	// GroupAll is the filter-all pseudo group, GroupDefault the group an
	// account with an empty Group falls back to (app.py:296-297).
	GroupAll     = "全部"
	GroupDefault = "未分组"
)

// Status filter values (app.py:316-324, UI_SPEC §7.1).
const (
	StatusFilterAll     = "全部状态"
	StatusFilterPending = "待处理"
	StatusFilterSession = "有 Session"
	StatusFilterPlus    = "Plus"
	StatusFilterTeam    = "Team"
	StatusFilterLinked  = "提链成功"
	StatusFilterFailed  = "失败"
)

// StatusFilterOptions is the picker order (app.py:317-325). Kept as a slice,
// not a map: Go map iteration is randomised where a Python tuple/dict is
// ordered, and this order is user-visible.
var StatusFilterOptions = []string{
	StatusFilterAll,
	StatusFilterPending,
	StatusFilterSession,
	StatusFilterPlus,
	StatusFilterTeam,
	StatusFilterLinked,
	StatusFilterFailed,
}

// Sortable columns (app.py:309).
const (
	ColumnEmail    = "email"
	ColumnType     = "type"
	ColumnStatus   = "status"
	ColumnAttempts = "attempts"
)

// SortColumns is the left-to-right column order (app.py:309); SortLabels the
// headings (app.py:310-315, UI_SPEC §7.1 — the fourth column is 撞链次数, not
// 次数). Iterate SortColumns, never SortLabels: Go map order is random.
var SortColumns = []string{ColumnEmail, ColumnType, ColumnStatus, ColumnAttempts}

var SortLabels = map[string]string{
	ColumnEmail:    "邮箱",
	ColumnType:     "类型",
	ColumnStatus:   "状态",
	ColumnAttempts: "撞链次数",
}

// Sort directions (app.py:305-307). SortCustom means "leave in list order",
// which is how a manual drag-reorder is preserved (app.py:19727-19728).
const (
	SortCustom = "custom"
	SortAsc    = "asc"
	SortDesc   = "desc"
)

// Status strings produced by StatusText / RefreshStatusText (UI_SPEC §7.1).
// These are the derived values only; the ~90 status strings written directly
// by workers (UI_SPEC §1.6) pass through StatusText unchanged.
const (
	StatusLinkExtracted        = "长链已提取"
	StatusSessionAcquired      = "Session已获取"
	StatusSuccess              = "成功"
	StatusPending              = "待处理"
	StatusFree                 = "Free"
	StatusK12Success           = "K12请求成功"
	StatusK12SuccessRefreshed  = "K12请求成功/Session已刷新"
	StatusNeedRTWithAuthPhone  = "待获取RT(带授权手机号)"
	StatusSessionRefreshed     = "Session已刷新"
	StatusK12SessionRefreshed  = "K12 Session已刷新"
	StatusPlusSessionRefreshed = "Plus/Session已刷新"
)

// k12OverlayStatuses is the whitelist the K12 overlay may replace
// (app.py:19064). A set, so random map order is harmless.
var k12OverlayStatuses = map[string]bool{
	StatusSessionAcquired:  true,
	StatusSessionRefreshed: true,
	StatusPending:          true,
	StatusSuccess:          true,
	StatusFree:             true,
	"":                     true,
}

// failureWords drive the 失败 filter. UI_SPEC §7.1: this is a SUBSTRING match
// over the rendered status text, not an enum test — "K12失败", "登录失败",
// "代理耗尽", "疑似已封禁" and "提取长链失败" all match (app.py:19153).
var failureWords = []string{"失败", "错误", "耗尽", "停用", "封禁", "不可用", "拒绝", "超时"}

// ContainsFailureWord reports whether a status string trips the 失败 filter.
func ContainsFailureWord(statusText string) bool {
	for _, word := range failureWords {
		if strings.Contains(statusText, word) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Row identity
// ---------------------------------------------------------------------------

// Key is the canonical row identity: the trimmed, lowercased email.
//
// UI_SPEC §0.3 and §7.4.1: the Tk code uses the list index as the row id and
// _apply_account_visible_order (app.py:19734) reorders self.accounts in place,
// so a stale index silently addresses a different account. Everything in this
// package keys off Key instead. Python already uses this exact normalisation
// where it does the right thing (app.py:19025, 19748: .strip().lower()).
func Key(acc models.MailAccount) string { return KeyOf(acc.Email) }

// KeyOf normalises a raw email into a row identity. pyLower, not
// strings.ToLower: app.py:19025/19750 call str.lower(), whose full mapping turns
// U+0130 into "i" + U+0307. Collapsing it to a bare "i" would give "İ@x.com" and
// "i@x.com" the SAME row identity, so a selection restored after a re-render
// would grab the wrong account (app.py:19747-19751 matches on this key).
func KeyOf(email string) string { return pyLower(pyStrip(email)) }

// GroupOf is `account.group or ACCOUNT_DEFAULT_GROUP` (app.py:19120/19129).
func GroupOf(acc models.MailAccount) string {
	if acc.Group == "" {
		return GroupDefault
	}
	return acc.Group
}

// ---------------------------------------------------------------------------
// Lookups
// ---------------------------------------------------------------------------

// Lookups is the read-only slice of app state the derived status needs. The
// maps are keyed by the RAW account email, exactly as Python keys them
// (app.py:19058/19061/19093) — Python is inconsistent about normalising these
// keys (UI_SPEC §7.4.11) and this package does not silently paper over it;
// callers that want the canonical key must normalise both sides themselves.
// Values are raw JSON-decoded values (any) because that is what the state file
// yields, hence the py* helpers in pyvalue.go.
type Lookups struct {
	// Results is self.results: email -> extracted long link (app.py:19058).
	Results map[string]any
	// SessionResults is self.session_results: email -> payload dict
	// (app.py:19061, key list in UI_SPEC §3 "Session payload keys").
	SessionResults map[string]any
	// LinkAttempts is self.link_attempt_counts: email -> int (app.py:19093).
	LinkAttempts map[string]any
}

// session returns the payload dict for an email, or nil.
//
// Python (app.py:19061) guards with isinstance(payload, dict) while
// _session_has_k12_success (app.py:19071) does NOT — a non-dict payload makes
// Python raise AttributeError there. Go returns nil (treated as empty) in both
// places rather than reproducing the crash.
func (lk Lookups) session(email string) map[string]any {
	payload, _ := lk.SessionResults[email].(map[string]any)
	return payload
}

// HasLink is `bool(str(self.results.get(email, "") or "").strip())`
// (app.py:19058/19142).
func (lk Lookups) HasLink(email string) bool {
	return pyStrip(pyStrOr(lk.Results[email])) != ""
}

// HasSession is the access_token/session_json test (app.py:19062/19141).
func (lk Lookups) HasSession(email string) bool {
	payload := lk.session(email)
	if payload == nil {
		return false
	}
	return pyStrip(pyStrOr(orChain(payload["access_token"], payload["session_json"]))) != ""
}

// HasK12Success ports _session_has_k12_success (app.py:19070-19076): the
// session's k12_status must be an all-digits string (or an int that stringifies
// to one) in [200, 300).
func (lk Lookups) HasK12Success(email string) bool {
	payload := lk.session(email)
	if payload == nil {
		return false
	}
	code, ok := pyDigitInt(pyStrip(pyStrOr(payload["k12_status"])))
	if !ok {
		return false
	}
	return code >= 200 && code < 300
}

// AttemptCount ports _account_attempt_count (app.py:19092-19093):
// max(0, int(link_attempt_counts.get(email, 0) or 0)).
func (lk Lookups) AttemptCount(email string) int {
	n := pyInt(orChain(lk.LinkAttempts[email], 0))
	if n < 0 {
		return 0
	}
	return n
}

// ---------------------------------------------------------------------------
// Derived 状态
// ---------------------------------------------------------------------------

// StatusText ports _account_status_text (app.py:19057-19068) — the 状态 column.
//
// Precedence, in order:
//  1. 长链已提取     when results[email] is non-blank;
//  2. account.Status when it is set (any of the ~90 worker strings);
//  3. Session已获取  when the session payload carries access_token/session_json;
//  4. 成功           when email is merely PRESENT in results;
//  5. 待处理;
//
// then two overlays, in this order (the second can only see 待处理, so an
// account that step 6 rewrites can never reach step 7):
//
//  6. K12请求成功 / K12请求成功/Session已刷新 when k12_status is 2xx AND the
//     status so far is in the k12OverlayStatuses whitelist;
//  7. 待获取RT(带授权手机号) when OpenaiRT is empty, both auth-phone fields are
//     set, and the status is exactly 待处理.
func StatusText(acc models.MailAccount, lk Lookups) string {
	var status string
	if lk.HasLink(acc.Email) {
		status = StatusLinkExtracted
	} else {
		switch {
		case acc.Status != "":
			status = acc.Status
		case lk.HasSession(acc.Email):
			status = StatusSessionAcquired
		default:
			// app.py:19063 tests MEMBERSHIP (`account.email in self.results`),
			// not truthiness: an email present in results whose link is blank
			// reads 成功, because the non-blank test above already failed.
			if _, present := lk.Results[acc.Email]; present {
				status = StatusSuccess
			} else {
				status = StatusPending
			}
		}
	}
	if lk.HasK12Success(acc.Email) && k12OverlayStatuses[status] {
		if status == StatusSessionRefreshed {
			status = StatusK12SuccessRefreshed
		} else {
			status = StatusK12Success
		}
	}
	if acc.OpenaiRT == "" && acc.AuthPhoneNumber != "" && acc.AuthPhoneSMSURL != "" && status == StatusPending {
		status = StatusNeedRTWithAuthPhone
	}
	return status
}

// RefreshStatusText ports _session_refresh_status_text (app.py:19078-19090):
// the status a session-refresh result event writes back (app.py:18974), which
// StatusText then sees as acc.Status. result may be nil (`result or {}`).
func RefreshStatusText(email string, result map[string]any, lk Lookups) string {
	targetWorkspaceID := pyStrip(pyStrOr(result["target_workspace_id"]))
	summary, _ := result["access_summary"].(map[string]any)
	// app.py:19082 strips first, then lowers — and uses .lower(), not
	// .casefold() as the sort/search paths do, so pyLower rather than caseFold.
	planType := pyLower(pyStrip(pyStrOr(summary["plan_type"])))
	if planType == "unknown" {
		planType = ""
	}
	if targetWorkspaceID != "" {
		if lk.HasK12Success(email) {
			return StatusK12SuccessRefreshed
		}
		return StatusK12SessionRefreshed
	}
	switch planType {
	case "plus":
		return StatusPlusSessionRefreshed
	case "k12":
		return StatusK12SessionRefreshed
	}
	return StatusSessionRefreshed
}

// Row is _account_row_values (app.py:19095-19096) plus the stable Key: one
// rendered table row, columns in SortColumns order.
type Row struct {
	Key      string
	Email    string
	Type     string
	Status   string
	Attempts int
}

// RowOf builds the rendered row for one account.
func RowOf(acc models.MailAccount, lk Lookups) Row {
	return Row{
		Key:      Key(acc),
		Email:    acc.Email,
		Type:     acc.AccountType,
		Status:   StatusText(acc, lk),
		Attempts: lk.AttemptCount(acc.Email),
	}
}

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

// MatchesStatusFilter ports _account_matches_status_filter
// (app.py:19136-19158). An unknown filter passes everything, as in Python.
func MatchesStatusFilter(acc models.MailAccount, statusFilter string, lk Lookups) bool {
	if statusFilter == "" { // app.py:19137 `str(status_filter or ALL)`
		statusFilter = StatusFilterAll
	}
	if statusFilter == StatusFilterAll {
		return true
	}
	hasSession := lk.HasSession(acc.Email)
	hasLink := lk.HasLink(acc.Email)
	accountType := caseFold(pyStrip(acc.AccountType))
	statusText := StatusText(acc, lk)
	switch statusFilter {
	case StatusFilterSession:
		return hasSession
	case StatusFilterPlus:
		// app.py:19148 — the Plus filter also admits pro.
		return accountType == "plus" || accountType == "pro"
	case StatusFilterTeam:
		return accountType == "team"
	case StatusFilterLinked:
		return hasLink
	case StatusFilterFailed:
		return ContainsFailureWord(statusText)
	case StatusFilterPending:
		return !hasSession && !hasLink && !ContainsFailureWord(statusText)
	}
	return true
}

// Filter is the account-table filter bar: group picker, status picker and the
// search box (app.py:19108-19116). The zero value matches everything.
type Filter struct {
	// Group is a group name, or GroupAll / "" for no group filter.
	Group string
	// Status is one of StatusFilterOptions, or "" for StatusFilterAll.
	Status string
	// Search is the raw search-box text.
	Search string
}

// SearchTerms is the search-box tokenizer (app.py:19116): strip, casefold,
// split on Python-flavoured whitespace, drop empties. The terms are AND-ed.
func SearchTerms(search string) []string {
	folded := caseFold(pyStrip(search))
	if folded == "" {
		return nil
	}
	parts := reWhitespace.Split(folded, -1)
	terms := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			terms = append(terms, part)
		}
	}
	if len(terms) == 0 {
		return nil
	}
	return terms
}

// Matches is the body of the _account_visible_indices comprehension
// (app.py:19117-19134) for a single account.
func Matches(acc models.MailAccount, f Filter, lk Lookups) bool {
	return matches(acc, f.Group, f.Status, SearchTerms(f.Search), lk)
}

func matches(acc models.MailAccount, group, statusFilter string, terms []string, lk Lookups) bool {
	// Python's group var always holds a real name (defaulting to GroupAll at
	// app.py:19111); Go's zero value "" is read as "no group filter" so the
	// zero Filter is usable.
	if group != "" && group != GroupAll && GroupOf(acc) != group {
		return false
	}
	if !MatchesStatusFilter(acc, statusFilter, lk) {
		return false
	}
	if len(terms) == 0 {
		return true
	}
	// app.py:19125-19130: one space-joined haystack of email+type+status+group,
	// casefolded as a whole, every term must appear (AND).
	haystack := caseFold(strings.Join([]string{
		acc.Email,
		acc.AccountType,
		StatusText(acc, lk),
		GroupOf(acc),
	}, " "))
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

// Visible ports _account_visible_indices (app.py:19108-19134) but returns the
// matching accounts in list order rather than their positions — see Key for
// why positions are not identities here.
func Visible(accs []models.MailAccount, f Filter, lk Lookups) []models.MailAccount {
	terms := SearchTerms(f.Search)
	out := make([]models.MailAccount, 0, len(accs))
	for _, acc := range accs {
		if matches(acc, f.Group, f.Status, terms, lk) {
			out = append(out, acc)
		}
	}
	return out
}

// VisibleKeys is Visible reduced to stable row identities — what a renderer or
// a selection set should hold onto.
func VisibleKeys(accs []models.MailAccount, f Filter, lk Lookups) []string {
	terms := SearchTerms(f.Search)
	out := make([]string, 0, len(accs))
	for _, acc := range accs {
		if matches(acc, f.Group, f.Status, terms, lk) {
			out = append(out, Key(acc))
		}
	}
	return out
}

// VisibleIndices returns the positions of the visible accounts within accs.
// This is the literal shape of app.py:19117 and exists only for the manual
// drag-reorder path (_apply_account_visible_order, app.py:19734), which has to
// splice rows back into the master list. The result is valid only until accs
// is reordered — never store it on a row.
func VisibleIndices(accs []models.MailAccount, f Filter, lk Lookups) []int {
	terms := SearchTerms(f.Search)
	out := make([]int, 0, len(accs))
	for index, acc := range accs {
		if matches(acc, f.Group, f.Status, terms, lk) {
			out = append(out, index)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Sorting
// ---------------------------------------------------------------------------

// SortKey is one account's comparison key for one column. Exactly one field is
// meaningful per column (Text for email/type/status, Num for attempts), which
// mirrors Python returning either a str or an int from the same function.
type SortKey struct {
	Text string
	Num  int
}

// Less orders two keys of the same column. Python compares casefolded strings
// by code point; Go compares by byte, and UTF-8 byte order is code-point
// order, so the two agree.
func (k SortKey) Less(other SortKey) bool {
	if k.Text != other.Text {
		return k.Text < other.Text
	}
	return k.Num < other.Num
}

// SortKeyOf ports _account_sort_key (app.py:19098-19106).
//
// DELIBERATE DEVIATION, and only in the argument: Python's signature is
// `_account_sort_key(self, index: int, column: str)` and it starts with
// `account = self.accounts[index]`. UI_SPEC §0.3 and §7.4.1 flag
// index-as-row-identity as a defect to FIX rather than port —
// _apply_account_visible_order (app.py:19734) rebuilds self.accounts in place,
// so any index captured before a sort/filter/re-render addresses a different
// account afterwards. This takes the account value itself.
//
// The KEYS are Python's, unchanged. In particular the email column sorts on
// caseFold(acc.Email) — app.py:19106's `str(account.email or "").casefold()` —
// and NOT on Key: Key additionally strips, so " a@x.io" sorts first here (as it
// does in Tk) but is indistinguishable from "a@x.io" as a row identity.
//
// An unrecognised column falls through to email, as Python's trailing return
// at app.py:19106 does.
func SortKeyOf(acc models.MailAccount, column string, lk Lookups) SortKey {
	switch column {
	case ColumnType:
		return SortKey{Text: caseFold(acc.AccountType)}
	case ColumnStatus:
		return SortKey{Text: caseFold(StatusText(acc, lk))}
	case ColumnAttempts:
		return SortKey{Num: lk.AttemptCount(acc.Email)}
	default:
		return SortKey{Text: caseFold(acc.Email)}
	}
}

// SortAccounts ports the sort half of _account_display_indices
// (app.py:19726-19732): an unknown column falls back to email, and the sort is
// STABLE — Python's sorted() is stable even with reverse=True, so ties keep
// list order in both directions. Returns a new slice; accs is not modified.
//
// Direction handling is literal: app.py:19728 short-circuits on EXACTLY
// ACCOUNT_SORT_CUSTOM, so any other unrecognised direction sorts ascending
// (reverse is only true for exactly ACCOUNT_SORT_DESC, app.py:19730). The one
// addition is that Go's zero value "" is also read as SortCustom, the same
// convenience `matches` applies to Filter.Group, so the zero-value call leaves
// the list alone instead of silently re-ordering it.
//
// That addition costs nothing because Python can never HOLD "": the field is
// initialised to ACCOUNT_SORT_CUSTOM (app.py:12425), the state file goes through
// `or ACCOUNT_SORT_CUSTOM` (app.py:14073), and _set_account_sort_state coerces
// anything outside ACCOUNT_SORT_DIRECTIONS back to it (app.py:19032). A
// differential sweep flags "" as the only direction the two disagree on; drop
// the `|| direction == ""` and every zero-value caller silently sorts by email.
func SortAccounts(accs []models.MailAccount, column, direction string, lk Lookups) []models.MailAccount {
	out := make([]models.MailAccount, len(accs))
	copy(out, accs)
	if direction == SortCustom || direction == "" {
		return out
	}
	if SortLabels[column] == "" {
		column = ColumnEmail
	}
	keys := make([]SortKey, len(out))
	order := make([]int, len(out))
	for i, acc := range out {
		keys[i] = SortKeyOf(acc, column, lk)
		order[i] = i
	}
	desc := direction == SortDesc
	sort.SliceStable(order, func(a, b int) bool {
		if desc {
			return keys[order[b]].Less(keys[order[a]])
		}
		return keys[order[a]].Less(keys[order[b]])
	})
	sorted := make([]models.MailAccount, len(out))
	for i, index := range order {
		sorted[i] = out[index]
	}
	return sorted
}

// Display is Visible followed by SortAccounts — the full account-table render
// input (_account_display_indices, app.py:19725-19731).
func Display(accs []models.MailAccount, f Filter, column, direction string, lk Lookups) []models.MailAccount {
	return SortAccounts(Visible(accs, f, lk), column, direction, lk)
}
