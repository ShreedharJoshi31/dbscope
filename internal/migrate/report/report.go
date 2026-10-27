// Package report renders a migrate.Report as JSON (the API shape), aligned
// text, SARIF 2.1.0 or GitHub Actions workflow annotations.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/rules"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// Formats are the accepted --format values.
var Formats = []string{"text", "json", "sarif", "github"}

// Write renders rep in format.
func Write(w io.Writer, format string, rep *migrate.Report, version string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case "sarif":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(SARIF(rep, version))
	case "github":
		return GitHub(w, rep)
	case "text", "":
		return Text(w, rep)
	}
	return fmt.Errorf("unknown format %q", format)
}

// Level maps a rating to a SARIF level (docs/MIGRATIONS.md).
func Level(r score.Rating) string {
	switch r {
	case score.Critical, score.High:
		return "error"
	case score.Medium:
		return "warning"
	}
	return "note"
}

// ---- text ----

// Text writes statements grouped by file, then the diff, impact and warnings.
func Text(w io.Writer, rep *migrate.Report) error {
	ew := &errWriter{w: w}
	counts := map[score.Rating]int{}
	file := ""
	tw := tabwriter.NewWriter(ew, 0, 0, 2, ' ', 0)
	for _, s := range rep.Statements {
		counts[s.Rating]++
		if s.File != file {
			if file != "" {
				fmt.Fprintln(tw)
			}
			file = s.File
			tw.Flush()
			fmt.Fprintln(ew, file)
		}
		fmt.Fprintf(tw, "  %d:%d\t%s\t%d\t%s\t%s\n", s.Line, s.Column, strings.ToUpper(string(s.Rating)), s.Score, s.Kind, details(s))
		for _, f := range s.Findings {
			fmt.Fprintf(tw, "\t\t\t%s\t%s [%s]\n", f.RuleID, f.Message, f.Rating)
			if f.SafeAlternative != "" {
				fmt.Fprintf(tw, "\t\t\t\tsafe: %s\n", f.SafeAlternative)
			}
		}
	}
	tw.Flush()
	fmt.Fprintf(ew, "\n%d statements against PostgreSQL %s (%s base): worst rating %s (%d critical, %d high, %d medium, %d low)\n",
		len(rep.Statements), version(rep.PGVersion), rep.Base, strings.ToUpper(string(rep.Rating)),
		counts[score.Critical], counts[score.High], counts[score.Medium], counts[score.Low])
	d := rep.Diff
	if len(d.Added)+len(d.Dropped)+len(d.Altered) > 0 {
		fmt.Fprintln(ew, "\nSchema changes:")
		for _, k := range d.Added {
			fmt.Fprintf(ew, "  + %s\n", k)
		}
		for _, k := range d.Dropped {
			fmt.Fprintf(ew, "  - %s\n", k)
		}
		for _, k := range d.Altered {
			fmt.Fprintf(ew, "  ~ %s\n", k)
			for _, c := range d.Columns {
				if c.Relation != k {
					continue
				}
				if c.To != "" {
					fmt.Fprintf(ew, "      %s %s -> %s\n", c.Change, c.Column, c.To)
				} else {
					fmt.Fprintf(ew, "      %s %s\n", c.Change, c.Column)
				}
			}
		}
	}
	if len(rep.Impact) > 0 {
		fmt.Fprintln(ew, "\nDependents of changed objects:")
		keys := make([]string, 0, len(rep.Impact))
		for k := range rep.Impact {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(ew, "  %s\n", k)
			for _, it := range rep.Impact[k] {
				fmt.Fprintf(ew, "      %-16s %s (%s)\n", it.Kind, it.Name, it.Certainty)
			}
		}
	}
	if len(rep.Unanalyzable) > 0 {
		fmt.Fprintln(ew, "\nNot analyzed:")
		for _, u := range rep.Unanalyzable {
			fmt.Fprintf(ew, "  %s:%d: %s\n", u.File, u.Line, u.Message)
		}
	}
	if len(rep.Warnings) > 0 {
		fmt.Fprintln(ew, "\nWarnings:")
		for _, wm := range rep.Warnings {
			fmt.Fprintf(ew, "  %s\n", wm)
		}
	}
	return ew.err
}

func details(s migrate.Statement) string {
	var parts []string
	for _, l := range s.Locks {
		parts = append(parts, l.Mode+" "+l.Relation)
	}
	if len(parts) == 0 {
		parts = append(parts, "no table locks")
	}
	parts = append(parts, "work "+s.Work)
	if s.SizeBytes != nil {
		parts = append(parts, bytesText(*s.SizeBytes))
	}
	parts = append(parts, "tx "+s.Tx)
	if !s.LockTimeout && len(s.Locks) > 0 {
		parts = append(parts, "no lock_timeout")
	}
	return strings.Join(parts, "; ")
}

func bytesText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func version(v int) string {
	if v%10000 == 0 {
		return fmt.Sprint(v / 10000)
	}
	return fmt.Sprintf("%d.%d", v/10000, v%10000)
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

// ---- GitHub annotations ----

// GitHub writes one workflow command per finding:
// ::error file=...,line=...,endLine=...,col=...,title=...::message
func GitHub(w io.Writer, rep *migrate.Report) error {
	ew := &errWriter{w: w}
	for _, s := range rep.Statements {
		for _, f := range s.Findings {
			cmd := map[string]string{"error": "error", "warning": "warning", "note": "notice"}[Level(f.Rating)]
			msg := fmt.Sprintf("%s (%s, score %d): %s", s.Kind, f.Rating, s.Score, f.Message)
			if f.SafeAlternative != "" {
				msg += "\nSafe alternative: " + f.SafeAlternative
			}
			fmt.Fprintf(ew, "::%s file=%s,line=%d,endLine=%d,col=%d,title=%s::%s\n", cmd,
				escapeProperty(s.File), s.Line, s.EndLine, s.Column, escapeProperty(f.RuleID+" "+rules.Findings[f.RuleID].Title), escapeData(msg))
		}
	}
	return ew.err
}

func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

// ---- SARIF ----

// SARIF builds a SARIF 2.1.0 log: one rule per finding type (every rule in
// the catalog, so ruleIndex is stable), repo-relative URIs, UTF-16 columns
// and partialFingerprints that do not depend on line numbers.
func SARIF(rep *migrate.Report, toolVersion string) map[string]any {
	ids := rules.FindingIDs()
	index := map[string]int{}
	var ruleList []map[string]any
	for i, id := range ids {
		d := rules.Findings[id]
		index[id] = i
		help := d.Help
		if d.Credit != "" {
			help += " Related rules: " + d.Credit + "."
		}
		ruleList = append(ruleList, map[string]any{
			"id": id, "name": d.Name,
			"shortDescription":     map[string]any{"text": d.Title},
			"fullDescription":      map[string]any{"text": d.Help},
			"help":                 map[string]any{"text": help},
			"defaultConfiguration": map[string]any{"level": defaultLevel(id)},
			"properties":           map[string]any{"tags": []string{"postgres", "migration"}, "precision": "high"},
		})
	}
	results := []map[string]any{}
	seen := map[string]int{}
	for _, s := range rep.Statements {
		for _, f := range s.Findings {
			text := fmt.Sprintf("%s Rating: %s (score %d).", strings.TrimSuffix(f.Message, "."), f.Rating, s.Score)
			if f.SafeAlternative != "" {
				text += " Safe alternative: " + f.SafeAlternative
			}
			base := f.RuleID + "\x00" + s.File + "\x00" + strings.Join(strings.Fields(s.SQL), " ")
			seen[base]++
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", base, seen[base])))
			results = append(results, map[string]any{
				"ruleId": f.RuleID, "ruleIndex": index[f.RuleID], "level": Level(f.Rating),
				"message": map[string]any{"text": text},
				"locations": []map[string]any{{"physicalLocation": map[string]any{
					"artifactLocation": map[string]any{"uri": fileURI(s.File)},
					"region":           map[string]any{"startLine": s.Line, "startColumn": s.Column, "endLine": max(s.EndLine, s.Line)},
				}}},
				"partialFingerprints": map[string]any{"dbscopeFinding/v1": hex.EncodeToString(sum[:16])},
				"properties":          map[string]any{"rating": string(f.Rating), "score": s.Score, "statementKind": s.Kind},
			})
		}
	}
	driver := map[string]any{"name": "dbscope", "rules": ruleList}
	if toolVersion != "" {
		driver["version"] = toolVersion
	}
	return map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []map[string]any{{
			"tool":              map[string]any{"driver": driver},
			"automationDetails": map[string]any{"id": "dbscope/migrate-check/"},
			"columnKind":        "utf16CodeUnits",
			"results":           results,
			"properties":        map[string]any{"pgVersion": rep.PGVersion, "rating": string(rep.Rating), "base": rep.Base},
		}},
	}
}

func defaultLevel(id string) string {
	switch id {
	case "DBS-PARSE", "DBS-TX-NOT-ALLOWED", "DBS-WILL-FAIL", "DBS-PARTITIONED-UNSUPPORTED", "DBS-DROP-TABLE", "DBS-DROP-COLUMN", "DBS-TRUNCATE":
		return "error"
	case "DBS-SET-LOCAL-NO-TX", "DBS-ENUM-VALUE-TX":
		return "note"
	}
	return "warning"
}

// fileURI percent-encodes each path segment of a repo-relative path. A
// path outside the repository (absolute) becomes a file:// URI.
func fileURI(p string) string {
	abs := strings.HasPrefix(p, "/") || (len(p) > 2 && p[1] == ':' && p[2] == '/')
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segs {
		if i == 0 && abs && strings.HasSuffix(s, ":") {
			continue // Windows drive letter
		}
		segs[i] = url.PathEscape(s)
	}
	out := strings.Join(segs, "/")
	if abs {
		return "file:///" + out
	}
	return out
}
