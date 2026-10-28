// Package migrate analyzes migration files against a base schema
// (docs/MIGRATIONS.md): ingest -> parse -> apply -> classify -> timeouts ->
// score -> impact. The result is the API's AnalyzeReport; package report
// renders it as text, SARIF or GitHub annotations.
package migrate

import (
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
