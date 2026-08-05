// Package sessionconv ports the ChatGPT session conversion / export layer of
// app.py — gap G23 in docs/UI_SPEC.md §5.3.
//
// It covers the seven output formats (sub2api / CPA / Cockpit / 9router /
// Codex / AxonHub / Codex-Manager) built by convert_chatgpt_session_record
// (app.py:5209-5424), the document wrapper build_session_conversion_document
// (app.py:5427-5446), the standalone build_sub2api_export (app.py:5017-5059),
// the raw account line account_export_line (app.py:1878-1898) and the ZIP
// entry-name de-duplication rule session_conversion_zip_entry_name
// (app.py:5174-5186).
//
// These documents are consumed byte-for-byte by other tools, so this package is
// written for exact shape fidelity with Python:
//
//   - key ORDER is preserved with OrderedMap, because Python dicts are ordered
//     while Go maps iterate randomly and encoding/json sorts object keys;
//   - encoding goes through an Encoder with SetEscapeHTML(false), because
//     json.dumps does not escape <, > or &, while json.Marshal does.
package sessionconv

import (
	"bytes"
	"encoding/json"
	"strings"
)

// OrderedMap is an insertion-ordered JSON object — the Go stand-in for a Python
// dict literal. Every builder in app.py:5298-5422 depends on literal key order
// for its output shape, so a map[string]any is not usable here.
type OrderedMap struct {
	keys []string
	vals map[string]any
}

// NewOrderedMap returns an empty ordered object.
func NewOrderedMap() *OrderedMap {
	return &OrderedMap{vals: map[string]any{}}
}

// Set stores value under key. Re-setting an existing key overwrites the value
// but keeps the original position, exactly like a Python dict assignment.
func (m *OrderedMap) Set(key string, value any) *OrderedMap {
	if m.vals == nil {
		m.vals = map[string]any{}
	}
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = value
	return m
}

// SetNotNil mirrors Python's `{k: v for k, v in {...}.items() if v is not None}`
// filter: the key is dropped when the value is nil, but kept for "" / 0 / false.
func (m *OrderedMap) SetNotNil(key string, value any) *OrderedMap {
	if value == nil {
		return m
	}
	return m.Set(key, value)
}

// SetTruthy mirrors Python's `{k: v for k, v in {...}.items() if v}` filter,
// used by codex_manager_meta (app.py:5406-5411) — empty strings are dropped.
func (m *OrderedMap) SetTruthy(key string, value any) *OrderedMap {
	if !pyTruthy(value) {
		return m
	}
	return m.Set(key, value)
}

// Get returns the value stored under key.
func (m *OrderedMap) Get(key string) (any, bool) {
	if m == nil || m.vals == nil {
		return nil, false
	}
	v, ok := m.vals[key]
	return v, ok
}

// Keys returns the keys in insertion order.
func (m *OrderedMap) Keys() []string {
	if m == nil {
		return nil
	}
	out := make([]string, len(m.keys))
	copy(out, m.keys)
	return out
}

// Len returns the number of keys.
func (m *OrderedMap) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// MarshalJSON writes the object in insertion order without HTML escaping.
//
// NOTE: encoding/json re-compacts the bytes returned here using the *outer*
// encoder's escapeHTML flag, so <, > and & only survive unescaped when the
// caller dumps through DumpJSON / DumpCompactJSON (or any Encoder with
// SetEscapeHTML(false)). A plain json.Marshal would re-escape them.
func (m *OrderedMap) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := encodeNoEscape(key)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := encodeNoEscape(m.vals[key])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func encodeNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// pyJSONEscapes rewrites the four string escapes where encoding/json and
// json.dumps disagree. SetEscapeHTML(false) covers <, > and & but there is no
// flag for these:
//
//	Go            Python (ensure_ascii=False)
//	        \b
//	        \f
//	         the literal U+2028
//	         the literal U+2029
//
// All four reach an export through a session note, an account label or a
// pasted session_json, and each one is a visible byte difference in the file.
//
// Escape pairs are walked rather than blind-replaced: in the encoder's output a
// literal backslash is written as `\\`, so ReplaceAll would turn the JSON
// string `\\u2028` — an escaped backslash followed by five ordinary characters
// — into a backslash plus a real line separator.
func pyJSONEscapes(text string) string {
	if !strings.Contains(text, `\u`) {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] != '\\' {
			out.WriteByte(text[i])
			i++
			continue
		}
		if i+6 <= len(text) && text[i+1] == 'u' {
			switch text[i+2 : i+6] {
			case "0008":
				out.WriteString(`\b`)
				i += 6
				continue
			case "000c":
				out.WriteString(`\f`)
				i += 6
				continue
			case "2028":
				out.WriteRune(' ')
				i += 6
				continue
			case "2029":
				out.WriteRune(' ')
				i += 6
				continue
			}
		}
		// Any other escape: copy the backslash and the character it escapes as a
		// unit, so a `\\` cannot be re-read as the start of an escape.
		out.WriteByte(text[i])
		if i+1 < len(text) {
			out.WriteByte(text[i+1])
		}
		i += 2
	}
	return out.String()
}

// DumpJSON is the Go equivalent of
// `json.dumps(value, ensure_ascii=False, indent=2) + "\n"` (app.py:24339,
// 24400, 24533). Encoder.Encode already appends the trailing newline.
func DumpJSON(value any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	return pyJSONEscapes(buf.String()), nil
}

// DumpCompactJSON is `json.dumps(value, ensure_ascii=False, separators=(",", ":"))`
// with no trailing newline — used by encode_base64url_json (app.py:5074-5076),
// whose result is base64-encoded, so an escape difference moves every byte of
// the synthetic id_token.
func DumpCompactJSON(value any) (string, error) {
	b, err := encodeNoEscape(value)
	if err != nil {
		return "", err
	}
	return pyJSONEscapes(string(b)), nil
}

// ParseSessionRecord decodes a session JSON blob into the map shape expected by
// ConvertChatGPTSessionRecord.
//
// It uses Decoder.UseNumber so JSON numbers arrive as json.Number rather than
// float64: Python's json.loads yields int for integral numbers and str(int) has
// no exponent, while Go's fmt of float64(1712345678) is "1.712345678e+09".
// Callers that build the record by hand should use json.Number (or int64) for
// numeric fields for the same reason.
func ParseSessionRecord(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}
