package mysql

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

func ddlBase(flavor string, num int) *Base {
	return &Base{
		Server: Server{Flavor: flavor, Version: "x", Num: num},
		Schema: "app",
		Source: "live",
		Tables: map[string]*TableInfo{
			"app.orders": {Rows: 5_000_000, Bytes: 2 << 30, Sized: true, RowFormat: "dynamic", PrimaryKey: true, ForeignKeys: 1,
				Columns: map[string]ColumnInfo{
					"total": {Type: "decimal(10,2)", BytesPerChar: 1},
					"note":  {Type: "varchar(50)", Nullable: true, BytesPerChar: 4},
				}},
			"app.tiny": {Rows: 10, Bytes: 16384, Sized: true, RowFormat: "dynamic", PrimaryKey: true, Columns: map[string]ColumnInfo{}},
		},
	}
}

func ddlFindings(r *migrate.Report) map[string][]string {
	out := map[string][]string{}
	for _, s := range r.Statements {
		for _, f := range s.Findings {
			k := s.File + ":" + strconv.Itoa(s.Line)
			out[k] = append(out[k], f.RuleID+"/"+string(f.Rating))
		}
	}
	return out
}

func TestDDLAnalyze(t *testing.T) {
	files := []ingest.Input{{Name: "1.sql", SQL: `ALTER TABLE orders MODIFY total DECIMAL(12,2) NOT NULL;
ALTER TABLE orders ADD COLUMN flag INT, ALGORITHM=INSTANT;
SET SESSION lock_wait_timeout = 5;
ALTER TABLE orders ADD INDEX by_total (total);
CREATE INDEX t ON tiny (id) ALGORITHM=NOCOPY;
ALTER TABLE orders MODIFY note VARCHAR(70), ALGORITHM=INPLACE;
ALTER TABLE orders ADD FULLTEXT INDEX ft (note), LOCK=NONE;
SET foreign_key_checks = 0;
ALTER TABLE tiny ADD CONSTRAINT fk FOREIGN KEY (id) REFERENCES orders (id);
ALTER TABLE orders DROP PARTITION p0;
ALTER TABLE ` + "`app`.`orders`" + ` ADD COLUMN z INT FIRST, ALGORITHM=INPLACE;`}}
	rep := Analyze(ddlBase("mysql", 80413), files)
	got := ddlFindings(rep)
	want := map[string][]string{
		// a type change copies 5M rows and blocks writes; no lock_wait_timeout yet
		"1.sql:1": {"DBS-MYSQL-BLOCKS-WRITES/critical", "DBS-MYSQL-LOCK-WAIT/medium"},
		"1.sql:2": {"DBS-MYSQL-ROW-VERSIONS/low", "DBS-MYSQL-LOCK-WAIT/medium"},
		"1.sql:4": {"DBS-MYSQL-TABLE-WORK/high"},
		"1.sql:5": {"DBS-MYSQL-ALGORITHM/high"},
		// 50 to 70 characters of utf8mb4 crosses 255 bytes: COPY, so ALGORITHM=INPLACE fails
		"1.sql:6":  {"DBS-MYSQL-ALGORITHM/high"},
		"1.sql:7":  {"DBS-MYSQL-LOCK-NONE/high"},
		"1.sql:9":  {"DBS-MYSQL-FK-UNCHECKED/medium"},
		"1.sql:10": {"DBS-MYSQL-PARTITION-DATA/high"},
		// MySQL does exactly what ALGORITHM asks: INPLACE for an instant ADD COLUMN rebuilds
		"1.sql:11": {"DBS-MYSQL-ROW-VERSIONS/low", "DBS-MYSQL-TABLE-WORK/high"},
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected %s: %v", k, got[k])
		}
	}
	s := rep.Statements[0]
	if s.Work != "rewrite" || s.Locks[0].Mode != "SHARED (BLOCKS WRITES)" || s.Locks[0].Relation != "app.orders" || *s.SizeBytes != 2<<30 {
		t.Errorf("type change %+v", s)
	}
	if rep.Engine != "mysql" || rep.Rating != "critical" || len(rep.Diff.Columns) != 4 {
		t.Errorf("report %s %s %+v", rep.Engine, rep.Rating, rep.Diff.Columns)
	}
}

func TestDDLAnalyzeMariaDB(t *testing.T) {
	files := []ingest.Input{{Name: "1.sql", SQL: "ALTER TABLE orders WAIT 5 MODIFY total DECIMAL(12,2) NOT NULL;\nALTER TABLE orders ADD INDEX i (total), ALGORITHM=NOCOPY;"}}
	got := ddlFindings(Analyze(ddlBase("mariadb", 110809), files))
	// MariaDB 11.2+ copies with writes allowed, so a type change is table work, not a write block.
	if !slices.Equal(got["1.sql:1"], []string{"DBS-MYSQL-TABLE-WORK/high"}) {
		t.Errorf("type change: %v", got["1.sql:1"])
	}
	if !slices.Equal(got["1.sql:2"], []string{"DBS-MYSQL-TABLE-WORK/high", "DBS-MYSQL-LOCK-WAIT/medium"}) {
		t.Errorf("nocopy index: %v", got["1.sql:2"])
	}
}

func TestDDLAnalyzeNoBase(t *testing.T) {
	base := &Base{Server: Server{Flavor: "mysql", Version: "8.4.0", Num: 80400}, Schema: "app", Tables: map[string]*TableInfo{}, Source: "none"}
	rep := Analyze(base, []ingest.Input{{Name: "1.sql", SQL: "ALTER TABLE x MODIFY c VARCHAR(10);"}})
	got := ddlFindings(rep)["1.sql:1"]
	if !slices.Equal(got, []string{"DBS-MYSQL-BLOCKS-WRITES/high", "DBS-MYSQL-LOCK-WAIT/medium"}) {
		t.Errorf("%v", got)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "no base schema") {
		t.Errorf("warnings %v", rep.Warnings)
	}
}
