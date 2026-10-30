package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

// verifySetup is the base every case runs against, inside a transaction
// that is rolled back. t is about 6 MB, so a write to every row logs
// megabytes while a metadata change logs a few kilobytes.
var verifySetup = []string{
	"CREATE TABLE p (id int CONSTRAINT pk_p PRIMARY KEY, a int NULL)",
	"INSERT p (id) SELECT TOP 100 ROW_NUMBER() OVER (ORDER BY (SELECT 1)) FROM sys.all_objects",
	`CREATE TABLE t (id int NOT NULL CONSTRAINT pk_t PRIMARY KEY, a int NOT NULL CONSTRAINT ck_t_a CHECK (a >= 0),
		v varchar(50) NULL CONSTRAINT uq_t_v UNIQUE, n int NULL CONSTRAINT fk_t_p REFERENCES p (id),
		f char(200) NOT NULL CONSTRAINT df_t_f DEFAULT 'x', w varchar(50) NULL, c int NOT NULL, ix int NULL INDEX ix_t_ix)`,
	`INSERT t (id, a, v, n, w, c) SELECT TOP 20000 ROW_NUMBER() OVER (ORDER BY (SELECT 1)), 1,
		CAST(ROW_NUMBER() OVER (ORDER BY (SELECT 1)) AS varchar(50)), 1, 'w', 1 FROM sys.all_objects x CROSS JOIN sys.all_objects y`,
	"CREATE TABLE h (id int NOT NULL, a int NULL)",
	"INSERT h (id) SELECT TOP 2000 ROW_NUMBER() OVER (ORDER BY (SELECT 1)) FROM sys.all_objects",
	"CREATE TABLE e (id int NOT NULL, a int NULL)",
}

// verifyCase is a statement on table, run after pre in the same transaction.
// post, when set, must return 1 after it ran.
type verifyCase struct {
	table, sql string
	pre        []string
	post       string
	// catalogReads: the statement's logical reads are catalog lookups, not
	// the table's pages (sp_rename reads about 2,500 pages of catalog).
	catalogReads bool
}

var verifyCases = []verifyCase{
	{table: "t", sql: "ALTER TABLE t ADD x int NULL"},
	{table: "t", sql: "ALTER TABLE t ADD x int NOT NULL DEFAULT 0"},
	{table: "t", sql: "ALTER TABLE t ADD x datetime2 NOT NULL DEFAULT sysdatetime()"},
	{table: "t", sql: "ALTER TABLE t ADD x uniqueidentifier NOT NULL DEFAULT newid()"},
	{table: "t", sql: "ALTER TABLE t ADD x int NULL DEFAULT 0 WITH VALUES"},
	{table: "t", sql: "ALTER TABLE t ADD x varchar(max) NOT NULL DEFAULT 'a'"},
	{table: "t", sql: "ALTER TABLE t ADD x AS (a + 1)"},
	{table: "t", sql: "ALTER TABLE t ADD x AS (a + 1) PERSISTED"},
	{table: "t", sql: "ALTER TABLE t ADD x int NOT NULL"},
	{table: "e", sql: "ALTER TABLE e ADD x int NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ADD x rowversion"},
	{table: "t", sql: "ALTER TABLE t ADD x int IDENTITY"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN w varchar(100) NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN w varchar(30) NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN w varchar(max) NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN w nvarchar(50) NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN w varchar(50) NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN n int NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN c bigint NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN c smallint NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN c bigint NOT NULL WITH (ONLINE = ON)"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN c int NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN c int", post: "SELECT CAST(is_nullable AS int) FROM sys.columns WHERE object_id = OBJECT_ID('t') AND name = 'c'"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN c int NOT NULL", post: "SELECT 1 - CAST(is_nullable AS int) FROM sys.columns WHERE object_id = OBJECT_ID('t') AND name = 'c'"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN c decimal(12,2) NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN f char(250) NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN f char(100) NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN f varchar(200) NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN v varchar(100) NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN v varchar(50) NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN ix bigint NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN a bigint NOT NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN a int NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN n bigint NULL"},
	{table: "t", sql: "ALTER TABLE t ALTER COLUMN n bigint NULL", pre: []string{"ALTER TABLE t DROP CONSTRAINT fk_t_p"}},
	{table: "t", sql: "CREATE INDEX i ON t (a)"},
	{table: "t", sql: "CREATE INDEX i ON t (a) WITH (ONLINE = ON)"},
	{table: "t", sql: "CREATE UNIQUE INDEX i ON t (id, a)"},
	{table: "h", sql: "CREATE CLUSTERED INDEX i ON h (a)"},
	{table: "t", sql: "ALTER TABLE t ADD CONSTRAINT fk2 FOREIGN KEY (c) REFERENCES p (id)"},
	{table: "t", sql: "ALTER TABLE t WITH NOCHECK ADD CONSTRAINT fk2 FOREIGN KEY (c) REFERENCES p (id)",
		post: "SELECT CAST(is_not_trusted AS int) FROM sys.foreign_keys WHERE name = 'fk2'"},
	{table: "t", sql: "ALTER TABLE t ADD CONSTRAINT ck2 CHECK (c > 0)"},
	{table: "t", sql: "ALTER TABLE t WITH NOCHECK ADD CONSTRAINT ck2 CHECK (c > 0)",
		post: "SELECT CAST(is_not_trusted AS int) FROM sys.check_constraints WHERE name = 'ck2'"},
	{table: "t", sql: "ALTER TABLE t ADD CONSTRAINT uq2 UNIQUE (id, a)"},
	{table: "t", sql: "ALTER TABLE t ADD CONSTRAINT uq2 UNIQUE (id, a) WITH (ONLINE = ON)"},
	{table: "t", sql: "ALTER TABLE t DROP COLUMN w", post: "SELECT CASE WHEN COL_LENGTH('t', 'w') IS NULL THEN 1 ELSE 0 END"},
	{table: "t", sql: "ALTER TABLE t DROP COLUMN f"},
	{table: "t", sql: "ALTER TABLE t DROP COLUMN ix"},
	{table: "t", sql: "ALTER TABLE t DROP COLUMN a"},
	{table: "t", sql: "ALTER TABLE t DROP COLUMN n"},
	{table: "t", sql: "ALTER TABLE t DROP COLUMN f", pre: []string{"ALTER TABLE t DROP CONSTRAINT df_t_f"}},
	{table: "t", sql: "ALTER TABLE t DROP CONSTRAINT fk_t_p"},
	{table: "t", sql: "ALTER TABLE t DROP CONSTRAINT uq_t_v"},
	{table: "t", sql: "DROP INDEX uq_t_v ON t"},
	{table: "t", sql: "DROP INDEX ix_t_ix ON t"},
	{table: "t", sql: "ALTER TABLE t NOCHECK CONSTRAINT fk_t_p", post: "SELECT CAST(is_disabled AS int) FROM sys.foreign_keys WHERE name = 'fk_t_p'"},
	{table: "t", sql: "ALTER TABLE t CHECK CONSTRAINT fk_t_p", pre: []string{"ALTER TABLE t NOCHECK CONSTRAINT fk_t_p"},
		post: "SELECT CAST(is_not_trusted AS int) FROM sys.foreign_keys WHERE name = 'fk_t_p'"},
	{table: "t", sql: "ALTER TABLE t WITH CHECK CHECK CONSTRAINT fk_t_p", pre: []string{"ALTER TABLE t NOCHECK CONSTRAINT fk_t_p"},
		post: "SELECT 1 - CAST(is_not_trusted AS int) FROM sys.foreign_keys WHERE name = 'fk_t_p'"},
	{table: "t", sql: "TRUNCATE TABLE t"},
	{table: "p", sql: "TRUNCATE TABLE p"},
	{table: "p", sql: "DROP TABLE p"},
	{table: "p", sql: "DROP TABLE p", pre: []string{"ALTER TABLE t DROP CONSTRAINT fk_t_p"}},
	{table: "t", sql: "DROP TABLE t"},
	{table: "t", sql: "EXEC sp_rename 't.w', 'w2', 'COLUMN'", post: "SELECT CASE WHEN COL_LENGTH('t', 'w2') IS NULL THEN 0 ELSE 1 END", catalogReads: true},
	{table: "t", sql: "ALTER TABLE t REBUILD"},
	{table: "t", sql: "ALTER TABLE t REBUILD WITH (ONLINE = ON)"},
	{table: "t", sql: "ALTER INDEX ALL ON t REBUILD"},
}

// expressCases are the edition-dependent ones, for Express.
var expressCases = []string{
	"ALTER TABLE t ADD x int NULL",
	"ALTER TABLE t ADD x int NOT NULL DEFAULT 0",
	"ALTER TABLE t ADD x datetime2 NOT NULL DEFAULT sysdatetime()",
	"ALTER TABLE t ADD x int NULL DEFAULT 0 WITH VALUES",
	"ALTER TABLE t ALTER COLUMN c bigint NOT NULL WITH (ONLINE = ON)",
	"CREATE INDEX i ON t (a)",
	"CREATE INDEX i ON t (a) WITH (ONLINE = ON)",
	"ALTER TABLE t REBUILD WITH (ONLINE = ON)",
}

// TestDDLPredictions runs each case on real servers and compares what dbscope
// predicts with what the server did: whether the statement failed, wrote
// every row (log bytes, or a new heap or clustered index partition), built
// an index (a new partition), or read every row (the session's logical
// reads); and which lock it queued for behind an open writer (sys.dm_tran_locks).
func TestDDLPredictions(t *testing.T) {
	for _, v := range []string{"2025", "2022"} {
		t.Run(v, func(t *testing.T) {
			verifyServer(t, mytest.MSSQL(t, v), verifyCases)
		})
	}
	t.Run("2022-express", func(t *testing.T) {
		var cases []verifyCase
		for _, s := range expressCases {
			cases = append(cases, verifyCase{table: "t", sql: s})
		}
		verifyServer(t, mssqlExpress(t), cases)
	})
}

// mssqlExpress starts SQL Server 2022 Express, the free edition without online operations.
func mssqlExpress(t *testing.T) conn.Profile {
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_MSSQL") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: mytest.MSSQLImages["2022"], ImagePlatform: "linux/amd64", ExposedPorts: []string{"1433/tcp"},
			Env: map[string]string{"ACCEPT_EULA": "Y", "MSSQL_SA_PASSWORD": mytest.MSSQLPassword, "MSSQL_PID": "Express"},
			WaitingFor: wait.ForExec([]string{"/opt/mssql-tools18/bin/sqlcmd", "-C", "-S", "localhost", "-U", "sa", "-P", mytest.MSSQLPassword, "-Q", "SELECT 1"}).
				WithExitCodeMatcher(func(code int) bool { return code == 0 }).
				WithPollInterval(2 * time.Second).WithStartupTimeout(4 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no sql server express container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "1433/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EngineSQLServer, Host: host, Port: n, Database: "master", User: "sa",
		SSLMode: conn.SSLRequire, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

func verifyServer(t *testing.T, p conn.Profile, cases []verifyCase) {
	ctx := context.Background()
	admin, err := Open(p, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	mustExec(t, admin, "CREATE DATABASE app")
	p.Database = "app"
	db, err := Open(p, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range verifySetup {
		mustExec(t, db, q)
	}
	base, err := ReadBase(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkBase(t, base)
	pages := base.Tables["dbo.t"].Bytes / 8192
	t.Logf("%s, engine edition %d, t has %d pages", base.Server.Edition, base.EngineEdition, pages)

	for _, c := range cases {
		name := c.sql
		if len(c.pre) > 0 {
			name = strings.Join(c.pre, "; ") + "; " + name
		}
		t.Run(name, func(t *testing.T) {
			var script strings.Builder
			for _, q := range c.pre {
				script.WriteString(q + ";\n")
			}
			script.WriteString(c.sql)
			rep := Analyze(base, []ingest.Input{{Name: "1.sql", SQL: script.String()}})
			st := rep.Statements[len(rep.Statements)-1]
			want := predicted(st)
			obs := observe(t, db, c, base, pages)
			t.Logf("predicted %s; %s, queued for %q %s", want, obs.detail, obs.waited, obs.waitedOther)
			if obs.effect != want {
				t.Errorf("predicted %s, the server did %s (%s)", want, obs.effect, obs.detail)
			}
			if c.post != "" && !obs.post {
				t.Errorf("%s did not return 1", c.post)
			}
			if want == "fails" || len(c.pre) > 0 {
				return
			}
			lockWait := slices.ContainsFunc(st.Findings, func(f migrate.Finding) bool { return f.RuleID == "DBS-MSSQL-LOCK-WAIT" })
			if !lockWait || obs.waited == "" {
				t.Errorf("lock wait predicted %v, waited behind an open writer for %q", lockWait, obs.waited)
			}
			// Some statements queue for one mode and then hold a stronger one (DROP TABLE: X, then Sch-M).
			if len(st.Locks) == 0 || st.Locks[0].Relation != "dbo."+c.table || (st.Locks[0].Mode != obs.waited && !slices.Contains(obs.held, st.Locks[0].Mode)) {
				t.Errorf("predicted locks %+v, it queued for %s on %s and then held %v", st.Locks, obs.waited, c.table, obs.held)
			}
		})
	}

	t.Run("lock timeout", func(t *testing.T) {
		// SET LOCK_TIMEOUT makes DDL behind an open writer fail with 1222 instead of waiting.
		rep := Analyze(base, []ingest.Input{{Name: "1.sql", SQL: "SET LOCK_TIMEOUT 1000\nALTER TABLE t ADD x int NULL"}})
		if st := rep.Statements[1]; !st.LockTimeout || len(st.Findings) != 0 {
			t.Errorf("predicted %+v", st)
		}
		a, w := conns(t, db)
		mustConnExec(t, w, "BEGIN TRAN; UPDATE TOP (1) t SET a = a")
		defer w.ExecContext(ctx, "IF @@TRANCOUNT > 0 ROLLBACK")
		start := time.Now()
		_, err := a.ExecContext(ctx, "SET LOCK_TIMEOUT 1000; ALTER TABLE t ADD x int NULL")
		a.ExecContext(ctx, "SET LOCK_TIMEOUT -1")
		if err == nil || !strings.Contains(err.Error(), "1222") || time.Since(start) > 10*time.Second {
			t.Errorf("err %v after %s", err, time.Since(start))
		}
	})

	if base.Online() {
		t.Run("resumable rewrite", func(t *testing.T) {
			rep := Analyze(base, []ingest.Input{{Name: "1.sql", SQL: "CREATE INDEX i ON t (a)"}})
			rw := rep.Statements[0].Rewrite
			if rw == nil {
				t.Fatal("no rewrite")
			}
			a, _ := conns(t, db)
			if _, err := a.ExecContext(ctx, "BEGIN TRAN; "+rw.SQL); err == nil || !strings.Contains(err.Error(), "(574)") {
				t.Errorf("in a transaction: %v", err)
			}
			a.ExecContext(ctx, "IF @@TRANCOUNT > 0 ROLLBACK")
			if _, err := a.ExecContext(ctx, rw.SQL); err != nil {
				t.Errorf("rewrite %s: %v", rw.SQL, err)
			}
			mustConnExec(t, a, "DROP INDEX i ON t")
		})

		t.Run("blockers", func(t *testing.T) {
			_, w := conns(t, db)
			mustConnExec(t, w, "BEGIN TRAN; UPDATE TOP (1) t SET a = a")
			defer w.ExecContext(ctx, "IF @@TRANCOUNT > 0 ROLLBACK")
			rep := Analyze(base, []ingest.Input{{Name: "1.sql", SQL: "ALTER TABLE t ADD x int NULL"}})
			ReadBlockers(ctx, db, base, rep)
			if rep.BlockersAt == nil || len(rep.Blockers) != 1 {
				t.Fatalf("blockers %+v %v", rep.Blockers, rep.Warnings)
			}
			b := rep.Blockers[0]
			if b.Relation != "dbo.t" || b.Lock != "IX" || b.User != "sa" || b.State != "sleeping, lock held" || !strings.Contains(b.Query, "UPDATE TOP (1) t") {
				t.Errorf("blocker %+v", b)
			}
		})
	}
}

func checkBase(t *testing.T, base *DDLBase) {
	tb, pb := base.Tables["dbo.t"], base.Tables["dbo.p"]
	if tb == nil || pb == nil || base.Tables["dbo.h"] == nil {
		t.Fatalf("tables %v", base.Tables)
	}
	c := tb.Columns
	if !tb.Sized || tb.Rows != 20000 || tb.Bytes < 5<<20 || tb.ObjectID == 0 || pb.ReferencedBy != 1 || base.Schema != "dbo" ||
		!c["v"].Indexed || !c["ix"].Indexed || !c["a"].Checked || !c["f"].Default || !c["n"].ForeignKey || c["w"].Indexed || c["w"].Checked ||
		c["f"].Type != "char(200)" || !c["w"].Nullable || !tb.KeyIndexes["uq_t_v"] || !tb.KeyIndexes["pk_t"] || tb.KeyIndexes["ix_t_ix"] ||
		tb.ForeignKeyRefs["fk_t_p"] != "dbo.p" || !strings.Contains(tb.Constraints["uq_t_v"], "duplicate (v)") ||
		!strings.Contains(tb.Constraints["ck_t_a"], "[a]>=(0)") || tb.Constraints["ix_t_ix"] != "" {
		t.Errorf("base table t %+v", tb)
	}
}

func predicted(st migrate.Statement) string {
	if slices.ContainsFunc(st.Findings, func(f migrate.Finding) bool { return f.RuleID == "DBS-MSSQL-FAILS" }) {
		return "fails"
	}
	switch st.Work {
	case "rewrite":
		return "rewrite"
	case "index_build":
		return "index"
	case "scan":
		return "scan"
	}
	return "metadata"
}

type observation struct {
	effect, detail, waited string
	waitedOther            string   // a lock it queued for on something other than the table
	held                   []string // modes granted on the table once it ran
	post                   bool
}

func conns(t *testing.T, db *sql.DB) (a, b *sql.Conn) {
	ctx := context.Background()
	a, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err = db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func mustConnExec(t *testing.T, c *sql.Conn, q string) {
	t.Helper()
	if _, err := c.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// observe runs c in a transaction on session a while session w holds an
// open UPDATE on c.table, records the lock a queues for, then lets it run
// and measures what it did before rolling it back.
func observe(t *testing.T, db *sql.DB, c verifyCase, base *DDLBase, pages int64) observation {
	ctx := context.Background()
	a, w := conns(t, db)
	var spid int
	var obj int64
	if err := a.QueryRowContext(ctx, "SELECT @@SPID, OBJECT_ID(@p1)", c.table).Scan(&spid, &obj); err != nil {
		t.Fatal(err)
	}
	parts := func() string {
		var s sql.NullString
		a.QueryRowContext(ctx, `SELECT STRING_AGG(CAST(index_id AS varchar(5)) + ':' + CAST(partition_id AS varchar(30)), ',') WITHIN GROUP (ORDER BY index_id)
			FROM sys.partitions WHERE object_id = @p1`, obj).Scan(&s)
		return s.String
	}
	reads := func() int64 {
		var n int64
		db.QueryRowContext(ctx, "SELECT logical_reads FROM sys.dm_exec_sessions WHERE session_id = @p1", spid).Scan(&n)
		return n
	}
	before := parts()
	mustConnExec(t, a, "BEGIN TRAN")
	defer a.ExecContext(ctx, "IF @@TRANCOUNT > 0 ROLLBACK")
	for _, q := range c.pre {
		mustConnExec(t, a, q)
	}
	r0 := reads()

	// After pre's DDL a writer could not get in, so only cases without pre check the queued lock.
	if len(c.pre) == 0 {
		mustConnExec(t, w, "SET LOCK_TIMEOUT 10000; BEGIN TRAN; UPDATE TOP (1) "+c.table+" SET a = a")
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.ExecContext(ctx, c.sql)
		done <- err
	}()
	var o observation
	var err error
	finished := false
	deadline := time.Now().Add(20 * time.Second)
poll:
	for time.Now().Before(deadline) {
		select {
		case err = <-done:
			finished = true
			break poll
		case <-time.After(300 * time.Millisecond):
		}
		// The request's wait, not sys.dm_tran_locks: SQL Server 2025 lists no WAIT row there for DROP TABLE.
		var waitType, resource string
		db.QueryRowContext(ctx, `SELECT coalesce(wait_type, ''), coalesce(wait_resource, '') FROM sys.dm_exec_requests WHERE session_id = @p1`, spid).
			Scan(&waitType, &resource)
		if mode, ok := strings.CutPrefix(waitType, "LCK_M_"); ok {
			if m := map[string]string{"SCH_M": "Sch-M", "SCH_S": "Sch-S"}[mode]; m != "" {
				mode = m
			}
			// wait_resource is "OBJECT: <db>:<object id>:<lock partition>".
			if f := strings.Split(strings.TrimPrefix(resource, "OBJECT: "), ":"); strings.HasPrefix(resource, "OBJECT: ") && len(f) >= 2 && f[1] == strconv.FormatInt(obj, 10) {
				o.waited = mode
			} else {
				o.waitedOther = mode + " on " + resource
			}
		}
		if o.waited != "" || o.waitedOther != "" {
			break
		}
	}
	w.ExecContext(ctx, "IF @@TRANCOUNT > 0 ROLLBACK")
	if !finished {
		err = <-done
	}
	if err != nil {
		o.effect, o.detail = "fails", strings.ReplaceAll(err.Error(), "\n", " | ")
		return o
	}
	r1 := reads()
	var logBytes int64
	a.QueryRowContext(ctx, `SELECT coalesce(sum(database_transaction_log_bytes_used), 0) FROM sys.dm_tran_database_transactions
		WHERE transaction_id = CURRENT_TRANSACTION_ID() AND database_id = DB_ID()`).Scan(&logBytes)
	after := parts()
	var held sql.NullString
	db.QueryRowContext(ctx, `SELECT STRING_AGG(request_mode, ',') FROM sys.dm_tran_locks
		WHERE request_session_id = @p1 AND request_status = 'GRANT' AND resource_type = 'OBJECT' AND resource_associated_entity_id = @p2`, spid, obj).Scan(&held)
	o.held = strings.Split(held.String, ",")
	if c.post != "" {
		var n int
		a.QueryRowContext(ctx, c.post).Scan(&n)
		o.post = n == 1
	}
	rowsPart := func(s string) string { // the heap's or clustered index's partitions
		var out []string
		for _, x := range strings.Split(s, ",") {
			if strings.HasPrefix(x, "0:") || strings.HasPrefix(x, "1:") {
				out = append(out, x)
			}
		}
		return strings.Join(out, ",")
	}
	readPages := r1 - r0
	o.detail = fmt.Sprintf("log %d bytes, %d logical reads, partitions %s -> %s, holds %v", logBytes, readPages, before, after, o.held)
	newIndex := len(strings.Split(after, ",")) > len(strings.Split(before, ",")) || strings.HasPrefix(before, "0:") && strings.HasPrefix(after, "1:")
	switch {
	case after != "" && (logBytes > 1<<20 || rowsPart(before) != rowsPart(after) && !strings.HasPrefix(before, "0:")):
		o.effect = "rewrite"
	case newIndex:
		o.effect = "index"
	case after != "" && !c.catalogReads && readPages >= pages/2:
		o.effect = "scan"
	default:
		o.effect = "metadata"
	}
	return o
}
