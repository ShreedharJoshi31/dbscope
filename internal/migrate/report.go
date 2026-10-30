// Package migrate analyzes migration files against a base schema
// (docs/MIGRATIONS.md): ingest -> parse -> apply -> classify -> timeouts ->
// score -> impact. The result is the API's AnalyzeReport; package report
// renders it as text, SARIF or GitHub annotations.
package migrate

import (
	"slices"
	"time"

	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// Rating re-exports score.Rating.
type Rating = score.Rating

// Report is the API's AnalyzeReport.
type Report struct {
	// Engine is empty for PostgreSQL; for SQLite PGVersion holds the SQLITE_VERSION_NUMBER checked with.
	Engine       string                   `json:"engine,omitempty"`
	PGVersion    int                      `json:"pgVersion"`
	Rating       Rating                   `json:"rating"`
	Statements   []Statement              `json:"statements"`
	Diff         apply.Diff               `json:"diff"`
	Impact       map[string][]impact.Item `json:"impact"`
	Unanalyzable []Unanalyzable           `json:"unanalyzable"`
	// Warnings are notes about the analysis itself (relations missing from
	// the base, skipped impact sources, colliding file names). Not in the
	// original contract; additive.
	Warnings []string `json:"warnings"`
	// Base says where the base schema came from: live, stats, dump or sample.
	Base string `json:"base"`
	// Blockers are sessions that, when the report was made, held or waited
	// for locks the migration needs; BlockersAt is when they were read.
	Blockers   []Blocker  `json:"blockers,omitempty"`
	BlockersAt *time.Time `json:"blockersAt,omitempty"`
	// Pending says which files the migration tool's history shows applied.
	Pending *Pending `json:"pending,omitempty"`
	// Usages are lines of the profile's project folder that name what the
	// migration drops or renames.
	Usages []Usage `json:"usages,omitempty"`
}

// Blocker is an open session holding or waiting for a lock on a relation the migration locks.
type Blocker struct {
	Relation string `json:"relation"`
	// Session is the server's id for it: a backend pid, a connection id or an operation id.
	Session    string  `json:"session"`
	User       string  `json:"user,omitempty"`
	State      string  `json:"state"`
	AgeSeconds float64 `json:"ageSeconds"` // how long its transaction or operation has been open
	Query      string  `json:"query,omitempty"`
	Lock       string  `json:"lock,omitempty"`
}

// Pending is what a migration tool's history table recorded as applied.
type Pending struct {
	Tool  string `json:"tool"`
	Table string `json:"table"`
	// Applied are files left out of the analysis because the history has them.
	Applied []string `json:"applied"`
	// Changed are applied files whose checksum no longer matches the history; they are analyzed again.
	Changed []string `json:"changed"`
}

// Usage is a line of application code naming a table or column the migration drops or renames.
type Usage struct {
	Name string `json:"name"`
	File string `json:"file"` // relative to the project folder
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Rewrite is a safer form of a statement that dbscope can write itself.
type Rewrite struct {
	SQL string `json:"sql"`
	// Note says what changed and what the new form needs, such as running outside a transaction.
	Note string `json:"note"`
}

// Estimate is how long a statement's work may take on this server.
type Estimate struct {
	LowSeconds  float64 `json:"lowSeconds"`
	HighSeconds float64 `json:"highSeconds"`
	// Basis says what the estimate rests on (sizes, measured rates) so it can be judged.
	Basis string `json:"basis"`
}

// Statement is one analyzed statement.
type Statement struct {
	File        string    `json:"file"`
	Line        int       `json:"line"`
	Column      int       `json:"column"`
	EndLine     int       `json:"endLine"`
	SQL         string    `json:"sql"`
	Kind        string    `json:"kind"`
	Tx          string    `json:"tx"`
	Locks       []Lock    `json:"locks"`
	Work        string    `json:"work"`
	SizeBytes   *int64    `json:"sizeBytes,omitempty"`
	LockTimeout bool      `json:"lockTimeout"`
	Score       int       `json:"score"`
	Rating      Rating    `json:"rating"`
	Findings    []Finding `json:"findings"`
	// Relations are the tables, collections, indices or labels the statement
	// acts on, for engines whose statements take no table lock to name them.
	Relations []string `json:"relations,omitempty"`
	// Rewrite is a safer form of the statement, when one can be written mechanically.
	Rewrite *Rewrite `json:"rewrite,omitempty"`
	// Estimate is how long its work may take, when the engine can tell.
	Estimate *Estimate `json:"estimate,omitempty"`
}

// Touch records a relation the statement acts on, once.
func (s *Statement) Touch(name string) {
	if name != "" && !slices.Contains(s.Relations, name) {
		s.Relations = append(s.Relations, name)
	}
}

// Lock is a lock on one relation ("schema.name", PostgreSQL lock mode name).
type Lock struct {
	Relation string `json:"relation"`
	Mode     string `json:"mode"`
}

// Finding is one problem found in a statement.
type Finding struct {
	RuleID          string `json:"ruleId"`
	Rating          Rating `json:"rating"`
	Message         string `json:"message"`
	SafeAlternative string `json:"safeAlternative,omitempty"`
}

// Unanalyzable is a statement dbscope could not analyze.
type Unanalyzable struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// Exceeds reports whether any statement's rating is at or above limit.
func (r *Report) Exceeds(limit Rating) bool {
	for _, s := range r.Statements {
		if s.Rating.Rank() >= limit.Rank() {
			return true
		}
	}
	return false
}
