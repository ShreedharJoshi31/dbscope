package rules

import "sort"

// FindingDef describes one finding type. It becomes a SARIF rule.
//
// Wording is dbscope's own. The checks and safe paths follow the published
// rule sets of squawk (github.com/sbdchd/squawk), strong_migrations
// (github.com/ankane/strong_migrations), Eugene (github.com/kaaveland/eugene),
// Atlas lint (atlasgo.io/lint/analyzers) and GitLab's migration style guide;
// Credit names the closest rule in each.
type FindingDef struct {
	ID     string
	Name   string // PascalCase, for SARIF rule.name
	Title  string
	Help   string
	Credit string
}

// Findings is the catalog, keyed by ID.
var Findings = map[string]FindingDef{
	"DBS-PARSE": {ID: "DBS-PARSE", Name: "Unparseable", Title: "Statement cannot be parsed with the PostgreSQL 17 grammar",
		Help: "dbscope uses libpg_query with the PostgreSQL 17 grammar. PostgreSQL 18-only syntax (VIRTUAL generated columns, " +
			"ADD CONSTRAINT ... NOT NULL ... NOT VALID, NOT ENFORCED) cannot be analyzed, so review the statement by hand. " +
			"Against PostgreSQL 14-17 a statement this grammar rejects is a syntax error the server rejects too."},
	"DBS-TX-NOT-ALLOWED": {ID: "DBS-TX-NOT-ALLOWED", Name: "NotAllowedInTransaction", Title: "Statement cannot run inside a transaction block",
		Help: "PostgreSQL rejects it with SQLSTATE 25001 inside BEGIN/COMMIT and inside a multi-statement simple query. " +
			"Run it alone: Flyway executeInTransaction=false, a separate golang-migrate file, or `-- atlas:txmode none`.",
		Credit: "squawk ban-concurrent-index-creation-in-transaction; Atlas PG103; strong_migrations disable_ddl_transaction!"},
	"DBS-WILL-FAIL": {ID: "DBS-WILL-FAIL", Name: "WillFail", Title: "PostgreSQL will reject this statement",
		Help:   "The statement cannot succeed against the base schema (for example NOT NULL without a default on a table with rows, or dropping an object others depend on without CASCADE).",
		Credit: "Atlas MF103"},
	"DBS-ADD-COLUMN-REWRITE": {ID: "DBS-ADD-COLUMN-REWRITE", Name: "AddColumnRewrite", Title: "Adding this column rewrites the table",
		Help:   "A volatile default, identity, serial, stored generated column or constrained domain makes PostgreSQL rewrite the table and its indexes under ACCESS EXCLUSIVE.",
		Credit: "squawk adding-field-with-default; strong_migrations volatile default; Eugene E11"},
	"DBS-TYPE-REWRITE": {ID: "DBS-TYPE-REWRITE", Name: "TypeChangeRewrite", Title: "Column type change rewrites the table",
		Help:   "The new type is not binary coercible, so every row is rewritten and every index rebuilt under ACCESS EXCLUSIVE.",
		Credit: "Eugene E5; strong_migrations change column type; Atlas PG301"},
	"DBS-TYPE-NO-REWRITE": {ID: "DBS-TYPE-NO-REWRITE", Name: "TypeChangeNoRewrite", Title: "Column type change locks the table but does not rewrite it",
		Help: "The change is binary coercible, so there is no rewrite, but it still takes ACCESS EXCLUSIVE. squawk flags it for the lock; " +
			"strong_migrations lists it as safe because there is no rewrite. Both are true; dbscope shows the lock and the work separately.",
		Credit: "squawk prefer-text-field / changing-column-type; strong_migrations safe type changes"},
	"DBS-TYPE-NARROWING": {ID: "DBS-TYPE-NARROWING", Name: "TypeNarrowing", Title: "Column type change can reject existing or future values",
		Help: "A shorter length, smaller integer or lower precision is a breaking change for writers."},
	"DBS-SET-NOT-NULL": {ID: "DBS-SET-NOT-NULL", Name: "SetNotNullScan", Title: "SET NOT NULL scans the table under ACCESS EXCLUSIVE",
		Help:   "PostgreSQL skips the scan when a valid CHECK (col IS NOT NULL) already proves it (PostgreSQL 12+).",
		Credit: "squawk adding-not-nullable-field; Eugene E2; strong_migrations change_column_null"},
	"DBS-NEW-NOT-NULL": {ID: "DBS-NEW-NOT-NULL", Name: "NewNotNull", Title: "New NOT NULL on an existing column",
		Help: "Writers that still insert NULL will fail after this change; deploy the application change first."},
	"DBS-VALIDATE-IN-ADD": {ID: "DBS-VALIDATE-IN-ADD", Name: "ConstraintValidatedOnAdd", Title: "Constraint is validated while the ADD holds its lock",
		Help:   "Adding a CHECK or FOREIGN KEY without NOT VALID scans the table while holding the ADD's lock (ACCESS EXCLUSIVE for CHECK, SHARE ROW EXCLUSIVE on both tables for FOREIGN KEY).",
		Credit: "Eugene E1; squawk constraint-missing-not-valid / adding-foreign-key-constraint; Atlas PG305/PG306"},
	"DBS-UNIQUE-CONSTRAINT": {ID: "DBS-UNIQUE-CONSTRAINT", Name: "UniqueConstraintIndexBuild", Title: "ADD UNIQUE / PRIMARY KEY builds an index under ACCESS EXCLUSIVE",
		Help:   "The index build blocks reads and writes. Eugene E7 describes the lock as blocking writes; PostgreSQL takes ACCESS EXCLUSIVE, which blocks reads too.",
		Credit: "squawk disallowed-unique-constraint; strong_migrations add unique constraint; Eugene E7; Atlas PG104"},
	"DBS-EXCLUSION": {ID: "DBS-EXCLUSION", Name: "ExclusionConstraint", Title: "Exclusion constraint builds an index under ACCESS EXCLUSIVE",
		Help: "Exclusion constraints cannot be added NOT VALID or USING INDEX, so there is no non-blocking path.", Credit: "Eugene E8; Atlas PG108"},
	"DBS-INDEX-NOT-CONCURRENT": {ID: "DBS-INDEX-NOT-CONCURRENT", Name: "IndexNotConcurrent", Title: "CREATE INDEX blocks writes while it builds",
		Help:   "Plain CREATE INDEX holds SHARE on the table, which blocks INSERT, UPDATE and DELETE for the whole build.",
		Credit: "squawk require-concurrent-index-creation; Eugene E6; Atlas PG101; strong_migrations add_index"},
	"DBS-DROP-INDEX-NOT-CONCURRENT": {ID: "DBS-DROP-INDEX-NOT-CONCURRENT", Name: "DropIndexNotConcurrent", Title: "DROP INDEX takes ACCESS EXCLUSIVE on the table",
		Help: "The drop itself is quick, but the lock request queues all traffic on the table while it waits.", Credit: "squawk require-concurrent-index-deletion; Atlas PG102"},
	"DBS-RENAME": {ID: "DBS-RENAME", Name: "Rename", Title: "Rename breaks clients that use the old name",
		Help: "Running application code, views written by hand and saved queries still use the old name.", Credit: "squawk renaming-column / renaming-table; strong_migrations rename; Atlas BC101/BC102"},
	"DBS-DROP-COLUMN": {ID: "DBS-DROP-COLUMN", Name: "DropColumn", Title: "Dropping a column loses its data",
		Help: "The drop is catalog-only (the space is reclaimed later), but the data is gone and clients that select it break.", Credit: "strong_migrations remove column; squawk ban-drop-column; Atlas DS103"},
	"DBS-DROP-TABLE": {ID: "DBS-DROP-TABLE", Name: "DropRelation", Title: "Dropping a table or view loses it",
		Help: "Clients and dependent objects that use it break; a table's data is gone.", Credit: "squawk ban-drop-table; Atlas DS102"},
	"DBS-TRUNCATE": {ID: "DBS-TRUNCATE", Name: "Truncate", Title: "TRUNCATE deletes every row",
		Help: "It takes ACCESS EXCLUSIVE and is not MVCC-safe; CASCADE also empties referencing tables.", Credit: "squawk ban-truncate-cascade"},
	"DBS-REWRITE": {ID: "DBS-REWRITE", Name: "Rewrite", Title: "Statement rewrites the table under ACCESS EXCLUSIVE",
		Help: "VACUUM FULL, CLUSTER, SET LOGGED/UNLOGGED, SET ACCESS METHOD and SET TABLESPACE copy the whole table while blocking reads and writes.", Credit: "squawk prefer-repack; Atlas PG307"},
	"DBS-REINDEX-NOT-CONCURRENT": {ID: "DBS-REINDEX-NOT-CONCURRENT", Name: "ReindexNotConcurrent", Title: "REINDEX blocks writes and locks the index exclusively",
		Help: "REINDEX holds SHARE on the table and ACCESS EXCLUSIVE on each index, so queries that would plan with the index wait too.", Credit: "squawk require-concurrent-reindex"},
	"DBS-REFRESH-MATVIEW": {ID: "DBS-REFRESH-MATVIEW", Name: "RefreshMatviewBlocksReads", Title: "REFRESH MATERIALIZED VIEW blocks reads of the view",
		Help: "Plain REFRESH takes ACCESS EXCLUSIVE on the materialized view for the whole recompute."},
	"DBS-DETACH-NOT-CONCURRENT": {ID: "DBS-DETACH-NOT-CONCURRENT", Name: "DetachNotConcurrent", Title: "DETACH PARTITION takes ACCESS EXCLUSIVE on the partitioned table",
		Help: "Every query on the partitioned table waits for the detach.", Credit: "squawk require-concurrent-partition-detach"},
	"DBS-ATTACH-SCAN": {ID: "DBS-ATTACH-SCAN", Name: "AttachPartitionScan", Title: "ATTACH PARTITION scans the partition to check its bound",
		Help: "The scan runs under ACCESS EXCLUSIVE on the attached table (and on the default partition, which is scanned too)."},
	"DBS-ENUM-VALUE-TX": {ID: "DBS-ENUM-VALUE-TX", Name: "EnumValueInTransaction", Title: "New enum value cannot be used until the transaction commits",
		Help: "ALTER TYPE ... ADD VALUE may run in a transaction (PostgreSQL 12+), but statements later in the same transaction cannot use the new value."},
	"DBS-NO-LOCK-TIMEOUT": {ID: "DBS-NO-LOCK-TIMEOUT", Name: "NoLockTimeout", Title: "Lock is requested without a lock_timeout",
		Help:   "While the request waits behind a long-running query, every later query on the table queues behind it. A lock_timeout bounds the outage; retry the migration on timeout.",
		Credit: "Eugene E9; squawk require-lock-timeout; GitLab with_lock_retries"},
	"DBS-SET-LOCAL-NO-TX": {ID: "DBS-SET-LOCAL-NO-TX", Name: "SetLocalOutsideTransaction", Title: "SET LOCAL outside a transaction has no effect",
		Help: "PostgreSQL only emits a WARNING; the setting does not apply to the following statements."},
	"DBS-AE-HELD": {ID: "DBS-AE-HELD", Name: "RunsWhileHoldingAccessExclusive", Title: "Statement runs while ACCESS EXCLUSIVE is already held",
		Help: "An earlier statement in the same transaction holds ACCESS EXCLUSIVE until COMMIT, so this statement's run time adds to the outage.", Credit: "Eugene E4"},
	"DBS-LOCK-TABLE": {ID: "DBS-LOCK-TABLE", Name: "ExplicitLock", Title: "Explicit LOCK TABLE",
		Help: "The lock is held until the end of the transaction."},
	"DBS-BACKFILL": {ID: "DBS-BACKFILL", Name: "UnbatchedBackfill", Title: "UPDATE or DELETE touches every row in one statement",
		Help: "One statement over a large table holds row locks and generates WAL and bloat for its whole run; batch it outside the schema migration."},
	"DBS-PARTITIONED-UNSUPPORTED": {ID: "DBS-PARTITIONED-UNSUPPORTED", Name: "UnsupportedOnPartitionedTable", Title: "Not supported on a partitioned table in this PostgreSQL version",
		Help: "CREATE INDEX CONCURRENTLY is not supported on partitioned tables (build it on each partition, then on the parent ON ONLY and ATTACH)."},
}

// FindingIDs returns the catalog IDs sorted.
func FindingIDs() []string {
	ids := make([]string, 0, len(Findings))
	for id := range Findings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
