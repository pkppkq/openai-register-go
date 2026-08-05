package sessionconv

import "time"

// This file ports CPython's datetime.fromisoformat, which every timestamp in
// this package is funnelled through (app.py:5092, 5129, 5138, 5148, 4928).
//
// A layout list is NOT good enough: since 3.11 fromisoformat accepts the ISO
// 8601 basic format ("20260825T091937"), week dates ("2026-W35-2"), a comma as
// the fractional separator, hour-only times, "+05" / "+05:30:15" offsets, any
// single character as the date/time separator, and it TRUNCATES a fraction
// longer than six digits instead of rejecting it. Each of those decides the
// "expired" / "expiresAt" bytes of an exported account, so the algorithm below
// follows CPython 3.12's Lib/_pydatetime.py (_find_isoformat_datetime_separator,
// _parse_isoformat_date, _parse_hh_mm_ss_ff, _parse_isoformat_time,
// _isoweek_to_gregorian) statement for statement.
//
// One deliberate narrowing: CPython's pure-Python fallback parses components
// with int(), which would also accept " 8" or "+8"; the C accelerator that
// actually runs requires ASCII digits, and so does this port.

type isoResult struct {
	t     time.Time // built in UTC when naive, in the parsed offset when aware
	aware bool
}

func isoASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// isoAtoi parses up to max ASCII digits starting at pos, mirroring Python's
// int(s[pos:pos+max]) — a SHORT slice is fine (int("5") == 5), an empty or
// non-digit one is a ValueError.
func isoAtoi(s string, pos, max int) (int, int, bool) {
	if pos < 0 || pos > len(s) {
		return 0, pos, false
	}
	end := pos + max
	if end > len(s) {
		end = len(s)
	}
	if end == pos {
		return 0, pos, false
	}
	value := 0
	for i := pos; i < end; i++ {
		if !isoASCIIDigit(s[i]) {
			return 0, pos, false
		}
		value = value*10 + int(s[i]-'0')
	}
	return value, end, true
}

// isoFindSeparator is _find_isoformat_datetime_separator.
func isoFindSeparator(s string) (int, bool) {
	n := len(s)
	if n == 7 {
		return 7, true
	}
	if n < 8 {
		return 0, false
	}
	if s[4] == '-' {
		if s[5] == 'W' {
			if n > 8 && s[8] == '-' {
				if n == 9 {
					return 0, false
				}
				if n > 10 && isoASCIIDigit(s[10]) {
					return 8, true
				}
				return 10, true
			}
			return 8, true
		}
		return 10, true
	}
	if s[4] == 'W' {
		idx := 7
		for idx < n && isoASCIIDigit(s[idx]) {
			idx++
		}
		if idx < 9 {
			return idx, true
		}
		if idx%2 == 0 {
			return 7, true
		}
		return 8, true
	}
	return 8, true
}

// isoParseDate is _parse_isoformat_date.
func isoParseDate(s string) (year, month, day int, ok bool) {
	year, pos, ok := isoAtoi(s, 0, 4)
	if !ok || pos != 4 {
		return 0, 0, 0, false
	}
	hasSep := len(s) > 4 && s[4] == '-'
	if hasSep {
		pos = 5
	}
	if pos < len(s) && s[pos] == 'W' {
		pos++
		week, next, ok := isoAtoi(s, pos, 2)
		if !ok {
			return 0, 0, 0, false
		}
		// Python advances by a fixed 2 even when the slice was shorter.
		pos += 2
		_ = next
		dayno := 1
		if len(s) > pos {
			if (s[pos] == '-') != hasSep {
				return 0, 0, 0, false
			}
			if hasSep {
				pos++
			}
			d, _, ok := isoAtoi(s, pos, 1)
			if !ok {
				return 0, 0, 0, false
			}
			dayno = d
		}
		return isoWeekToGregorian(year, week, dayno)
	}
	month, _, ok = isoAtoi(s, pos, 2)
	if !ok {
		return 0, 0, 0, false
	}
	pos += 2
	dash := pos < len(s) && s[pos] == '-'
	if dash != hasSep {
		return 0, 0, 0, false
	}
	if hasSep {
		pos++
	}
	day, _, ok = isoAtoi(s, pos, 2)
	if !ok {
		return 0, 0, 0, false
	}
	return year, month, day, true
}

var isoFractionCorrection = [5]int{100000, 10000, 1000, 100, 10}

// isoParseHMSF is _parse_hh_mm_ss_ff: HH[:?MM[:?SS[{.,}f+]]].
func isoParseHMSF(s string) (comps [4]int, ok bool) {
	n := len(s)
	pos := 0
	hasSep := false
	for comp := 0; comp < 3; comp++ {
		if n-pos < 2 {
			return comps, false
		}
		v, _, good := isoAtoi(s, pos, 2)
		if !good {
			return comps, false
		}
		comps[comp] = v
		pos += 2
		var next byte
		if pos < n {
			next = s[pos]
		}
		if comp == 0 {
			hasSep = next == ':'
		}
		if next == 0 || comp >= 2 {
			break
		}
		if hasSep && next != ':' {
			return comps, false
		}
		if hasSep {
			pos++
		}
	}
	if pos < n {
		if s[pos] != '.' && s[pos] != ',' {
			return comps, false
		}
		pos++
		remainder := n - pos
		toParse := remainder
		if toParse >= 6 {
			toParse = 6
		}
		v, _, good := isoAtoi(s, pos, toParse)
		if !good {
			return comps, false
		}
		comps[3] = v
		if toParse < 6 {
			comps[3] *= isoFractionCorrection[toParse-1]
		}
		for i := pos + toParse; i < n; i++ {
			if !isoASCIIDigit(s[i]) {
				return comps, false
			}
		}
	}
	return comps, true
}

// isoParseTime is _parse_isoformat_time. offset is in seconds east of UTC.
func isoParseTime(s string) (comps [4]int, offset int, aware bool, ok bool) {
	n := len(s)
	if n < 2 {
		return comps, 0, false, false
	}
	// Python: tz_pos = s.find('-')+1 or s.find('+')+1 or s.find('Z')+1 — the
	// FIRST '-' wins even when a '+' precedes it.
	tzPos := 0
	for _, c := range []byte{'-', '+', 'Z'} {
		if i := indexByte(s, c); i >= 0 {
			tzPos = i + 1
			break
		}
	}
	timeStr := s
	if tzPos > 0 {
		timeStr = s[:tzPos-1]
	}
	comps, ok = isoParseHMSF(timeStr)
	if !ok {
		return comps, 0, false, false
	}
	if tzPos == n && s[n-1] == 'Z' {
		return comps, 0, true, true
	}
	if tzPos > 0 {
		tzStr := s[tzPos:]
		switch len(tzStr) {
		case 0, 1, 3:
			return comps, 0, false, false
		}
		tz, good := isoParseHMSF(tzStr)
		if !good {
			return comps, 0, false, false
		}
		if tz == [4]int{} {
			return comps, 0, true, true
		}
		sign := 1
		if s[tzPos-1] == '-' {
			sign = -1
		}
		total := tz[0]*3600 + tz[1]*60 + tz[2]
		if tz[3] != 0 {
			// timezone() keeps sub-second offsets only in whole microseconds;
			// Go's FixedZone is second-resolution, so a fractional offset is
			// out of reach. No producer emits one.
			return comps, 0, false, false
		}
		if total >= 24*3600 {
			return comps, 0, false, false
		}
		return comps, sign * total, true, true
	}
	return comps, 0, false, true
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// isoWeekToGregorian is _isoweek_to_gregorian.
func isoWeekToGregorian(year, week, day int) (int, int, int, bool) {
	if year < 1 || year > 9999 {
		return 0, 0, 0, false
	}
	if week <= 0 || week >= 53 {
		outOfRange := true
		if week == 53 {
			firstWeekday := isoOrdinal(year, 1, 1) % 7
			if firstWeekday == 4 || (firstWeekday == 3 && isoIsLeap(year)) {
				outOfRange = false
			}
		}
		if outOfRange {
			return 0, 0, 0, false
		}
	}
	if day <= 0 || day >= 8 {
		return 0, 0, 0, false
	}
	ord := isoWeek1Monday(year) + (week-1)*7 + (day - 1)
	y, m, d, ok := isoFromOrdinal(ord)
	return y, m, d, ok
}

func isoIsLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

var isoDaysBeforeMonth = [13]int{0, 0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}

// isoOrdinal is _ymd2ord: days since 0001-01-01, that day being 1. Computed
// arithmetically because a time.Time subtraction of ~3.6M days overflows
// time.Duration (int64 nanoseconds tops out at ~292 years).
func isoOrdinal(y, m, d int) int {
	yy := y - 1
	daysBeforeYear := yy*365 + yy/4 - yy/100 + yy/400
	days := isoDaysBeforeMonth[m]
	if m > 2 && isoIsLeap(y) {
		days++
	}
	return daysBeforeYear + days + d
}

func isoFromOrdinal(ord int) (int, int, int, bool) {
	if ord < 1 {
		return 0, 0, 0, false
	}
	t := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, ord-1)
	if t.Year() < 1 || t.Year() > 9999 {
		return 0, 0, 0, false
	}
	return t.Year(), int(t.Month()), t.Day(), true
}

// isoWeek1Monday is _isoweek1monday (THURSDAY == 3 with Monday == 0).
func isoWeek1Monday(year int) int {
	const thursday = 3
	firstDay := isoOrdinal(year, 1, 1)
	firstWeekday := (firstDay + 6) % 7
	week1Monday := firstDay - firstWeekday
	if firstWeekday > thursday {
		week1Monday += 7
	}
	return week1Monday
}

// isoDaysInMonth mirrors the datetime constructor's day check.
func isoDaysInMonth(y, m int) int {
	switch m {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if isoIsLeap(y) {
			return 29
		}
		return 28
	}
	return 0
}

// PyFromISOFormat is datetime.fromisoformat. ok is false for every input
// CPython would reject with ValueError.
func pyFromISOFormat(text string) (isoResult, bool) {
	var out isoResult
	if len(text) < 7 {
		return out, false
	}
	sep, ok := isoFindSeparator(text)
	if !ok {
		return out, false
	}
	head := sep
	if head > len(text) {
		head = len(text)
	}
	dstr := text[:head]
	tstr := ""
	if sep+1 < len(text) {
		tstr = text[sep+1:]
	}
	year, month, day, ok := isoParseDate(dstr)
	if !ok {
		return out, false
	}
	comps := [4]int{}
	offset := 0
	aware := false
	if tstr != "" {
		comps, offset, aware, ok = isoParseTime(tstr)
		if !ok {
			return out, false
		}
	}
	if year < 1 || year > 9999 || month < 1 || month > 12 ||
		day < 1 || day > isoDaysInMonth(year, month) ||
		comps[0] > 23 || comps[1] > 59 || comps[2] > 59 || comps[3] > 999999 {
		return out, false
	}
	loc := time.UTC
	if aware && offset != 0 {
		loc = time.FixedZone("", offset)
	}
	out.t = time.Date(year, time.Month(month), day, comps[0], comps[1], comps[2], comps[3]*1000, loc)
	out.aware = aware
	return out, true
}
