package providerproxy

import "time"

// Clock is the manager's only source of time. It exists so that the backoff
// window (app.py:1271-1272), the take deadline (app.py:1173) and the pump's
// idle wait (app.py:1246) can be exercised without a real second passing —
// which matters here more than usual, because the alternative to waiting is
// minting, and every mint is a billed provider session.
//
// Python reads time.monotonic() throughout; Go's time.Now() carries a monotonic
// reading that Sub/Before/After use, so the semantics line up.
type Clock interface {
	// Now is time.monotonic().
	Now() time.Time
	// After is the timeout arm of threading.Condition.wait(timeout=…).
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the real clock, used unless WithClock says otherwise.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) After(d time.Duration) <-chan time.Time {
	if d <= 0 {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	return time.After(d)
}
