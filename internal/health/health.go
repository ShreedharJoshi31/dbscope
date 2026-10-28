// Package health reads a server's health signals as a list of checks, one
// shape for every engine (docs/HEALTH.md): each check says what it measured,
// whether that is fine, worth watching or urgent, and what to do about it.
package health

import (
	"fmt"
	"time"
)

// Status is how a check came out.
type Status string

const (
	OK     Status = "ok"
	Warn   Status = "warn"
	Danger Status = "danger"
	// Unknown means the check could not run, usually for want of a privilege; Detail says which.
	Unknown Status = "unknown"
)

// Check is one health signal.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	// Value is the measurement in words: "12 of 100", "3 h 4 min".
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// Report is the health of one connection's server.
type Report struct {
	Engine string  `json:"engine"`
	Server string  `json:"server"`
	Checks []Check `json:"checks"`
	TookMs int64   `json:"tookMs"`
}

// Add appends a check.
func (r *Report) Add(c Check) { r.Checks = append(r.Checks, c) }

// Unavailable records a check that could not run.
func (r *Report) Unavailable(id, title, why string) {
	r.Add(Check{ID: id, Title: title, Status: Unknown, Value: "not readable", Detail: why})
}

// Level picks a status from a value and its warn and danger thresholds (higher is worse).
func Level(v, warn, danger float64) Status {
	switch {
	case v >= danger:
		return Danger
	case v >= warn:
		return Warn
	}
	return OK
}

// Duration spells a duration for a check value: "45 s", "12 min", "3 h 4 min", "2 d 5 h".
func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d d %d h", int(d.Hours())/24, int(d.Hours())%24)
}

// Bytes spells a size: "512 B", "3.2 MiB", "1.5 GiB".
func Bytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
