package sessionconv

import (
	"errors"
	"math"
	"strings"
	"time"
)

// ErrTimestampOutOfRange is the OSError CPython raises out of
// datetime.fromtimestamp(seconds, timezone.utc) when `seconds` is outside the
// Windows CRT _gmtime64_s window [-43200, 32536850399] — [1969-12-31T12:00Z,
// 3001-01-19T21:59:59Z]. app.py:5086 does NOT guard that call, so a session
// record carrying `"expires": 1e15` aborts the whole conversion and the account
// lands in the skip list with this exact text.
//
// The bound is applied unconditionally rather than behind runtime.GOOS: app.py
// is a Windows Tk desktop app, this port replaces it on the same machine, and
// the alternative for Go is emitting a plausible-looking wrong date. Before
// this existed, `"expires": 32536850399` produced "1831-12-11T22:50:51.580Z"
// (int64 nanosecond overflow) where Python produced "3001-01-19T21:59:59.000Z".
var ErrTimestampOutOfRange = errors.New("[Errno 22] Invalid argument")

const (
	gmtimeMinSeconds = int64(-43200)
	gmtimeMaxSeconds = int64(32536850399)
)

// parseISO mirrors datetime.fromisoformat. A naive result is read as UTC,
// matching `date.replace(tzinfo=timezone.utc)` at app.py:5095-5096.
//
// No pyStrip here: app.py hands this function an already-stripped string, and
// fromisoformat itself rejects surrounding whitespace.
func parseISO(text string) (time.Time, bool) {
	res, ok := pyFromISOFormat(text)
	if !ok {
		return time.Time{}, false
	}
	return res.t, true
}

// timeFromUnixFloat mirrors datetime.fromtimestamp(seconds, timezone.utc).
//
// CPython (_PyTime_DoubleToDenominator) splits the double with modf FIRST and
// only then scales the fraction to microseconds, rounding half-to-even and
// carrying. Scaling the whole value (seconds*1e6) instead loses precision above
// 2**53 microseconds — about the year 2255 — and then overflows int64 when
// converted to nanoseconds.
func timeFromUnixFloat(seconds float64) (time.Time, error) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return time.Time{}, ErrTimestampOutOfRange
	}
	intPart, frac := math.Modf(seconds)
	micros := math.RoundToEven(frac * 1e6)
	if micros >= 1e6 {
		micros -= 1e6
		intPart++
	} else if micros < 0 {
		micros += 1e6
		intPart--
	}
	if intPart < float64(gmtimeMinSeconds) || intPart > float64(gmtimeMaxSeconds) {
		return time.Time{}, ErrTimestampOutOfRange
	}
	return time.Unix(int64(intPart), int64(micros)*1000).UTC(), nil
}

// isoMillis mirrors `.astimezone(timezone.utc).isoformat(timespec="milliseconds")
// .replace("+00:00", "Z")`: exactly three fractional digits, truncated (Go's
// ".000" truncates too, so this matches).
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// isoSeconds mirrors `.isoformat(timespec="seconds").replace("+00:00", "Z")`,
// used by build_sub2api_export (app.py:5056).
func isoSeconds(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05") + "Z"
}

// NormalizeISOTimestamp ports normalize_iso_timestamp (app.py:5079-5097).
//
// Accepts a time.Time, a JSON number (seconds, or milliseconds when > 1e11) or
// an ISO-8601 string; anything unparseable yields "". The error is
// ErrTimestampOutOfRange and only the numeric branch can raise it — app.py
// leaves that OSError uncaught in every caller but get_axonhub_last_refresh.
func NormalizeISOTimestamp(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	var date time.Time
	if t, ok := value.(time.Time); ok {
		date = t
	} else if f, ok := pyIsNumber(value); ok && f > 0 {
		seconds := f
		if f > 1e11 {
			seconds = f / 1000
		}
		parsed, err := timeFromUnixFloat(seconds)
		if err != nil {
			return "", err
		}
		date = parsed
	} else {
		// Python: `text = str(value or "").strip()` — a falsy value (0, False,
		// "") collapses to "" before the strip, so 0 returns "" rather than "0".
		text := ""
		if pyTruthy(value) {
			text = pyStrip(pyStr(value))
		}
		if text == "" {
			return "", nil
		}
		parsed, ok := parseISO(strings.ReplaceAll(text, "Z", "+00:00"))
		if !ok {
			return "", nil
		}
		date = parsed
	}
	return isoMillis(date), nil
}

// normalizeISOTimestampSwallow is normalize_iso_timestamp wrapped in the
// `except Exception: pass` of get_axonhub_last_refresh (app.py:5150-5151), the
// one caller that survives an out-of-range timestamp.
func normalizeISOTimestampSwallow(value any) string {
	text, err := NormalizeISOTimestamp(value)
	if err != nil {
		return ""
	}
	return text
}

// TimestampFromUnixSeconds ports timestamp_from_unix_seconds (app.py:5100-5107).
// Unlike NormalizeISOTimestamp this one goes through float(value), so numeric
// strings are accepted. Only the float() call is guarded by Python's try, so an
// out-of-range value still propagates.
func TimestampFromUnixSeconds(value any) (string, error) {
	numeric, ok := pyFloat(value)
	if !ok {
		return "", nil
	}
	if !(numeric > 0) {
		return "", nil
	}
	return NormalizeISOTimestamp(numeric)
}

// UnixSecondsFromJWTExp ports unix_seconds_from_jwt_exp (app.py:5110-5115).
func UnixSecondsFromJWTExp(value any) int64 {
	numeric, ok := pyFloat(value)
	if !ok {
		return 0
	}
	if numeric > 0 {
		return pyIntTrunc(numeric)
	}
	return 0
}

// EpochSecondsFromValue ports epoch_seconds_from_value (app.py:5118-5131).
//
// The normalize_iso_timestamp call sits inside the `except` arm, so it only
// ever runs on a value float() rejected — a string, which cannot reach the
// out-of-range branch. Hence no error out of this one.
func EpochSecondsFromValue(value any) int64 {
	if value == nil {
		return 0
	}
	if s, ok := value.(string); ok && s == "" {
		return 0
	}
	if numeric, ok := pyFloat(value); ok && !math.IsInf(numeric, 0) && !math.IsNaN(numeric) {
		if numeric > 1e11 {
			return pyIntTrunc(numeric / 1000)
		}
		return pyIntTrunc(numeric)
	}
	timestamp := normalizeISOTimestampSwallow(value)
	if timestamp == "" {
		return 0
	}
	parsed, ok := parseISO(strings.ReplaceAll(timestamp, "Z", "+00:00"))
	if !ok {
		return 0
	}
	return parsed.Unix()
}

// pyDatetimeTimestamp is datetime.timestamp() for a parsed ISO value: exact
// arithmetic when the value is tz-aware, the LOCAL zone when it is naive
// (app.py:4928, 5149). ok is false where CPython raises.
func pyDatetimeTimestamp(res isoResult) (float64, bool) {
	t := res.t
	if !res.aware {
		t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.Local)
	}
	seconds := t.Unix()
	if !res.aware && (seconds < gmtimeMinSeconds || seconds > gmtimeMaxSeconds) {
		// See the DIVERGENCE note on ParseExpiredTime: a naive timestamp() goes
		// through the platform mktime, whose exact window depends on the local
		// zone; the CRT gmtime window is the closest reproducible approximation.
		return 0, false
	}
	return float64(seconds) + float64(t.Nanosecond())/1e9, true
}

// GetExpiresIn ports get_expires_in (app.py:5134-5142). Returns nil for the
// Python None so strip_unavailable can drop the key; 0 is a real value.
//
// DIVERGENCE: `expires - now` in Python is a TypeError when expires_at parses
// NAIVE and now is tz-aware ("can't subtract offset-naive and offset-aware
// datetimes"), which would abort the conversion. Unreachable in app.py — the
// only caller (app.py:5294) passes the output of normalize_iso_timestamp, which
// always ends in "Z" — so the naive value is simply read as UTC here rather
// than growing an error return nothing can trigger.
func GetExpiresIn(expiresAt string, now time.Time) any {
	if expiresAt == "" {
		return nil
	}
	expires, ok := parseISO(strings.ReplaceAll(expiresAt, "Z", "+00:00"))
	if !ok {
		return nil
	}
	// Python: max(0, int((expires - now).total_seconds())) — int() truncates
	// toward zero, and total_seconds() is an exact microsecond count divided by
	// 1e6. expires.Sub(now) is NOT usable: time.Duration is int64 nanoseconds
	// and saturates at 9223372036.854s, so a year-9999 expiry used to report
	// 9223372036 where Python reports 251617231503.
	delta := float64(unixMicros(expires)-unixMicros(now)) / 1e6
	secs := pyIntTrunc(delta)
	if secs < 0 {
		secs = 0
	}
	return secs
}

// unixMicros is t as whole microseconds since the epoch, the resolution Python
// datetimes carry.
func unixMicros(t time.Time) int64 {
	return t.Unix()*1e6 + int64(t.Nanosecond())/1000
}

// GetAxonHubLastRefresh ports get_axonhub_last_refresh (app.py:5145-5152):
// one hour before the access-token expiry, else "now".
//
// The try/except wraps BOTH the parse and the normalize call, so an expiry that
// lands outside the gmtime window — anything from year 3001 to about 5138, e.g.
// "4000-01-01T00:00:00Z" — falls back to `now` rather than to "". And
// .timestamp() on a naive expiry is read in the LOCAL zone, not as UTC.
func GetAxonHubLastRefresh(expiresAt string, now time.Time) string {
	if expiresAt != "" {
		if res, ok := pyFromISOFormat(strings.ReplaceAll(expiresAt, "Z", "+00:00")); ok {
			if epoch, ok := pyDatetimeTimestamp(res); ok {
				// Python routes through the float timestamp, so a non-positive
				// result falls into the string branch and yields "".
				if text, err := NormalizeISOTimestamp(epoch - 3600); err == nil {
					return text
				}
			}
		}
	}
	// now is a time.Time, which cannot take the numeric branch.
	text, _ := NormalizeISOTimestamp(now)
	return text
}
