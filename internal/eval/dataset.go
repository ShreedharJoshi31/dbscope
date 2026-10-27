// Package eval measures the NL pipeline on a dataset of questions with gold
// SQL (docs/NL.md "Evaluation"). Gold and predicted SQL run through the same
// guard and read-only executor; results are compared as sets (BIRD's
// Set-EX), multisets, and in order when the gold query has a top-level
// ORDER BY.
package eval

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/dbscope/dbscope/internal/store"
)

// Question is one dataset entry.
type Question struct {
	ID       string `yaml:"id" json:"id"`
	Category string `yaml:"category" json:"category"`
	Question string `yaml:"question" json:"question"`
	// Gold is the reference SQL; empty for ambiguous and unanswerable
	// questions, where the expected answer is a clarifying question.
	Gold string `yaml:"gold,omitempty" json:"gold,omitempty"`
	// Alternatives are other acceptable gold queries (e.g. tie handling).
	Alternatives []string `yaml:"alternatives,omitempty" json:"alternatives,omitempty"`
	Ambiguous    bool     `yaml:"ambiguous,omitempty" json:"ambiguous,omitempty"`
	Unanswerable bool     `yaml:"unanswerable,omitempty" json:"unanswerable,omitempty"`
	// Result is the recorded gold result, checked before each run.
	Result *Recorded `yaml:"result,omitempty" json:"result,omitempty"`
	Note   string    `yaml:"note,omitempty" json:"note,omitempty"`
}

// Recorded is a gold result fingerprint.
type Recorded struct {
	Rows     int    `yaml:"rows" json:"rows"`
	Checksum string `yaml:"checksum" json:"checksum"`
}

// ExpectsClarify reports whether the right answer is a clarifying question.
func (q Question) ExpectsClarify() bool { return q.Ambiguous || q.Unanswerable }

// Dataset is a loaded eval/nl/<name> directory.
type Dataset struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Schemas     []string   `yaml:"schemas"`
	Questions   []Question `yaml:"questions"`
	// Glossary comes from glossary.yaml next to questions.yaml, if present.
	Glossary []store.GlossaryEntry `yaml:"-"`
	Dir      string                `yaml:"-"`
}

type glossaryFile struct {
	Glossary []struct {
		Term       string `yaml:"term"`
		Definition string `yaml:"definition"`
		SQL        string `yaml:"sql"`
	} `yaml:"glossary"`
}

// Load reads dir/questions.yaml and dir/glossary.yaml (optional).
func Load(dir string) (*Dataset, error) {
	b, err := os.ReadFile(filepath.Join(dir, "questions.yaml"))
	if err != nil {
		return nil, err
	}
	var ds Dataset
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&ds); err != nil {
		return nil, fmt.Errorf("questions.yaml: %w", err)
	}
	ds.Dir = dir
	if err := ds.validate(); err != nil {
		return nil, fmt.Errorf("questions.yaml: %w", err)
	}
	gb, err := os.ReadFile(filepath.Join(dir, "glossary.yaml"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var gf glossaryFile
		dec := yaml.NewDecoder(strings.NewReader(string(gb)))
		dec.KnownFields(true)
		if err := dec.Decode(&gf); err != nil {
			return nil, fmt.Errorf("glossary.yaml: %w", err)
		}
		for _, g := range gf.Glossary {
			ds.Glossary = append(ds.Glossary, store.GlossaryEntry{Term: g.Term, Definition: strings.TrimSpace(g.Definition), SQL: strings.TrimSpace(g.SQL)})
		}
	}
	return &ds, nil
}

func (ds *Dataset) validate() error {
	seen := map[string]bool{}
	for i, q := range ds.Questions {
		switch {
		case q.ID == "":
			return fmt.Errorf("question %d has no id", i+1)
		case seen[q.ID]:
			return fmt.Errorf("duplicate id %s", q.ID)
		case strings.TrimSpace(q.Question) == "":
			return fmt.Errorf("%s has no question", q.ID)
		case q.ExpectsClarify() && q.Gold != "":
			return fmt.Errorf("%s expects a clarifying question but has gold SQL", q.ID)
		case !q.ExpectsClarify() && strings.TrimSpace(q.Gold) == "":
			return fmt.Errorf("%s has no gold SQL", q.ID)
		}
		seen[q.ID] = true
	}
	return nil
}
