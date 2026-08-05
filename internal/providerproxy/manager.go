package providerproxy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// Detector probes one freshly minted candidate and returns its exit string.
// It is the port of GUI._detect_provider_proxy_candidate (app.py:12644-12649),
// which chains the local proxy in front of the provider URL and runs
// detect_proxy_health against it.
//
// A non-nil error becomes "检测失败: <err>", matching the bare `except Exception
// as exc` at app.py:1251-1252; a returned string is str.strip()ed first
// (app.py:1250). A panic is recovered and treated the same as an error, because
// in Python an exception in the worker thread lands in the future, not in the
// pump.
//
// This is the package's ONLY outbound call. Tests must never supply one that
// touches the network: a probe of a minted URL is a billed provider session.
type Detector func(ctx context.Context, candidate Candidate, localProxy string) (string, error)

// Option configures a Manager. Defaults mirror ProviderProxyPoolManager's
// keyword defaults (app.py:1052-1066, clamped at app.py:1064-1066).
type Option func(*Manager)

// WithStatusCallback is the status_callback of app.py:1055 — invoked, outside
// the lock, whenever a role's snapshot changes (app.py:1191-1195). app.py posts
// it onto the Tk event queue (app.py:12641-12642).
func WithStatusCallback(fn func(role proxypool.Role, status Status)) Option {
	return func(m *Manager) { m.statusCB = fn }
}

// WithValidatedCallback is the validated_callback of app.py:1056, fired for
// each candidate that passes its exit check.
//
// It runs while the manager lock is held (app.py:1265) — Python gets away with
// that because threading.Condition wraps a re-entrant RLock, Go's sync.Mutex is
// not re-entrant. The callback must not call back into the Manager.
func WithValidatedCallback(fn func(candidate Candidate)) Option {
	return func(m *Manager) { m.validatedCB = fn }
}

// WithClock injects the clock. Default SystemClock().
func WithClock(clock Clock) Option {
	return func(m *Manager) { m.clock = clock }
}

// WithSIDSource injects the session-id generator (app.py:1033-1035). Default
// RandomProviderSID. A test can pin it to make minted URLs comparable.
func WithSIDSource(fn func() (string, error)) Option {
	return func(m *Manager) { m.newSID = fn }
}

// WithContext sets the context handed to the Detector. Default
// context.Background().
//
// Stop deliberately does NOT cancel it: app.py:1100 shuts the executor down
// with wait=False, cancel_futures=True, which drops queued work but lets
// running probes finish. Cancel this context to abort in-flight probes at app
// shutdown.
func WithContext(ctx context.Context) Option {
	return func(m *Manager) { m.baseCtx = ctx }
}

// WithStock overrides target_stock / low_water (app.py:1057-1058). The clamps
// are Python's: target = max(1, target), low = min(target, max(0, low)).
func WithStock(target, lowWater int) Option {
	return func(m *Manager) {
		m.targetStock = target
		m.lowWater = lowWater
	}
}

// WithMaxWorkers overrides max_workers (app.py:1059); see UpdateMaxWorkers.
func WithMaxWorkers(n int) Option {
	return func(m *Manager) { m.maxWorkers = n }
}

// Manager is ProviderProxyPoolManager (app.py:1051-1276): one background pump
// keeping a per-role queue of validated provider sessions topped up, so that
// 批量提链 can take a fresh exit per attempt without ever blocking on a mint.
//
// Every map below is keyed by role and is only ever READ by key — iteration
// goes over the Roles slice, never over the map.
type Manager struct {
	detector    Detector
	statusCB    func(proxypool.Role, Status)
	validatedCB func(Candidate)
	clock       Clock
	newSID      func() (string, error)
	baseCtx     context.Context

	targetStock int
	lowWater    int

	mu sync.Mutex
	// wake is threading.Condition.notify_all: closed and replaced under mu.
	wake chan struct{}
	// stopCh is _stop_event: closed by Stop, replaced by Start ("clear").
	stopCh      chan struct{}
	maxWorkers  int
	configs     map[proxypool.Role]settings.ProviderProxyConfig
	localProxy  string
	ready       map[proxypool.Role][]Candidate
	inflight    map[proxypool.Role]int
	refilling   map[proxypool.Role]bool
	regionIndex map[proxypool.Role]int
	failures    map[proxypool.Role]int
	nextAllowed map[proxypool.Role]time.Time
	generation  map[proxypool.Role]int
	roundRobin  int
	running     bool
	done        chan struct{}
	lastErr     error
}

// New builds a stopped Manager. Nothing is minted until Configure (which calls
// Start, app.py:1138) or Start runs.
func New(detector Detector, opts ...Option) *Manager {
	m := &Manager{
		detector:    detector,
		clock:       SystemClock(),
		newSID:      RandomProviderSID,
		baseCtx:     context.Background(),
		targetStock: TargetStock,
		lowWater:    LowWater,
		maxWorkers:  MaxWorkers,
		wake:        make(chan struct{}),
		stopCh:      make(chan struct{}),
		configs:     make(map[proxypool.Role]settings.ProviderProxyConfig, len(Roles)),
		ready:       make(map[proxypool.Role][]Candidate, len(Roles)),
		inflight:    make(map[proxypool.Role]int, len(Roles)),
		refilling:   make(map[proxypool.Role]bool, len(Roles)),
		regionIndex: make(map[proxypool.Role]int, len(Roles)),
		failures:    make(map[proxypool.Role]int, len(Roles)),
		nextAllowed: make(map[proxypool.Role]time.Time, len(Roles)),
		generation:  make(map[proxypool.Role]int, len(Roles)),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	// app.py:1064-1066, in that order.
	if m.targetStock < 1 {
		m.targetStock = 1
	}
	if m.lowWater < 0 {
		m.lowWater = 0
	}
	if m.lowWater > m.targetStock {
		m.lowWater = m.targetStock
	}
	if m.maxWorkers < 1 {
		m.maxWorkers = 1
	}
	// app.py:1069: every role starts on ProxyProviderConfig(), i.e. the
	// dataclass defaults (duration 5, regions "JP") — NOT a zero value. The
	// distinction is visible through Configure's `config != self._configs[role]`
	// equality test at app.py:1129.
	for _, role := range Roles {
		m.configs[role] = settings.DefaultProviderProxyConfig()
	}
	return m
}

// ---------------------------------------------------------------------------
// Condition-variable plumbing
// ---------------------------------------------------------------------------

// notifyAll is Condition.notify_all(); the caller must hold mu.
func (m *Manager) notifyAll() {
	close(m.wake)
	m.wake = make(chan struct{})
}

// waitLocked is Condition.wait(timeout=d): the caller holds mu, the lock is
// dropped for the duration of the wait and re-taken before returning. Reading
// m.wake under mu before unlocking is what makes a notify impossible to miss.
func (m *Manager) waitLocked(d time.Duration) {
	wake := m.wake
	m.mu.Unlock()
	select {
	case <-wake:
	case <-m.clock.After(d):
	}
	m.mu.Lock()
}

func isClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start ports start() (app.py:1082-1089): idempotent while the pump is alive,
// and it clears the stop flag so a previously stopped Manager can run again.
func (m *Manager) Start() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.stopCh = make(chan struct{}) // _stop_event.clear()
	m.running = true
	m.lastErr = nil
	stopCh := m.stopCh
	done := make(chan struct{})
	m.done = done
	m.mu.Unlock()
	go m.run(stopCh, done)
}

// Stop ports stop() (app.py:1091-1102) and is what app.py calls when the route
// mode flips to 全走本地代理 (app.py:16719 via _on_proxy_route_mode_changed,
// and app.py:12509 when 应用 is pressed in that mode), plus on window close
// (app.py:14314).
//
// It sets the stop flag, wakes every waiter, and joins the pump with the same
// 3 s bound as thread.join(timeout=3). In-flight probes are NOT cancelled —
// app.py:1100 passes wait=False — so a late completion can still land in the
// ready queue; the generation counter, not the stop flag, is what invalidates
// stock.
func (m *Manager) Stop() {
	m.mu.Lock()
	if !isClosed(m.stopCh) {
		close(m.stopCh)
	}
	m.notifyAll()
	done := m.done
	m.done = nil
	m.mu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-m.clock.After(3 * time.Second): // app.py:1097
		}
	}
	m.mu.Lock()
	m.running = false // app.py:1101-1102: thread/executor dropped either way
	m.mu.Unlock()
}

// Running reports whether the pump goroutine is alive.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// LastError reports why the pump stopped early, if it did.
//
// DIVERGENCE: in Python a mint that raises inside _run (app.py:1236) kills the
// pump thread with an unhandled exception and nothing records it. Go returns
// the error here instead, with the same net effect — the pump is gone until
// Start runs again. It is unreachable through Configure, which has already
// validated every enabled role.
func (m *Manager) LastError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}

// UpdateMaxWorkers ports update_max_workers (app.py:1104-1119). app.py drives
// it from link_proxy_precheck_concurrency on every 应用 (app.py:12520).
//
// Changing it bumps every role's generation and zeroes inflight, which orphans
// the probes already running: their results are discarded on arrival
// (app.py:1259). Their inflight decrement still happens and is floored at 0
// (app.py:1258), exactly as in Python.
func (m *Manager) UpdateMaxWorkers(maxWorkers int) {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if maxWorkers == m.maxWorkers {
		return
	}
	m.maxWorkers = maxWorkers
	for _, role := range Roles {
		m.generation[role]++
		m.inflight[role] = 0
	}
	// DIVERGENCE: Python also swaps the ThreadPoolExecutor here. This port
	// spawns one goroutine per probe under the same inflight cap the pump
	// already enforces (app.py:1218), so there is no queue to cancel and no
	// executor to replace.
	m.notifyAll()
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// ConfigsFromSettings adapts settings.Settings.ProviderProxyConfigs to the
// role-keyed map Configure wants. A role absent from the snapshot gets
// ProxyProviderConfig()'s dataclass defaults, matching
// `configs.get(role, ProxyProviderConfig())` at app.py:1127.
func ConfigsFromSettings(configs map[string]settings.ProviderProxyConfig) map[proxypool.Role]settings.ProviderProxyConfig {
	out := make(map[proxypool.Role]settings.ProviderProxyConfig, len(Roles))
	for _, role := range Roles {
		if config, ok := configs[string(role)]; ok {
			out[role] = config
			continue
		}
		out[role] = settings.DefaultProviderProxyConfig()
	}
	return out
}

// Configure ports configure() (app.py:1121-1139).
//
// localProxy is normalized through proxypool.NormalizeProxyURL first
// (app.py:1123) — the same normalize the chain server needs so that a socks5://
// local proxy resolves DNS remotely rather than locally.
//
// A role whose config OR the shared local proxy changed has its generation
// bumped and its stock dropped: those sessions were minted for a different
// account or would exit through a different first hop.
//
// Configure returns the validation error unchanged (app.py:12533 renders it as
// 提供商代理配置无效: <err>) and, like Python, leaves the roles it already
// processed applied — the raise at app.py:1128 aborts mid-loop, having already
// stored _local_proxy and the earlier roles, and skips the notify, the start
// and the status publish.
func (m *Manager) Configure(configs map[proxypool.Role]settings.ProviderProxyConfig, localProxy string) error {
	m.mu.Lock()
	normalized := proxypool.NormalizeProxyURL(localProxy)
	localChanged := normalized != m.localProxy
	m.localProxy = normalized
	for _, role := range Roles {
		config, ok := configs[role]
		if !ok {
			config = settings.DefaultProviderProxyConfig()
		}
		if err := config.Validate(); err != nil { // app.py:1128
			m.mu.Unlock()
			return err
		}
		// Struct equality is Python's frozen-dataclass __eq__: all six fields
		// are comparable scalars.
		if localChanged || config != m.configs[role] {
			m.generation[role]++
			m.ready[role] = nil
			m.regionIndex[role] = 0
			m.failures[role] = 0
			m.nextAllowed[role] = time.Time{}
		}
		m.configs[role] = config
		m.refilling[role] = config.Enabled
	}
	m.notifyAll()
	m.mu.Unlock()

	m.Start()            // app.py:1138
	m.publishAllStatus() // app.py:1139
	return nil
}

// LocalProxy is the normalized first hop the probes chain through.
func (m *Manager) LocalProxy() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.localProxy
}

// Config returns the applied config for one role.
func (m *Manager) Config(role proxypool.Role) settings.ProviderProxyConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.configs[role]
}

// EnabledRoles ports enabled_roles (app.py:1141-1143) and is returned in Roles
// order, never map order.
func (m *Manager) EnabledRoles() []proxypool.Role {
	m.mu.Lock()
	defer m.mu.Unlock()
	roles := make([]proxypool.Role, 0, len(Roles))
	for _, role := range Roles {
		if m.configs[role].Enabled {
			roles = append(roles, role)
		}
	}
	return roles
}

// ReadyCount ports ready_count (app.py:1145-1147).
func (m *Manager) ReadyCount(role proxypool.Role) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ready[role])
}

// Snapshot ports snapshot (app.py:1149-1158).
func (m *Manager) Snapshot(role proxypool.Role) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(role)
}

func (m *Manager) snapshotLocked(role proxypool.Role) Status {
	return Status{
		Enabled:  m.configs[role].Enabled,
		Ready:    len(m.ready[role]),
		Inflight: m.inflight[role],
		Target:   m.targetStock,
		LowWater: m.lowWater,
		Failures: m.failures[role],
	}
}

// Snapshots returns every role's status in Roles order.
func (m *Manager) Snapshots() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(Roles))
	for _, role := range Roles {
		out = append(out, m.snapshotLocked(role))
	}
	return out
}

func (m *Manager) publishStatus(role proxypool.Role) {
	if m.statusCB == nil {
		return
	}
	status := m.Snapshot(role)
	// app.py:1192-1195 swallows anything the callback throws.
	defer func() { _ = recover() }()
	m.statusCB(role, status)
}

func (m *Manager) publishAllStatus() {
	for _, role := range Roles { // app.py:1197-1199
		m.publishStatus(role)
	}
}

// ---------------------------------------------------------------------------
// Consumption
// ---------------------------------------------------------------------------

// WaitUntilReady ports wait_until_ready (app.py:1160-1170). 批量提链 calls it
// with LowWater before the first account (app.py:23410).
//
// stop is the caller's task-stop signal (app.py passes self.stop_event); it is
// polled, not selected on, because Python polls it too — a stop is noticed
// within one 0.5 s wait.
//
// An EMPTY roles slice means ALL roles: app.py:1164 writes
// `set(roles or PROVIDER_PROXY_ROLES)`, and Python truthiness makes an empty
// tuple fall through to the default. Disabled roles never hold it up, and with
// no enabled role at all `all([])` is True, so it returns immediately.
func (m *Manager) WaitUntilReady(minimum int, stop <-chan struct{}, roles []proxypool.Role) bool {
	if minimum < 0 {
		minimum = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		wanted := roles
		if len(wanted) == 0 {
			wanted = Roles
		}
		satisfied := true
		for _, role := range Roles {
			if !containsRole(wanted, role) || !m.configs[role].Enabled {
				continue
			}
			if len(m.ready[role]) < minimum {
				satisfied = false
				break
			}
		}
		if satisfied {
			return true
		}
		if isClosed(m.stopCh) || isClosed(stop) {
			return false
		}
		m.waitLocked(500 * time.Millisecond)
	}
}

// Take ports take() (app.py:1172-1189): pop one validated session for role,
// waiting up to timeout. Reports ok == false for a disabled role (immediately),
// for a stop, and for the timeout — all three of which app.py:23366-23376
// treats the same way, by falling back to the manual pool.
//
// The candidate is consumed: it is never handed out twice.
func (m *Manager) Take(role proxypool.Role, timeout time.Duration, stop <-chan struct{}) (Candidate, bool) {
	if timeout < 0 {
		timeout = 0 // max(0.0, float(timeout)) — app.py:1173
	}
	deadline := m.clock.Now().Add(timeout)
	m.mu.Lock()
	if !m.configs[role].Enabled {
		m.mu.Unlock()
		return Candidate{}, false
	}
	for len(m.ready[role]) == 0 {
		if isClosed(m.stopCh) || isClosed(stop) {
			m.mu.Unlock()
			return Candidate{}, false
		}
		remaining := deadline.Sub(m.clock.Now())
		if remaining <= 0 {
			m.mu.Unlock()
			return Candidate{}, false
		}
		wait := 500 * time.Millisecond
		if remaining < wait {
			wait = remaining
		}
		m.waitLocked(wait)
	}
	candidate := m.ready[role][0]
	m.ready[role][0] = Candidate{} // drop the reference before reslicing
	m.ready[role] = m.ready[role][1:]
	if len(m.ready[role]) <= m.lowWater { // app.py:1185
		m.refilling[role] = true
	}
	m.notifyAll()
	m.mu.Unlock()
	m.publishStatus(role)
	return candidate, true
}

func containsRole(list []proxypool.Role, want proxypool.Role) bool {
	for _, role := range list {
		if role == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Minting and the background pump
// ---------------------------------------------------------------------------

// nextCandidateLocked ports _next_candidate_locked (app.py:1201-1207).
//
// THE ROTATION RULE. Two things rotate, and neither is a clock:
//
//  1. Region: a per-role cursor walks parse_provider_regions(regions_text)
//     round-robin, regions[index % len(regions)], and advances on every mint —
//     including mints that go on to fail their exit check.
//  2. Session: every mint draws a fresh 8-character sid, so every URL is a new
//     provider session. `duration` is not a local timer at all — it goes into
//     the URL as -t-N and tells the PROVIDER how many minutes to hold that exit
//     for that sid. Nothing in app.py ever expires a stocked candidate; a
//     candidate lives until Take consumes it, until the generation is bumped
//     (Configure / UpdateMaxWorkers), or until the process ends.
//
// The only clock-driven gate is the failure backoff (app.py:1234, 1284-1285).
func (m *Manager) nextCandidateLocked(role proxypool.Role) (Candidate, string, int, error) {
	config := m.configs[role]
	regions, err := settings.ParseProviderRegions(config.Regions)
	if err != nil {
		return Candidate{}, "", 0, err
	}
	index := m.regionIndex[role]
	region := regions[index%len(regions)]
	m.regionIndex[role] = index + 1

	sid := ""
	if m.newSID != nil {
		sid, err = m.newSID()
		if err != nil {
			return Candidate{}, "", 0, err
		}
	}
	url, err := BuildProxyURL(config, region, sid)
	if err != nil {
		return Candidate{}, "", 0, fmt.Errorf("%s: %w", RoleLabel(role), err)
	}
	return Candidate{Role: role, URL: url, Region: region}, m.localProxy, m.generation[role], nil
}

// run ports _run (app.py:1209-1246): the refill pump.
func (m *Manager) run(stopCh chan struct{}, done chan struct{}) {
	defer close(done)
	defer func() {
		m.mu.Lock()
		if m.stopCh == stopCh {
			m.running = false
		}
		m.mu.Unlock()
	}()

	for {
		if isClosed(stopCh) { // app.py:1210
			return
		}
		scheduled := false
		m.mu.Lock()
		// Stop 可能在循环顶部检查之后、取得锁之前关闭 stopCh。这里必须
		// 再检查一次，否则泵会改等新一代 wake，错过 Stop 已发出的通知。
		if isClosed(stopCh) {
			m.mu.Unlock()
			return
		}
		totalInflight := 0
		for _, role := range Roles { // sum(self._inflight.values()), app.py:1216
			totalInflight += m.inflight[role]
		}
		for offset := 0; offset < len(Roles); offset++ {
			if totalInflight >= m.maxWorkers { // app.py:1218
				break
			}
			role := Roles[(m.roundRobin+offset)%len(Roles)]
			config := m.configs[role]
			readyCount := len(m.ready[role])
			if !config.Enabled { // app.py:1224
				continue
			}
			if readyCount <= m.lowWater { // app.py:1226
				m.refilling[role] = true
			}
			if !m.refilling[role] {
				continue
			}
			if readyCount+m.inflight[role] >= m.targetStock { // app.py:1230
				if readyCount >= m.targetStock {
					m.refilling[role] = false
				}
				continue
			}
			if m.clock.Now().Before(m.nextAllowed[role]) { // app.py:1234
				continue
			}
			candidate, localProxy, generation, err := m.nextCandidateLocked(role)
			if err != nil {
				// See LastError: Python's pump thread dies here.
				m.lastErr = err
				m.mu.Unlock()
				return
			}
			m.inflight[role]++
			totalInflight++
			scheduled = true
			go m.probe(candidate, localProxy, generation)
		}
		m.roundRobin = (m.roundRobin + 1) % len(Roles) // app.py:1244
		if !scheduled {
			m.waitLocked(200 * time.Millisecond) // app.py:1246
		}
		m.mu.Unlock()
	}
}

// probe runs one exit check and hands the result to completeCheck. It is the
// executor.submit + add_done_callback pair of app.py:1240-1243 plus the
// try/except of app.py:1250-1252.
func (m *Manager) probe(candidate Candidate, localProxy string, generation int) {
	proxyExit := func() (out string) {
		defer func() {
			if r := recover(); r != nil {
				out = fmt.Sprintf("检测失败: %v", r)
			}
		}()
		if m.detector == nil {
			return "检测失败: detector 未配置"
		}
		result, err := m.detector(m.baseCtx, candidate, localProxy)
		if err != nil {
			// app.py:1252 — the error branch is NOT stripped.
			return "检测失败: " + err.Error()
		}
		return pyStrip(result) // app.py:1250
	}()
	m.completeCheck(candidate, generation, proxyExit)
}

// completeCheck ports _complete_check (app.py:1248-1276).
//
// A candidate is stocked only if the probe did not fail AND the country it
// actually exited from equals the region it asked for (app.py:1254) — the whole
// point of the pool is that a wrong exit never reaches a payment link.
func (m *Manager) completeCheck(candidate Candidate, generation int, proxyExit string) {
	actualCountry := ProxyExitCountry(proxyExit)
	passed := !ProxyExitFailed(proxyExit) && actualCountry == candidate.Region
	accepted := candidate
	accepted.ProxyExit = proxyExit
	role := candidate.Role

	m.mu.Lock()
	if m.inflight[role] > 0 { // max(0, x-1), app.py:1258
		m.inflight[role]--
	}
	if generation == m.generation[role] && m.configs[role].Enabled { // app.py:1259
		switch {
		case passed && len(m.ready[role]) < m.targetStock:
			m.ready[role] = append(m.ready[role], accepted)
			m.failures[role] = 0
			m.nextAllowed[role] = time.Time{} // _next_allowed = 0.0
			m.callValidatedLocked(accepted)
		case !passed:
			failures := m.failures[role] + 1
			m.failures[role] = failures
			index := failures - 1
			if index > len(BackoffSeconds)-1 { // app.py:1271
				index = len(BackoffSeconds) - 1
			}
			m.nextAllowed[role] = m.clock.Now().Add(BackoffSeconds[index])
		}
		// Note the nesting: app.py:1273 sits INSIDE the generation guard, and a
		// pass that arrives at a full queue takes neither branch above.
		if len(m.ready[role]) >= m.targetStock {
			m.refilling[role] = false
		}
	}
	m.notifyAll()
	m.mu.Unlock()
	m.publishStatus(role)
}

func (m *Manager) callValidatedLocked(candidate Candidate) {
	if m.validatedCB == nil {
		return
	}
	defer func() { _ = recover() }() // app.py:1264-1267
	m.validatedCB(candidate)
}
