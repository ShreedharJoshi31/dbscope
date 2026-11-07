package migrate

import (
	"fmt"
	"time"
)

// Busy tables: at least this share of the counted activity, and at least
// minHotCount events, so a nearly idle database has no busy tables. Every
// engine uses the same cutoffs (docs/MIGRATIONS.md, "Scoring").
const (
	hotShare    = 0.05
	minHotCount = 1000
)

// BusyShare returns n's share of total and whether that makes a table busy.
func BusyShare(n, total int64) (float64, bool) {
	if total <= 0 || n < minHotCount {
		return 0, false
	}
	share := float64(n) / float64(total)
	return share, share >= hotShare
}

// Traffic is per-table activity an engine's analyzer weighs locks against,
// read from the counters its Workload view uses. Keys are the engine's
// table keys; units name what the engine counts.
type Traffic struct {
	Writes, Reads           map[string]int64
	TotalWrites, TotalReads int64
	WriteUnit, ReadUnit     string // e.g. "rows written", "rows read"
	// Of names the population the totals cover, e.g. "the schemas read".
	Of    string
	Since *time.Time
}

// Busy describes key's share of the traffic a lock stops: writes only, or
// reads and writes together. ok is false when the table is not busy.
func (t *Traffic) Busy(key, display string, stopsReads bool) (line string, ok bool) {
	if t == nil {
		return "", false
	}
	n, total, unit := t.Writes[key], t.TotalWrites, t.WriteUnit
	if stopsReads {
		n, total, unit = n+t.Reads[key], total+t.TotalReads, t.ReadUnit+" and "+t.WriteUnit
	}
	share, busy := BusyShare(n, total)
	if !busy {
		return "", false
	}
	since := ""
	if t.Since != nil {
		since = " since " + t.Since.UTC().Format("2006-01-02")
	}
	return fmt.Sprintf("%s has %d%% of the %s in %s (%d%s)", display, int(100*share+0.5), unit, t.Of, n, since), true
}
