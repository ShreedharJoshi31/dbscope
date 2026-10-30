// Package estimate turns the bytes a migration statement reads and writes
// into a duration range, from throughput measured per engine and kind of
// work on test containers. The range is wide on purpose: production disks,
// CPUs, caches and concurrent load differ from the machine it was measured on.
package estimate

import "fmt"

// Rate is the throughput range for one kind of work, in MB/s (10^6 bytes)
// of the size the statement is rated by.
type Rate struct {
	LowMBps, HighMBps float64
	// FixedSeconds is time that does not grow with size (catalog work,
	// commit, starting a mutation); it is added to the high end only.
	FixedSeconds float64
}

// Range has migrate.Estimate's fields, so migrate.Estimate(r) converts it;
// migrate imports this package, so it cannot return that type itself.
type Range struct {
	LowSeconds  float64 `json:"lowSeconds"`
	HighSeconds float64 `json:"highSeconds"`
	Basis       string  `json:"basis"`
}

// fasterHeadroom widens the fast end of every rate: the measurements left as
// little as 1.4x room below the fastest observed time, and production
// servers (and CI machines) can be well over twice as fast as the laptop.
const fasterHeadroom = 2

// Bytes estimates work on n bytes; what names the work and its size source,
// e.g. "rewriting the table and its indexes".
func Bytes(n int64, r Rate, what string) Range {
	mb := float64(n) / 1e6
	high := r.HighMBps * fasterHeadroom
	return Range{
		LowSeconds:  mb / high,
		HighSeconds: mb/r.LowMBps + r.FixedSeconds,
		Basis: fmt.Sprintf("%s: from %s at %s-%s MB/s measured on a laptop-class machine; your server may be faster or slower",
			what, Size(n), num(r.LowMBps), num(high)),
	}
}

// Size writes a byte count in decimal units, matching the MB/s rates.
func Size(n int64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.1f TB", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d bytes", n)
}

func num(f float64) string {
	if f < 10 {
		return fmt.Sprintf("%.1f", f)
	}
	return fmt.Sprintf("%.0f", f)
}
