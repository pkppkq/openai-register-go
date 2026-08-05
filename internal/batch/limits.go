package batch

import "github.com/pkppkq/openai-register-go/internal/settings"

// User-facing account statuses, verbatim from app.py. They are matched by
// string elsewhere in the app (the failure_statuses set at app.py:18619 and
// app.py:20083), so they must not be reworded.
const (
	// StatusProxyExhausted is 代理耗尽: the pool ran dry before this account
	// could get a proxy (app.py:17687, 23619, 23627, 23660, 23478).
	StatusProxyExhausted = "代理耗尽"
	// StatusAttemptsExhausted is 提取长链失败, reported once an account burns
	// link_attempt_limit attempts (app.py:23545, 23647, 23713).
	StatusAttemptsExhausted = "提取长链失败"
	// StatusFailed is the exception_status default 失败 (app.py:17712).
	StatusFailed = "失败"
)

// ClampAuthConcurrency ports the window sizing at app.py:17622-17623:
//
//	concurrency = min(max(1, value), MAX_AUTH_CONCURRENCY, max(1, len(accounts)))
//
// DIVERGENCE: app.py:17622 also maps the Tk values "" and None to
// DEFAULT_AUTH_CONCURRENCY. In Go the value arrives as an int from
// settings.Settings.AuthConcurrency, where snapshot.go:175 already applied that
// blank-to-default substitution, so 0 simply clamps up to 1 exactly as the
// max(1, …) here does.
func ClampAuthConcurrency(value, jobCount int) int {
	if jobCount < 1 {
		jobCount = 1
	}
	concurrency := max(1, value)
	concurrency = min(concurrency, settings.MaxAuthConcurrency)
	return min(concurrency, jobCount)
}

// CapConcurrencyByProxyPool ports app.py:17624-17629: when a proxy pool is in
// use and holds fewer entries than the requested window, the window drops to
// the pool size so one proxy is not shared by several accounts at once. A
// poolSize of 0 means "unknown / not pool-backed" and caps nothing.
func CapConcurrencyByProxyPool(concurrency, poolSize int) int {
	if poolSize > 0 && concurrency > poolSize {
		return poolSize
	}
	return concurrency
}

// ClampRaceConcurrency ports _link_race_concurrency (app.py:17029-17033) and
// the defensive re-clamp inside the retry workers (app.py:23605, 23434):
// min(30, max(1, value or 1)).
func ClampRaceConcurrency(value int) int {
	return min(settings.MaxLinkRaceConcurrency, max(1, value))
}

// ClampAttemptLimit ports _link_attempt_limit (app.py:12473-12477) and the
// re-clamp at app.py:23606: min(10000, max(1, value or 1)).
func ClampAttemptLimit(value int) int {
	return min(settings.MaxLinkAttemptLimit, max(settings.MinLinkAttemptLimit, value))
}

// Messages are the log lines the orchestrator emits through Options.Log. They
// are data rather than constants because the 认证 batch and the 提链 batch print
// different Chinese for the same event. An empty field suppresses that line.
//
// Fields documented with a verb are fed to fmt.Sprintf; every other field is
// logged as-is.
type Messages struct {
	// ConcurrencyWindow (one %d) is logged once when the clamped window is
	// greater than 1. app.py:17631.
	ConcurrencyWindow string
	// ConcurrencyCapped (%d pool size, %d original window) is logged when
	// CapConcurrencyByProxyPool lowers the window. app.py:17627.
	ConcurrencyCapped string
	// ProxyExhausted is logged per account when no proxy can be taken.
	// app.py:17686 / 23626.
	ProxyExhausted string
	// AttemptLimit (one %d) is logged when an account burns its attempts.
	// app.py:23646.
	AttemptLimit string
	// Stopped is logged once after the pool drains under cancellation.
	// app.py:17663 / 23715.
	Stopped string
	// RoundFailed is logged between attempts when the round raced exactly one
	// proxy. app.py:23643.
	RoundFailed string
	// RaceRoundFailed (one %d: proxies raced) replaces RoundFailed when the
	// round raced more than one. app.py:23709.
	RaceRoundFailed string
}

// AuthMessages is the 注册/登录 wording of _run_accounts / _run_account_thread
// (app.py:17609-17714). AttemptLimit is empty because that loop has no attempt
// cap — see Options.UnlimitedAttempts.
func AuthMessages() Messages {
	return Messages{
		ConcurrencyWindow: "注册/登录认证并发窗口数: %d",
		ConcurrencyCapped: "认证并发已按代理池数量自动降到 %d（原设置 %d；避免同一代理被多个账号同时挤爆）",
		ProxyExhausted:    "认证代理池已耗尽，停止该账号",
		Stopped:           "任务已手动停止",
	}
}

// LinkMessages is the 批量提链 wording of _generate_opll_link_retry_worker
// (app.py:23602-23715).
//
// ConcurrencyWindow is empty on purpose: the Tk link batch spawns one unbounded
// thread per account (app.py:23316-23323) and has no window line to port. The
// caller announces the run itself with app.py:23268
// ("批量并发提取选中长链启动: %d 个账号，单账号撞链并发=%d，每账号最多重试=%d").
func LinkMessages() Messages {
	return Messages{
		ProxyExhausted:  "支付代理池已耗尽，停止重试",
		AttemptLimit:    "已达到每账号最多尝试次数 %d，停止重试",
		Stopped:         "批量生成支付链接已停止",
		RoundFailed:     "支付链接生成失败，当前代理组已放回队尾，继续按尝试次数重试",
		RaceRoundFailed: "本轮 %d 路撞链均失败，代理组已放回队尾，继续按尝试次数重试",
	}
}
