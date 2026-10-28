package mysql

import (
	"errors"
	"testing"

	"github.com/dbscope/dbscope/internal/query/guard"
)

func TestCheck(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"select * from `t;x` where a = 'it''s; fine' and b = \"q\\\"; c\" -- trailing; comment",
		"WITH x AS (SELECT 1) SELECT * FROM x;",
		"SHOW TABLES",
		"EXPLAIN FORMAT=JSON SELECT 1",
		"SELECT /*+ MAX_EXECUTION_TIME(1000) */ 1 # hash comment; still one",
		"TABLE users",
		"SELECT `into`, 'for update' FROM t",
		"SELECT sleep_minutes FROM t",
	}
	for _, q := range ok {
		if err := Check(q); err != nil {
			t.Errorf("%q rejected: %v", q, err)
		}
	}
	bad := map[string]guard.Reason{
		"SELECT 1; SELECT 2":                    guard.MultipleStatements,
		"DELETE FROM t":                         guard.NotSelect,
		"SET @a = 1":                            guard.NotSelect,
		"SELECT * FROM t INTO OUTFILE '/tmp/x'": guard.SelectInto,
		"SELECT * FROM t /*!50000 INTO OUTFILE '/tmp/x' */": guard.SelectInto,
		"SELECT 1 /*M!100000 ; DROP TABLE t */":             guard.MultipleStatements,
		"SELECT a INTO @v FROM t":                           guard.SelectInto,
		"SELECT * FROM t FOR UPDATE":                        guard.LockingClause,
		"SELECT * FROM t LOCK IN SHARE MODE":                guard.LockingClause,
		"SELECT SLEEP(10)":                                  guard.FunctionNotAllowed,
		"SELECT load_file ('/etc/passwd')":                  guard.FunctionNotAllowed,
		"SELECT GET_LOCK('x', 1)":                           guard.FunctionNotAllowed,
		"SELECT @a := 1":                                    guard.WritesData,
		"/* SELECT */ UPDATE t SET a = 1":                   guard.NotSelect,
		"-- SELECT\nINSERT INTO t VALUES (1)":               guard.NotSelect,
		"SELECT 'a\\'; DROP TABLE t; --'":                   "",
	}
	for q, want := range bad {
		err := Check(q)
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v, want accepted (the quote is escaped)", q, err)
			}
			continue
		}
		var ge *guard.Error
		if !errors.As(err, &ge) || ge.Reason != want {
			t.Errorf("%q: %v, want %s", q, err, want)
		}
	}
}
