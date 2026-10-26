package exec

import (
	"encoding/json"
	"strconv"
)

// Type OIDs mapped to JSON values other than strings.
const (
	oidBool   = 16
	oidInt8   = 20
	oidInt2   = 21
	oidInt4   = 23
	oidOID    = 26
	oidJSON   = 114
	oidFloat4 = 700
	oidFloat8 = 701
	oidJSONB  = 3802
)

// maxSafeInt is the largest integer a JavaScript number holds exactly.
const maxSafeInt = 1<<53 - 1

// Value maps one text-format result value to JSON, following the table in
// docs/QUERY-SAFETY.md. A nil value is SQL NULL.
func Value(oid uint32, text []byte) json.RawMessage {
	if text == nil {
		return json.RawMessage("null")
	}
	s := string(text)
	switch oid {
	case oidInt2, oidInt4, oidOID:
		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			return json.RawMessage(s)
		}
	case oidInt8:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n <= maxSafeInt && n >= -maxSafeInt {
			return json.RawMessage(s)
		}
	case oidFloat4, oidFloat8:
		// NaN and Infinity stay strings; finite values are already valid JSON numbers.
		if _, err := strconv.ParseFloat(s, 64); err == nil && json.Valid(text) {
			return json.RawMessage(s)
		}
	case oidBool:
		switch s {
		case "t":
			return json.RawMessage("true")
		case "f":
			return json.RawMessage("false")
		}
	case oidJSON, oidJSONB:
		if json.Valid(text) {
			return json.RawMessage(s)
		}
	}
	// numeric and everything else: the text value.
	b, _ := json.Marshal(s)
	return b
}
