package sessionconv

import (
	"fmt"
	"time"
)

// FormatOrder is SESSION_CONVERT_FORMATS' key order (app.py:5062-5070). Python
// dicts are ordered and the combobox is populated from this dict, so the order
// is part of the UI contract; a Go map alone would not preserve it.
var FormatOrder = []string{
	"sub2api",
	"cpa",
	"cockpit",
	"9router",
	"codex",
	"axonhub",
	"codexmanager",
}

// FormatLabels is SESSION_CONVERT_FORMATS (app.py:5062-5070): key -> UI label.
var FormatLabels = map[string]string{
	"sub2api":      "sub2api",
	"cpa":          "CPA",
	"cockpit":      "Cockpit",
	"9router":      "9router",
	"codex":        "Codex",
	"axonhub":      "AxonHub",
	"codexmanager": "Codex-Manager",
}

// DefaultFormat is what the UI falls back to for an unknown selection
// (app.py:24288-24290).
const DefaultFormat = "sub2api"

// NormalizeFormat mirrors `output_format = self.session_convert_format.get()
// .strip().lower(); if output_format not in SESSION_CONVERT_FORMATS:
// output_format = "sub2api"` (app.py:24287-24289).
func NormalizeFormat(value string) string {
	key := pyLower(pyStrip(value))
	if _, ok := FormatLabels[key]; !ok {
		return DefaultFormat
	}
	return key
}

// FormatLabel mirrors SESSION_CONVERT_FORMATS.get(fmt, "sub2api")
// (app.py:24290, 24350, 24362).
func FormatLabel(value string) string {
	if label, ok := FormatLabels[pyLower(pyStrip(value))]; ok {
		return label
	}
	return "sub2api"
}

// documentKeyMap is the key_map of build_session_conversion_document
// (app.py:5436-5443). "sub2api" is absent on purpose — it takes the early
// return that wraps the accounts in an exported_at/proxies envelope.
var documentKeyMap = map[string]string{
	"cpa":          "cpa",
	"cockpit":      "cockpit",
	"9router":      "nineRouter",
	"codex":        "codexAuthJson",
	"axonhub":      "axonHub",
	"codexmanager": "codexManager",
}

// member returns the Converted field the document key names.
func (c Converted) member(key string) any {
	switch key {
	case "cpa":
		return c.CPA
	case "cockpit":
		return c.Cockpit
	case "nineRouter":
		return c.NineRouter
	case "codexAuthJson":
		return c.CodexAuthJSON
	case "axonHub":
		return c.AxonHub
	case "codexManager":
		return c.CodexManager
	default:
		return c.Sub2APIAccount
	}
}

// BuildSessionConversionDocument ports build_session_conversion_document
// (app.py:5427-5446).
//
// sub2api gets an {exported_at, proxies, accounts} envelope; every other format
// returns the bare per-account payload — a single object when exactly ONE
// account was converted, otherwise a JSON array. Pass the zero time.Time for
// Python's `now or datetime.now(utc)`.
//
// An unrecognised (but non-empty) format falls through to the sub2apiAccount
// member WITHOUT the envelope, matching key_map.get(fmt, "sub2apiAccount").
func BuildSessionConversionDocument(converted []Converted, outputFormat string, now time.Time) any {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// Python: str(output_format or "sub2api").strip().lower() — note that an
	// EMPTY format becomes sub2api, but an unknown non-empty one does not.
	format := pyLower(pyStrip(outputFormat))
	if format == "" {
		format = DefaultFormat
	}
	if format == "sub2api" {
		accounts := []any{}
		for _, item := range converted {
			accounts = append(accounts, item.Sub2APIAccount)
		}
		// A time.Time can never take normalize_iso_timestamp's numeric branch,
		// so the OSError arm is unreachable here.
		exportedAt, _ := NormalizeISOTimestamp(now)
		return NewOrderedMap().
			Set("exported_at", exportedAt).
			Set("proxies", []any{}).
			Set("accounts", accounts)
	}
	key := documentKeyMap[format]
	values := []any{}
	for _, item := range converted {
		values = append(values, item.member(key))
	}
	if len(values) == 1 {
		return values[0]
	}
	return values
}

// SessionConversionZipEntryName ports session_conversion_zip_entry_name
// (app.py:5174-5186): "<format>-<email>.json", with "-2", "-3", … appended
// before the extension on collision.
//
// Pass a nil usedNames to skip de-duplication entirely (Python's
// `used_names is None` early return). A non-nil set is MUTATED, exactly like
// the Python `used_names.add(name)`.
func SessionConversionZipEntryName(emailAddr, outputFormat string, usedNames map[string]bool) string {
	raw := pyLower(pyStrip(outputFormat))
	safeFormat := reEdgeUnderscores.ReplaceAllString(reNonAlnumRun.ReplaceAllString(raw, "_"), "")
	if safeFormat == "" {
		safeFormat = "session"
	}
	safeEmail := EmailKey(emailAddr)
	if safeEmail == "" {
		safeEmail = "account"
	}
	baseName := safeFormat + "-" + safeEmail
	name := baseName + ".json"
	if usedNames == nil {
		return name
	}
	for counter := 2; usedNames[name]; counter++ {
		name = fmt.Sprintf("%s-%d.json", baseName, counter)
	}
	usedNames[name] = true
	return name
}
