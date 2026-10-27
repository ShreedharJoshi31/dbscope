package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/report/sarifschema"
	"github.com/dbscope/dbscope/internal/sample"
)

const migration = `SET lock_timeout = '2s';
ALTER TABLE shop.orders ADD COLUMN uid uuid DEFAULT gen_random_uuid();
-- é😀 comment so columns differ from bytes
ALTER TABLE shop.orders ALTER COLUMN status TYPE text;  CREATE INDEX shop_orders_note ON shop.orders (placed_at);
ALTER TABLE shop.customers RENAME COLUMN email TO email_address;
ALTER TABLE t ADD CONSTRAINT nn NOT NULL a NOT VALID;
`

func analyze(t *testing.T, files ...ingest.Input) *migrate.Report {
	t.Helper()
	m, err := sample.Model()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := migrate.Analyze(t.Context(), migrate.SampleBase(m), migrate.Request{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func render(t *testing.T, format string, rep *migrate.Report) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, format, rep, "0.4.0-test"); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSARIFValidatesAgainstSchema(t *testing.T) {
	rep := analyze(t, ingest.Input{Name: "db/migrations/V1__orders and more.sql", SQL: migration})
	out := render(t, "sarif", rep)
	if errs := sarifschema.Validate(out); len(errs) > 0 {
		t.Fatalf("SARIF does not validate:\n%s", strings.Join(errs, "\n"))
	}
	var doc struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Rules []struct{ ID string } `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID    string `json:"ruleId"`
				RuleIndex int    `json:"ruleIndex"`
				Level     string `json:"level"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct{ URI string } `json:"artifactLocation"`
						Region           struct{ StartLine, StartColumn int }
					} `json:"physicalLocation"`
				} `json:"locations"`
				PartialFingerprints map[string]string `json:"partialFingerprints"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	run := doc.Runs[0]
	levels := map[string]string{}
	for _, r := range run.Results {
		if run.Tool.Driver.Rules[r.RuleIndex].ID != r.RuleID {
			t.Errorf("ruleIndex %d is %s, result says %s", r.RuleIndex, run.Tool.Driver.Rules[r.RuleIndex].ID, r.RuleID)
		}
		loc := r.Locations[0].PhysicalLocation
		if loc.ArtifactLocation.URI != "db/migrations/V1__orders%20and%20more.sql" {
			t.Errorf("uri %q", loc.ArtifactLocation.URI)
		}
		levels[r.RuleID] = r.Level
		if r.RuleID == "DBS-INDEX-NOT-CONCURRENT" && (loc.Region.StartLine != 4 || loc.Region.StartColumn != 57) {
			t.Errorf("CREATE INDEX at %d:%d, want 4:57", loc.Region.StartLine, loc.Region.StartColumn)
		}
	}
	// Critical/High -> error, Medium -> warning, Low -> note.
	want := map[string]string{"DBS-ADD-COLUMN-REWRITE": "error", "DBS-RENAME": "error", "DBS-PARSE": "error"}
	for id, lvl := range want {
		if levels[id] != lvl {
			t.Errorf("%s level %q, want %q (all: %v)", id, levels[id], lvl, levels)
		}
	}
}

func TestSchemaValidatorRejectsBadDocuments(t *testing.T) {
	for _, doc := range []string{
		`{"version":"2.0.0","runs":[]}`,
		`{"version":"2.1.0","runs":[{"tool":{"driver":{}}}]}`,
		`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x"}},"results":[{"message":{"text":"m"},"level":"fatal"}]}]}`,
		`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x"}},"results":[{"message":{"text":"m"},"locations":[{"physicalLocation":{"region":{"startLine":0}}}]}]}]}`,
	} {
		if errs := sarifschema.Validate([]byte(doc)); len(errs) == 0 {
			t.Errorf("accepted %s", doc)
		}
	}
	if errs := sarifschema.Validate([]byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x"}},"results":[]}]}`)); len(errs) > 0 {
		t.Errorf("minimal document rejected: %v", errs)
	}
}

// Fingerprints must not change when lines move.
func TestFingerprintsStable(t *testing.T) {
	fp := func(sql string) []string {
		var doc struct {
			Runs []struct {
				Results []struct {
					PartialFingerprints map[string]string `json:"partialFingerprints"`
				} `json:"results"`
			} `json:"runs"`
		}
		json.Unmarshal(render(t, "sarif", analyze(t, ingest.Input{Name: "m.sql", SQL: sql})), &doc)
		var out []string
		for _, r := range doc.Runs[0].Results {
			out = append(out, r.PartialFingerprints["dbscopeFinding/v1"])
		}
		return out
	}
	a := fp(migration)
	b := fp("\n\n-- moved down\n" + migration)
	if len(a) == 0 || strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("fingerprints changed:\n%v\n%v", a, b)
	}
	if again := fp(migration); strings.Join(a, ",") != strings.Join(again, ",") {
		t.Fatal("fingerprints differ between runs")
	}
}

func TestGitHubAnnotations(t *testing.T) {
	rep := analyze(t, ingest.Input{Name: "db/m,1.sql", SQL: "ALTER TABLE shop.orders ADD COLUMN uid uuid DEFAULT gen_random_uuid();"})
	out := string(render(t, "github", rep))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("%q", out)
	}
	if !strings.HasPrefix(lines[0], "::error file=db/m%2C1.sql,line=1,endLine=1,col=1,title=DBS-ADD-COLUMN-REWRITE Adding this column rewrites the table::") {
		t.Fatalf("%q", lines[0])
	}
	if strings.Count(lines[0], "\n") != 0 || !strings.Contains(lines[0], "%0ASafe alternative:") {
		t.Fatalf("message not escaped: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "::error ") || !strings.Contains(lines[1], "DBS-NO-LOCK-TIMEOUT") {
		t.Fatalf("%q", lines[1])
	}
}

func TestTextFormat(t *testing.T) {
	out := string(render(t, "text", analyze(t, ingest.Input{Name: "db/V1__a.sql", SQL: migration})))
	for _, want := range []string{"db/V1__a.sql\n", "  2:1", "HIGH", "DBS-ADD-COLUMN-REWRITE", "safe: Add the column without the default",
		"worst rating CRITICAL", "Schema changes:", "renamed email -> email_address", "Dependents of changed objects:", "Not analyzed:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestJSONIsTheReport(t *testing.T) {
	rep := analyze(t, ingest.Input{Name: "a.sql", SQL: migration})
	var back migrate.Report
	if err := json.Unmarshal(render(t, "json", rep), &back); err != nil {
		t.Fatal(err)
	}
	if back.Rating != rep.Rating || len(back.Statements) != len(rep.Statements) {
		t.Fatalf("round trip %v %d", back.Rating, len(back.Statements))
	}
}
