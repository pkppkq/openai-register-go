package proxypool

import (
	"strconv"
	"strings"
	"sync"
)

// Role is one of the four manual proxy pools of S17. app.py holds them in Tk
// Text widgets (proxy_text / payment_dynamic_proxy_text / followup_… /
// approve_…) and keys the （剩余 N） titles by these names (app.py:12443).
type Role string

const (
	// RoleRegister is 注册/获取 Session 动态代理池 (settings key dynamic_proxies).
	RoleRegister Role = "register"
	// RoleCreate is 创建长链第一步代理池 (settings key payment_dynamic_proxy).
	RoleCreate Role = "create"
	// RoleFollowup is 创建长链后续代理池 (settings key followup_dynamic_proxy).
	RoleFollowup Role = "followup"
	// RoleApprove is Approve 代理池 (settings key approve_dynamic_proxy).
	RoleApprove Role = "approve"
)

// Roles is the display and iteration order. Never range over a map to produce
// UI or log output: Go randomises map order where a Python dict is insertion
// ordered, and these four are rendered top-to-bottom in a fixed layout.
var Roles = []Role{RoleRegister, RoleCreate, RoleFollowup, RoleApprove}

// roleTitles are proxy_pool_title_bases (app.py:12443).
var roleTitles = map[Role]string{
	RoleRegister: "注册/获取 Session 动态代理池",
	RoleCreate:   "创建长链第一步代理池",
	RoleFollowup: "创建长链后续代理池",
	RoleApprove:  "Approve 代理池",
}

// TitleBase is the pool label without the count.
func (r Role) TitleBase() string {
	if title, ok := roleTitles[r]; ok {
		return title
	}
	return string(r)
}

// Title ports _proxy_pool_title_text (app.py:13211): "<base>（剩余 N）" with
// full-width parentheses and a floor of 0.
func (r Role) Title(count int) string {
	if count < 0 {
		count = 0
	}
	return r.TitleBase() + "（剩余 " + strconv.Itoa(count) + "）"
}

// Route modes of the S17 代理模式 combo (app.py:282).
const (
	RouteModeDefault   = "照旧"
	RouteModeLocalOnly = "全走本地代理"
)

// RouteModeOptions is PROXY_ROUTE_MODE_OPTIONS in order.
var RouteModeOptions = []string{RouteModeDefault, RouteModeLocalOnly}

// NormalizeRouteMode reproduces the load and save guards (app.py:14089 and
// app.py:14239): anything unrecognised — including whitespace-only — collapses
// to 照旧, on both read and write.
// pyStrip, not strings.TrimSpace: app.py strips with str.strip(), which also
// eats U+001C..U+001F.
func NormalizeRouteMode(value string) string {
	if pyStrip(value) == RouteModeLocalOnly {
		return RouteModeLocalOnly
	}
	return RouteModeDefault
}

// Pool is one rotating proxy list.
//
// The state is the TEXTAREA CONTENT, verbatim, exactly as app.py's state is the
// Tk Text widget: every read runs parse_proxy_pool_text over it again
// (app.py:16725-16743, 13205), and only a rotation or a removal rewrites it.
// Caching the parsed list instead looks equivalent and is not:
//
//   - Text() has to be able to hand back what the user typed. app.py only
//     rewrites the widget inside _rotate_proxy_pool_values / _remove_*; a pool
//     that echoed the normalized form back would rewrite the editor under the
//     user's cursor on every keystroke, and would report 剩余 0 with the text
//     blanked for a pool that parses to nothing.
//   - parse_proxy_pool_text is NOT idempotent for lines holding interior
//     whitespace: "https 1.2.3.4:8080:u:p" parses to
//     "http://https 1.2.3.4:8080:u:p", which re-parses to "http://https". app.py
//     loses the tail on the first rotation; a cached list would keep it. Both
//     are defensible, but only one of them is app.py.
type Pool struct {
	mu       sync.Mutex
	text     string
	onChange func()
}

// NewPool builds a pool from raw textarea content.
func NewPool(text string) *Pool {
	return &Pool{text: text}
}

func (p *Pool) notify() {
	// Read the callback under the lock but never invoke it under the lock: it
	// calls back into Snapshot()/Text() to build the pools-updated event.
	p.mu.Lock()
	cb := p.onChange
	p.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// SetText replaces the pool with a textarea edit, stored verbatim.
func (p *Pool) SetText(text string) {
	p.mu.Lock()
	p.text = text
	p.mu.Unlock()
	p.notify()
}

// Text is the widget content, which is both what the editor shows and what
// app.py:14240-14243 persists (after its own .strip()).
func (p *Pool) Text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text
}

// items is parse_proxy_pool_text over the current text. Caller holds the lock.
func (p *Pool) items() []string { return ParseProxyPoolText(p.text) }

// Remaining is the （剩余 N） count. Deliberately NOT gated by the route mode:
// _proxy_pool_nonempty_line_count (app.py:13205) reports the real pool size even
// when 全走本地代理 is on, because it describes the editor, not a read.
func (p *Pool) Remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items())
}

// List is one _read_*_dynamic_proxies call: parse the widget, in order.
func (p *Pool) List() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.items()
}

// Peek is _peek_*_dynamic_proxy (app.py:17438): proxies[0] with no rotation.
func (p *Pool) Peek() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	items := p.items()
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

// TakeN ports _rotate_proxy_pool_values (app.py:17316): pop the first n and
// append them to the tail, so the pool cycles and never shrinks. Asking for
// more than the pool holds yields the whole pool once (no duplicates) and
// leaves the order unchanged. n <= 0 or an empty pool leaves the text alone —
// app.py returns before it touches the widget, so a blank-but-not-empty pool
// keeps its whitespace.
func (p *Pool) TakeN(n int) []string {
	p.mu.Lock()
	items := p.items()
	if n <= 0 || len(items) == 0 {
		p.mu.Unlock()
		return nil
	}
	if n > len(items) {
		n = len(items)
	}
	taken := append([]string(nil), items[:n]...)
	p.text = strings.Join(append(append([]string(nil), items[n:]...), taken...), "\n")
	p.mu.Unlock()
	p.notify()
	return taken
}

// Take pops one proxy and rotates it to the tail, or returns "" when empty.
func (p *Pool) Take() string {
	taken := p.TakeN(1)
	if len(taken) == 0 {
		return ""
	}
	return taken[0]
}

// nonEmptyLines is `[line.strip() for line in widget.get(...).splitlines() if
// line.strip()]`, the view _remove_register_dynamic_proxy_value (app.py:17342)
// and _remove_dynamic_proxy_values_everywhere (app.py:17365) take of the pool.
// Note this is RAW LINES, not parse_proxy_pool_text output: a line holding two
// URLs is one removal candidate here but two entries to every reader.
func (p *Pool) nonEmptyLines() []string {
	var out []string
	for _, line := range pySplitlines(p.text) {
		if stripped := pyStrip(line); stripped != "" {
			out = append(out, stripped)
		}
	}
	return out
}

// Remove drops the FIRST line matching proxyURL, mirroring
// _remove_register_dynamic_proxy_value (app.py:17342) which stops after one hit.
func (p *Pool) Remove(proxyURL string) bool {
	target := NormalizeProxyURL(proxyURL)
	if target == "" {
		return false
	}
	p.mu.Lock()
	lines := p.nonEmptyLines()
	kept := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		if !removed && NormalizeProxyURL(line) == target {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if removed {
		p.text = strings.Join(kept, "\n")
	}
	p.mu.Unlock()
	if removed {
		p.notify()
	}
	return removed
}

// RemoveAll drops every line whose normalized form is in targets and returns
// how many lines went, mirroring the per-pool half of
// _remove_dynamic_proxy_values_everywhere (app.py:17365).
func (p *Pool) RemoveAll(targets map[string]bool) int {
	if len(targets) == 0 {
		return 0
	}
	p.mu.Lock()
	lines := p.nonEmptyLines()
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if targets[NormalizeProxyURL(line)] {
			continue
		}
		kept = append(kept, line)
	}
	removed := len(lines) - len(kept)
	if removed > 0 {
		p.text = strings.Join(kept, "\n")
	}
	p.mu.Unlock()
	if removed > 0 {
		p.notify()
	}
	return removed
}

// PoolView is one pool's slice of the pools-updated event payload (UI_SPEC §4.2).
type PoolView struct {
	Role      string `json:"role"`
	Text      string `json:"text"`
	Remaining int    `json:"remaining"`
	Title     string `json:"title"`
}

// Snapshot is the pools-updated payload. Fields, not a map, so the frontend and
// the JSON key order stay fixed.
type Snapshot struct {
	Mode      string   `json:"mode"`
	LocalOnly bool     `json:"localOnly"`
	Register  PoolView `json:"register"`
	Create    PoolView `json:"create"`
	Followup  PoolView `json:"followup"`
	Approve   PoolView `json:"approve"`
}

// Set owns the four pools, the three reuse-proxy overrides and the route mode.
// The route mode lives here rather than in each caller because UI_SPEC G6 needs
// the gate to be unbypassable: any caller that forgets to check 全走本地代理
// would leak a dynamic proxy into a run that is supposed to be local-only.
type Set struct {
	mu       sync.RWMutex
	mode     string
	reuse    map[Role]string
	onChange func()

	pools map[Role]*Pool // written once in NewSet; never reassigned
}

// NewSet builds an empty set in 照旧 mode.
func NewSet() *Set {
	s := &Set{
		mode:  RouteModeDefault,
		reuse: map[Role]string{},
		pools: map[Role]*Pool{},
	}
	for _, role := range Roles {
		s.pools[role] = &Pool{}
	}
	return s
}

// SetOnChange installs a callback fired after any mutation (pool edit, take,
// removal, mode or reuse change). It is invoked with no locks held.
func (s *Set) SetOnChange(fn func()) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
	for _, role := range Roles {
		pool := s.pools[role]
		pool.mu.Lock()
		pool.onChange = s.fire
		pool.mu.Unlock()
	}
}

func (s *Set) fire() {
	s.mu.RLock()
	cb := s.onChange
	s.mu.RUnlock()
	if cb != nil {
		cb()
	}
}

// Pool exposes the raw pool for a role; nil for an unknown role.
func (s *Set) Pool(role Role) *Pool { return s.pools[role] }

// Mode returns the current 代理模式.
func (s *Set) Mode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// SetMode applies the combo value through NormalizeRouteMode and returns the
// mode actually stored.
func (s *Set) SetMode(value string) string {
	mode := NormalizeRouteMode(value)
	s.mu.Lock()
	changed := s.mode != mode
	s.mode = mode
	s.mu.Unlock()
	if changed {
		s.fire()
	}
	return mode
}

// LocalOnly ports _local_proxy_only_enabled (app.py:16712). Provider-pool
// owners must also consult this: _provider_roles_needed_for_link (app.py:16882)
// returns no roles at all in this mode, and the manager is stopped.
func (s *Set) LocalOnly() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode == RouteModeLocalOnly
}

// SetText replaces one pool from textarea content.
func (s *Set) SetText(role Role, text string) {
	if pool := s.pools[role]; pool != nil {
		pool.SetText(text)
	}
}

// Text is the editor/persisted content of one pool. Not gated: 全走本地代理
// hides the pools from readers, it does not erase them.
func (s *Set) Text(role Role) string {
	if pool := s.pools[role]; pool != nil {
		return pool.Text()
	}
	return ""
}

// Remaining is the （剩余 N） count for one pool. Not gated — see Pool.Remaining.
func (s *Set) Remaining(role Role) int {
	if pool := s.pools[role]; pool != nil {
		return pool.Remaining()
	}
	return 0
}

// List is _read_*_dynamic_proxies (app.py:16725-16743): the whole pool, or
// nothing at all under 全走本地代理.
func (s *Set) List(role Role) []string {
	if s.LocalOnly() {
		return nil
	}
	if pool := s.pools[role]; pool != nil {
		return pool.List()
	}
	return nil
}

// Peek is _peek_*_dynamic_proxy (app.py:17438-17454): head without rotating.
func (s *Set) Peek(role Role) string {
	if s.LocalOnly() {
		return ""
	}
	if pool := s.pools[role]; pool != nil {
		return pool.Peek()
	}
	return ""
}

// Take is _take_*_dynamic_proxy: rotate one out, "" under 全走本地代理.
func (s *Set) Take(role Role) string {
	if s.LocalOnly() {
		return ""
	}
	if pool := s.pools[role]; pool != nil {
		return pool.Take()
	}
	return ""
}

// TakeN is _take_dynamic_proxies(count) / _take_payment_dynamic_proxies(count).
func (s *Set) TakeN(role Role, n int) []string {
	if s.LocalOnly() {
		return nil
	}
	if pool := s.pools[role]; pool != nil {
		return pool.TakeN(n)
	}
	return nil
}

// TakeAuth ports _take_auth_dynamic_proxy_for_worker + its event handler
// (app.py:17412/17424). This is the call that made the cross-thread
// take-auth-proxy RPC necessary in Tk; UI_SPEC §4.2 deletes that RPC, so
// workers call this directly.
func (s *Set) TakeAuth(usePaymentProxyForRegister bool) string {
	if usePaymentProxyForRegister {
		return s.Take(RoleCreate)
	}
	return s.Take(RoleRegister)
}

// TakeFollowupOrCreate ports _take_followup_or_payment_dynamic_proxy
// (app.py:17486): the followup pool first, else the 第一步 pool.
func (s *Set) TakeFollowupOrCreate() string {
	if proxy := s.Take(RoleFollowup); proxy != "" {
		return proxy
	}
	return s.Take(RoleCreate)
}

// Remove drops the first matching entry from one pool.
func (s *Set) Remove(role Role, proxyURL string) bool {
	if pool := s.pools[role]; pool != nil {
		return pool.Remove(proxyURL)
	}
	return false
}

// RemoveEverywhere ports _remove_dynamic_proxy_values_everywhere
// (app.py:17365): 清理无效代理 prunes a dead proxy from all four pools at once,
// not just the one it was taken from. Returns the per-role removal counts (in
// Roles order) and the total.
func (s *Set) RemoveEverywhere(proxyURLs []string) (map[Role]int, int) {
	targets := map[string]bool{}
	for _, value := range proxyURLs {
		if normalized := NormalizeProxyURL(value); normalized != "" {
			targets[normalized] = true
		}
	}
	counts := map[Role]int{}
	total := 0
	if len(targets) == 0 {
		return counts, 0
	}
	for _, role := range Roles {
		removed := s.pools[role].RemoveAll(targets)
		if removed > 0 {
			counts[role] = removed
			total += removed
		}
	}
	return counts, total
}

// SetReuse stores a 复用代理 override verbatim (S17 reuse entries). Only
// create/followup/approve have one; a register role is accepted and ignored to
// keep callers loop-friendly.
func (s *Set) SetReuse(role Role, value string) {
	if role == RoleRegister {
		return
	}
	s.mu.Lock()
	changed := s.reuse[role] != value
	s.reuse[role] = value
	s.mu.Unlock()
	if changed {
		s.fire()
	}
}

// ReuseText is the raw persisted value of a reuse entry (never gated — the
// textbox keeps showing what the user typed).
func (s *Set) ReuseText(role Role) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reuse[role]
}

// Reuse is the effective per-stage override, i.e. the first half of
// _reuse_link_proxy_for_region (app.py:16852): "" under 全走本地代理, otherwise
// the normalized entry. "" means "fall back to the pool".
//
// The region rewrite/inversion that app.py applies afterwards is UI_SPEC G22
// and deliberately lives outside this package; a G22 caller wraps this result.
func (s *Set) Reuse(role Role) string {
	if s.LocalOnly() {
		return ""
	}
	return NormalizeProxyURL(s.ReuseText(role))
}

// Snapshot builds the pools-updated payload.
func (s *Set) Snapshot() Snapshot {
	view := func(role Role) PoolView {
		count := s.Remaining(role)
		return PoolView{
			Role:      string(role),
			Text:      s.Text(role),
			Remaining: count,
			Title:     role.Title(count),
		}
	}
	return Snapshot{
		Mode:      s.Mode(),
		LocalOnly: s.LocalOnly(),
		Register:  view(RoleRegister),
		Create:    view(RoleCreate),
		Followup:  view(RoleFollowup),
		Approve:   view(RoleApprove),
	}
}
