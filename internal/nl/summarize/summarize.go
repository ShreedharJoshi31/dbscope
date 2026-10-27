// Package summarize builds what a result summary sends to the model
// (docs/NL.md "Summary modes"): a statistical profile of the columns, or
// the first rows.
package summarize

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dbscope/dbscope/internal/query/exec"
)

// Type OIDs by category.
var (
	numericOIDs = map[uint32]bool{20: true, 21: true, 23: true, 26: true, 700: true, 701: true, 1700: true}
	temporalOID = map[uint32]bool{1082: true, 1083: true, 1114: true, 1184: true, 1266: true}
	textOIDs    = map[uint32]bool{18: true, 19: true, 25: true, 1042: true, 1043: true}
)

// ColumnProfile describes one result column without its values: min, max
// and average only for numeric and date/time columns, and for text
// columns only the length range.
type ColumnProfile struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	NullPct  float64 `json:"nullPct"`
	Distinct int     `json:"distinct"`
	Min      any     `json:"min,omitempty"`
	Max      any     `json:"max,omitempty"`
	Avg      any     `json:"avg,omitempty"`
	MinLen   *int    `json:"minLength,omitempty"`
	MaxLen   *int    `json:"maxLength,omitempty"`
}

// Profile is the "profile" summary payload.
type Profile struct {
	RowCount  int             `json:"rowCount"`
	Truncated bool            `json:"truncated"`
	Columns   []ColumnProfile `json:"columns"`
}

// Category is how a column is profiled.
func Category(c exec.Column) string {
	switch {
	case numericOIDs[c.TypeOID]:
		return "numeric"
	case temporalOID[c.TypeOID]:
		return "temporal"
	case textOIDs[c.TypeOID] || c.Type == "citext":
		return "text"
	}
	return "other"
}

// Compute profiles the rows the executor returned.
func Compute(res *exec.Result) Profile {
	p := Profile{RowCount: res.RowCount, Truncated: res.Truncated, Columns: make([]ColumnProfile, len(res.Columns))}
	for ci, col := range res.Columns {
		cp := ColumnProfile{Name: col.Name, Type: col.Type}
		cat := Category(col)
		distinct := map[string]bool{}
		nulls := 0
		var nums []float64
		var times []time.Time
		var minT, maxT string
		var minL, maxL = -1, -1
		for _, row := range res.Rows {
			if ci >= len(row) {
				continue
			}
			v := row[ci]
			if string(v) == "null" {
				nulls++
				continue
			}
			distinct[string(v)] = true
			switch cat {
			case "numeric":
				if f, ok := number(v); ok {
					nums = append(nums, f)
				}
			case "temporal":
				var s string
				if json.Unmarshal(v, &s) != nil {
					continue
				}
				if t, ok := parseTime(s); ok {
					times = append(times, t)
					if minT == "" || t.Before(mustTime(minT)) {
						minT = s
					}
					if maxT == "" || t.After(mustTime(maxT)) {
						maxT = s
					}
				}
			case "text":
				var s string
				if json.Unmarshal(v, &s) != nil {
					continue
				}
				n := utf8.RuneCountInString(s)
				if minL < 0 || n < minL {
					minL = n
				}
				if n > maxL {
					maxL = n
				}
			}
		}
		if len(res.Rows) > 0 {
			cp.NullPct = math.Round(float64(nulls)/float64(len(res.Rows))*1000) / 10
		}
		cp.Distinct = len(distinct)
		switch {
		case len(nums) > 0:
			cp.Min, cp.Max = slices.Min(nums), slices.Max(nums)
			sum := 0.0
			for _, f := range nums {
				sum += f
			}
			cp.Avg = round(sum / float64(len(nums)))
		case len(times) > 0:
			cp.Min, cp.Max = minT, maxT
			var sum float64
			for _, t := range times {
				sum += float64(t.UnixMicro())
			}
			avg := time.UnixMicro(int64(sum / float64(len(times)))).UTC()
			cp.Avg = avg.Format(time.RFC3339)
		case minL >= 0:
			cp.MinLen, cp.MaxLen = &minL, &maxL
		}
		p.Columns[ci] = cp
	}
	return p
}

func round(f float64) float64 {
	if f == 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return f
	}
	mag := math.Pow(10, 6-math.Ceil(math.Log10(math.Abs(f))))
	return math.Round(f*mag) / mag
}

// number reads a JSON number or a numeric string; NaN and Infinity are skipped.
func number(v json.RawMessage) (float64, bool) {
	s := string(v)
	if strings.HasPrefix(s, `"`) {
		if json.Unmarshal(v, &s) != nil {
			return 0, false
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// Layouts of date/time values under DateStyle ISO, TimeZone UTC.
var layouts = []string{
	"2006-01-02", "2006-01-02 15:04:05.999999", "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05.999999-07:00",
	"15:04:05.999999", "15:04:05.999999-07", "15:04:05.999999-07:00",
}

// parseTime reads ISO date/time text; infinity and BC values are skipped.
func parseTime(s string) (time.Time, bool) {
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func mustTime(s string) time.Time {
	t, _ := parseTime(s)
	return t
}

// Rows is the "rows" summary payload: the first max rows.
type Rows struct {
	RowCount  int                 `json:"rowCount"`
	Truncated bool                `json:"truncated"`
	Columns   []string            `json:"columns"`
	Rows      [][]json.RawMessage `json:"rows"`
}

// FirstRows returns the first max rows of res.
func FirstRows(res *exec.Result, max int) Rows {
	r := Rows{RowCount: res.RowCount, Truncated: res.Truncated || len(res.Rows) > max, Columns: make([]string, len(res.Columns))}
	for i, c := range res.Columns {
		r.Columns[i] = c.Name
	}
	r.Rows = res.Rows[:min(max, len(res.Rows))]
	return r
}
