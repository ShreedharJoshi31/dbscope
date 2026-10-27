package rules

import (
	"fmt"
	"strings"
)

// Rule is one row of the data table: what PostgreSQL does for one statement
// subtype under one condition, for a range of major versions.
//
// Sources: the ALTER TABLE, CREATE/DROP INDEX, REINDEX, CLUSTER, VACUUM,
// TRUNCATE and explicit-locking pages of the PostgreSQL 14, 17 and 18 docs
// (docs/research-migrations.md section 1), corrected by measurement
// (internal/migrate/groundtruth/testdata/groundtruth-pg*.json).
type Rule struct {
	Key  string
	Kind string // statement label shown in reports
	// MinMajor and MaxMajor bound the PostgreSQL major versions (inclusive, 0 = open).
	MinMajor, MaxMajor int
	Locks              []Lock
	Work               Work
	// NewStorage marks statements that give the table a new relfilenode
	// without touching rows (TRUNCATE).
	NewStorage bool
	// InTx is false for statements PostgreSQL refuses inside a transaction block.
	InTx bool
	// Fails is set when PostgreSQL rejects the statement in these versions.
	Fails string
	// FailsWithRows: the statement fails when the target table has rows.
	FailsWithRows string
	// Finding is the finding ID reported for this row ("" for none);
	// Message and Safe are its text, with {table}, {column}, {ref}, {index},
	// {partition} and {detail} placeholders.
	Finding, Message, Safe string
	// Grammar18 marks PostgreSQL 18-only syntax the 17 grammar cannot parse;
	// the row documents measured behaviour but the classifier cannot reach it.
	Grammar18 bool
}

func target(m Mode) []Lock { return []Lock{{Target, m}} }

const (
	safeConcurrentIndex = "CREATE INDEX CONCURRENTLY outside a transaction block"
	safeNotValid        = "ADD the constraint ... NOT VALID (brief lock, no scan), then ALTER TABLE {table} VALIDATE CONSTRAINT in a separate transaction (SHARE UPDATE EXCLUSIVE)"
	safeAddColumn       = "Add the column without the default (or nullable), then SET DEFAULT for new rows and backfill existing rows in batches"
	safeTypeChange      = "Add a new column of the new type, write to both, backfill in batches, switch reads, then drop the old column"
	safeSetNotNull      = "ADD CONSTRAINT {column}_not_null CHECK ({column} IS NOT NULL) NOT VALID; VALIDATE CONSTRAINT {column}_not_null; then SET NOT NULL (no scan) and drop the CHECK"
	safeUnique          = "CREATE UNIQUE INDEX CONCURRENTLY, then ALTER TABLE {table} ADD CONSTRAINT ... UNIQUE (or PRIMARY KEY) USING INDEX"
	safeRename          = "Expand and contract: add the new name (a new column kept in sync, or a view with the old name), move clients, then remove the old one"
	safeDrop            = "Stop reading and writing it in the application first, deploy, then drop it in a later migration; keep a backup"
	safeRepack          = "Use pg_repack or pg_squeeze, which rebuild the table online"
	attachMessage       = "ATTACH PARTITION {partition} scans it under ACCESS EXCLUSIVE to check the partition bound"
	attachSafe          = "Before attaching, add a CHECK matching the bound to {partition} (NOT VALID, then VALIDATE); ATTACH then skips the scan; drop the CHECK afterwards"
)

// Table is the rule data. Lookup picks the first row whose key matches and
// whose version range contains the server's major version.
var Table = []Rule{
	// ---- indexes ----
	{Key: "create_index", Kind: "CREATE INDEX", Locks: target(Share), Work: IndexBuild, InTx: true,
		Finding: "DBS-INDEX-NOT-CONCURRENT", Message: "CREATE INDEX on {table} holds SHARE (blocks writes) for the whole build", Safe: safeConcurrentIndex},
	{Key: "create_index.partitioned", Kind: "CREATE INDEX", Locks: []Lock{{Target, Share}, {Partitions, Share}}, Work: IndexBuild, InTx: true,
		Finding: "DBS-INDEX-NOT-CONCURRENT", Message: "CREATE INDEX on partitioned {table} holds SHARE on it and every partition for the whole build",
		Safe: "CREATE INDEX CONCURRENTLY on each partition, CREATE INDEX ... ON ONLY {table}, then ALTER INDEX ... ATTACH PARTITION for each"},
	{Key: "create_index.concurrently", Kind: "CREATE INDEX CONCURRENTLY", Locks: target(ShareUpdateExclusive), Work: IndexBuild},
	{Key: "create_index.concurrently.partitioned", Kind: "CREATE INDEX CONCURRENTLY", Locks: target(ShareUpdateExclusive), Work: IndexBuild,
		Fails: "CREATE INDEX CONCURRENTLY is not supported on partitioned tables", Finding: "DBS-PARTITIONED-UNSUPPORTED",
		Message: "{table} is partitioned; PostgreSQL rejects CREATE INDEX CONCURRENTLY on it",
		Safe:    "CREATE INDEX CONCURRENTLY on each partition, CREATE INDEX ... ON ONLY {table}, then ALTER INDEX ... ATTACH PARTITION for each"},
	{Key: "drop_index", Kind: "DROP INDEX", Locks: []Lock{{Target, AccessExclusive}, {Index, AccessExclusive}}, InTx: true,
		Finding: "DBS-DROP-INDEX-NOT-CONCURRENT", Message: "DROP INDEX {index} takes ACCESS EXCLUSIVE on {table}", Safe: "DROP INDEX CONCURRENTLY {index}"},
	{Key: "drop_index.concurrently", Kind: "DROP INDEX CONCURRENTLY", Locks: []Lock{{Target, ShareUpdateExclusive}, {Index, ShareUpdateExclusive}}},
	{Key: "reindex", Kind: "REINDEX", Locks: []Lock{{Target, Share}, {Indexes, AccessExclusive}}, Work: IndexBuild, InTx: true,
		Finding: "DBS-REINDEX-NOT-CONCURRENT", Message: "REINDEX holds SHARE on {table} and ACCESS EXCLUSIVE on {detail}", Safe: "REINDEX ... CONCURRENTLY"},
	{Key: "reindex.concurrently", Kind: "REINDEX CONCURRENTLY", Locks: []Lock{{Target, ShareUpdateExclusive}, {Indexes, ShareUpdateExclusive}}, Work: IndexBuild},
	{Key: "reindex.schema", Kind: "REINDEX SCHEMA/DATABASE", Work: IndexBuild},
	{Key: "rename_index", Kind: "ALTER INDEX RENAME", Locks: []Lock{{Index, ShareUpdateExclusive}}, InTx: true},

	// ---- ADD COLUMN ----
	{Key: "add_column", Kind: "ALTER TABLE ADD COLUMN", Locks: target(AccessExclusive), InTx: true},
	{Key: "add_column.default", Kind: "ALTER TABLE ADD COLUMN", Locks: target(AccessExclusive), InTx: true},
	{Key: "add_column.not_null_no_default", Kind: "ALTER TABLE ADD COLUMN", Locks: target(AccessExclusive), Work: Scan, InTx: true,
		FailsWithRows: "a NOT NULL column without a default fails on a table that has rows (23502)", Finding: "DBS-WILL-FAIL",
		Message: "ADD COLUMN {column} NOT NULL without a DEFAULT fails because {table} has rows",
		Safe:    "Add the column with a constant DEFAULT (no rewrite since PostgreSQL 11), or add it nullable, backfill, then add NOT NULL"},
	{Key: "add_column.volatile_default", Kind: "ALTER TABLE ADD COLUMN", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-ADD-COLUMN-REWRITE", Message: "ADD COLUMN {column} with a volatile default ({detail}) rewrites {table} and its indexes", Safe: safeAddColumn},
	{Key: "add_column.serial", Kind: "ALTER TABLE ADD COLUMN", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-ADD-COLUMN-REWRITE", Message: "ADD COLUMN {column} {detail} fills every row from a sequence (nextval() is volatile), rewriting {table}",
		Safe: "Add a nullable column, attach a sequence default for new rows, and backfill existing rows in batches"},
	{Key: "add_column.identity", Kind: "ALTER TABLE ADD COLUMN", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-ADD-COLUMN-REWRITE", Message: "ADD COLUMN {column} GENERATED AS IDENTITY rewrites {table}",
		Safe: "Add a nullable bigint column, backfill in batches, then ALTER COLUMN ... ADD GENERATED AS IDENTITY and SET NOT NULL via a validated CHECK"},
	{Key: "add_column.generated_stored", Kind: "ALTER TABLE ADD COLUMN", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-ADD-COLUMN-REWRITE", Message: "ADD COLUMN {column} GENERATED ... STORED computes every row, rewriting {table}",
		Safe: "Add a plain column filled by a trigger and backfilled in batches; on PostgreSQL 18 a VIRTUAL generated column needs no rewrite"},
	{Key: "add_column.generated_virtual", Kind: "ALTER TABLE ADD COLUMN", MinMajor: 18, Locks: target(AccessExclusive), InTx: true, Grammar18: true},
	{Key: "add_column.domain", Kind: "ALTER TABLE ADD COLUMN", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-ADD-COLUMN-REWRITE", Message: "ADD COLUMN {column} of constrained domain {detail} checks every row, rewriting {table}", Safe: safeAddColumn},

	// ---- other column changes ----
	{Key: "drop_column", Kind: "ALTER TABLE DROP COLUMN", Locks: target(AccessExclusive), InTx: true,
		Finding: "DBS-DROP-COLUMN", Message: "DROP COLUMN {column} removes the column and its data from {table}", Safe: safeDrop},
	{Key: "alter_type.no_rewrite", Kind: "ALTER TABLE ALTER COLUMN TYPE", Locks: target(AccessExclusive), InTx: true,
		Finding: "DBS-TYPE-NO-REWRITE", Message: "ALTER COLUMN {column} TYPE takes ACCESS EXCLUSIVE on {table} but does not rewrite it: {detail}",
		Safe: "Keep it, with a short lock_timeout so the brief lock cannot queue traffic"},
	{Key: "alter_type.rewrite", Kind: "ALTER TABLE ALTER COLUMN TYPE", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-TYPE-REWRITE", Message: "ALTER COLUMN {column} TYPE rewrites {table} and rebuilds its indexes: {detail}", Safe: safeTypeChange},
	{Key: "set_default", Kind: "ALTER TABLE ALTER COLUMN SET DEFAULT", Locks: target(AccessExclusive), InTx: true},
	{Key: "drop_default", Kind: "ALTER TABLE ALTER COLUMN DROP DEFAULT", Locks: target(AccessExclusive), InTx: true},
	{Key: "set_not_null", Kind: "ALTER TABLE ALTER COLUMN SET NOT NULL", Locks: target(AccessExclusive), Work: Scan, InTx: true,
		Finding: "DBS-SET-NOT-NULL", Message: "SET NOT NULL on {table}.{column} scans the whole table under ACCESS EXCLUSIVE", Safe: safeSetNotNull},
	{Key: "set_not_null.proven", Kind: "ALTER TABLE ALTER COLUMN SET NOT NULL", Locks: target(AccessExclusive), InTx: true},
	{Key: "drop_not_null", Kind: "ALTER TABLE ALTER COLUMN DROP NOT NULL", Locks: target(AccessExclusive), InTx: true},
	{Key: "add_identity", Kind: "ALTER TABLE ALTER COLUMN ADD IDENTITY", Locks: target(AccessExclusive), InTx: true},
	{Key: "drop_expression", Kind: "ALTER TABLE ALTER COLUMN DROP EXPRESSION", Locks: target(AccessExclusive), InTx: true},
	{Key: "set_statistics", Kind: "ALTER TABLE ALTER COLUMN SET STATISTICS", Locks: target(ShareUpdateExclusive), InTx: true},

	// ---- constraints ----
	// On a partitioned table the foreign key is added to every partition too (measured).
	{Key: "add_fk", Kind: "ALTER TABLE ADD FOREIGN KEY", Locks: []Lock{{Target, ShareRowExclusive}, {Referenced, ShareRowExclusive}, {Partitions, ShareRowExclusive}}, Work: Scan, InTx: true,
		Finding: "DBS-VALIDATE-IN-ADD", Message: "ADD FOREIGN KEY validates every row of {table} while holding SHARE ROW EXCLUSIVE on {table} and {ref}", Safe: safeNotValid},
	{Key: "add_fk.not_valid", Kind: "ALTER TABLE ADD FOREIGN KEY NOT VALID", Locks: []Lock{{Target, ShareRowExclusive}, {Referenced, ShareRowExclusive}}, InTx: true},
	{Key: "add_fk.not_valid.partitioned", Kind: "ALTER TABLE ADD FOREIGN KEY NOT VALID", MaxMajor: 17,
		Locks: []Lock{{Target, ShareRowExclusive}, {Referenced, ShareRowExclusive}}, InTx: true,
		Fails: "PostgreSQL before 18 cannot add a NOT VALID foreign key on a partitioned table", Finding: "DBS-PARTITIONED-UNSUPPORTED",
		Message: "{table} is partitioned; PostgreSQL {detail} rejects ADD FOREIGN KEY ... NOT VALID on it",
		Safe:    "Add the NOT VALID foreign key on each partition and VALIDATE them, or add it validated in a quiet window"},
	{Key: "add_fk.not_valid.partitioned", Kind: "ALTER TABLE ADD FOREIGN KEY NOT VALID", MinMajor: 18,
		Locks: []Lock{{Target, ShareRowExclusive}, {Referenced, ShareRowExclusive}, {Partitions, ShareRowExclusive}}, InTx: true},
	{Key: "add_check", Kind: "ALTER TABLE ADD CHECK", Locks: target(AccessExclusive), Work: Scan, InTx: true,
		Finding: "DBS-VALIDATE-IN-ADD", Message: "ADD CHECK validates every row of {table} under ACCESS EXCLUSIVE", Safe: safeNotValid},
	{Key: "add_check.not_valid", Kind: "ALTER TABLE ADD CHECK NOT VALID", Locks: target(AccessExclusive), InTx: true},
	{Key: "add_check.not_enforced", Kind: "ALTER TABLE ADD CHECK NOT ENFORCED", MinMajor: 18, Locks: target(AccessExclusive), InTx: true, Grammar18: true},
	{Key: "add_not_null.not_valid", Kind: "ALTER TABLE ADD CONSTRAINT NOT NULL NOT VALID", MinMajor: 18, Locks: target(AccessExclusive), InTx: true, Grammar18: true},
	{Key: "add_unique", Kind: "ALTER TABLE ADD UNIQUE", Locks: target(AccessExclusive), Work: IndexBuild, InTx: true,
		Finding: "DBS-UNIQUE-CONSTRAINT", Message: "ADD UNIQUE builds an index on {table} under ACCESS EXCLUSIVE", Safe: safeUnique},
	{Key: "add_pk", Kind: "ALTER TABLE ADD PRIMARY KEY", Locks: target(AccessExclusive), Work: IndexBuild, InTx: true,
		Finding: "DBS-UNIQUE-CONSTRAINT", Message: "ADD PRIMARY KEY builds an index on {table} under ACCESS EXCLUSIVE", Safe: safeUnique},
	{Key: "add_unique.using_index", Kind: "ALTER TABLE ADD UNIQUE USING INDEX", Locks: target(AccessExclusive), InTx: true},
	{Key: "add_pk.using_index", Kind: "ALTER TABLE ADD PRIMARY KEY USING INDEX", Locks: target(AccessExclusive), InTx: true},
	{Key: "add_pk.using_index.nullable", Kind: "ALTER TABLE ADD PRIMARY KEY USING INDEX", Locks: target(AccessExclusive), Work: Scan, InTx: true,
		Finding: "DBS-SET-NOT-NULL", Message: "PRIMARY KEY USING INDEX on nullable {column} implies SET NOT NULL, which scans {table} under ACCESS EXCLUSIVE",
		Safe: "Make the columns NOT NULL first through a validated CHECK (Eugene W14)"},
	{Key: "add_exclude", Kind: "ALTER TABLE ADD EXCLUDE", Locks: target(AccessExclusive), Work: IndexBuild, InTx: true,
		Finding: "DBS-EXCLUSION", Message: "ADD EXCLUDE builds an index on {table} under ACCESS EXCLUSIVE",
		Safe: "There is no non-blocking way; schedule a quiet window or build a new table and swap"},
	{Key: "validate_constraint", Kind: "ALTER TABLE VALIDATE CONSTRAINT", Locks: target(ShareUpdateExclusive), Work: Scan, InTx: true},
	{Key: "validate_constraint.fk", Kind: "ALTER TABLE VALIDATE CONSTRAINT", Locks: []Lock{{Target, ShareUpdateExclusive}, {Referenced, RowShare}}, Work: Scan, InTx: true},
	{Key: "drop_constraint", Kind: "ALTER TABLE DROP CONSTRAINT", Locks: target(AccessExclusive), InTx: true},
	// Measured: dropping a foreign key takes ACCESS EXCLUSIVE on the referenced table too (its triggers are removed).
	{Key: "drop_constraint.fk", Kind: "ALTER TABLE DROP CONSTRAINT", Locks: []Lock{{Target, AccessExclusive}, {Referenced, AccessExclusive}}, InTx: true},
	{Key: "rename_constraint", Kind: "ALTER TABLE RENAME CONSTRAINT", Locks: target(AccessExclusive), InTx: true},

	// ---- whole-table changes ----
	{Key: "rename_column", Kind: "ALTER TABLE RENAME COLUMN", Locks: target(AccessExclusive), InTx: true,
		Finding: "DBS-RENAME", Message: "RENAME COLUMN {column} to {detail} breaks clients that use the old name", Safe: safeRename},
	{Key: "rename_table", Kind: "ALTER TABLE RENAME", Locks: target(AccessExclusive), InTx: true,
		Finding: "DBS-RENAME", Message: "RENAME {table} to {detail} breaks clients that use the old name", Safe: safeRename},
	{Key: "set_schema", Kind: "ALTER TABLE SET SCHEMA", Locks: target(AccessExclusive), InTx: true,
		Finding: "DBS-RENAME", Message: "SET SCHEMA moves {table} to {detail}; clients that use the old name break", Safe: safeRename},
	{Key: "set_tablespace", Kind: "ALTER TABLE SET TABLESPACE", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-REWRITE", Message: "SET TABLESPACE copies {table} under ACCESS EXCLUSIVE", Safe: safeRepack},
	{Key: "set_logged", Kind: "ALTER TABLE SET LOGGED/UNLOGGED", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-REWRITE", Message: "SET LOGGED/UNLOGGED rewrites {table} under ACCESS EXCLUSIVE"},
	{Key: "set_access_method", Kind: "ALTER TABLE SET ACCESS METHOD", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-REWRITE", Message: "SET ACCESS METHOD rewrites {table} under ACCESS EXCLUSIVE"},
	{Key: "set_options", Kind: "ALTER TABLE SET (storage parameters)", Locks: target(ShareUpdateExclusive), InTx: true},
	{Key: "cluster_on", Kind: "ALTER TABLE CLUSTER ON", Locks: target(ShareUpdateExclusive), InTx: true},
	{Key: "enable_trigger", Kind: "ALTER TABLE ENABLE/DISABLE TRIGGER", Locks: target(ShareRowExclusive), InTx: true},
	{Key: "alter_table", Kind: "ALTER TABLE", Locks: target(AccessExclusive), InTx: true},

	// ---- partitions ----
	// Measured: tables whose foreign keys reference the partitioned table are
	// locked too (the FK is cloned onto the new partition): ACCESS SHARE on
	// 14, SHARE ROW EXCLUSIVE on 17 and 18. 15 and 16 were not measured and
	// use the stronger mode.
	{Key: "attach_partition", Kind: "ALTER TABLE ATTACH PARTITION", MaxMajor: 14,
		Locks: []Lock{{Target, ShareUpdateExclusive}, {Partition, AccessExclusive}, {DefaultPartition, AccessExclusive}, {Referencing, AccessShare}},
		Work:  Scan, InTx: true, Finding: "DBS-ATTACH-SCAN", Message: attachMessage, Safe: attachSafe},
	{Key: "attach_partition", Kind: "ALTER TABLE ATTACH PARTITION", MinMajor: 15,
		Locks: []Lock{{Target, ShareUpdateExclusive}, {Partition, AccessExclusive}, {DefaultPartition, AccessExclusive}, {Referencing, ShareRowExclusive}},
		Work:  Scan, InTx: true, Finding: "DBS-ATTACH-SCAN", Message: attachMessage, Safe: attachSafe},
	// Measured: ACCESS EXCLUSIVE on referencing tables (the docs mention SHARE)
	// and on the default partition, whose implicit constraint changes.
	{Key: "detach_partition", Kind: "ALTER TABLE DETACH PARTITION",
		Locks: []Lock{{Target, AccessExclusive}, {Partition, AccessExclusive}, {Referencing, AccessExclusive}, {DefaultPartition, AccessExclusive}}, InTx: true,
		Finding: "DBS-DETACH-NOT-CONCURRENT", Message: "DETACH PARTITION takes ACCESS EXCLUSIVE on {table}",
		Safe: "DETACH PARTITION ... CONCURRENTLY outside a transaction (not possible while {table} has a default partition)"},
	{Key: "detach_partition.concurrently", Kind: "ALTER TABLE DETACH PARTITION CONCURRENTLY", Locks: []Lock{{Target, ShareUpdateExclusive}, {Partition, ShareUpdateExclusive}, {Referencing, Share}}},
	{Key: "detach_partition.finalize", Kind: "ALTER TABLE DETACH PARTITION FINALIZE", Locks: []Lock{{Target, ShareUpdateExclusive}, {Partition, AccessExclusive}}},
	{Key: "create_table", Kind: "CREATE TABLE", InTx: true},
	{Key: "create_table.fk", Kind: "CREATE TABLE", Locks: []Lock{{Referenced, ShareRowExclusive}}, InTx: true},
	{Key: "create_table.partition_of", Kind: "CREATE TABLE PARTITION OF", MaxMajor: 14,
		Locks: []Lock{{Parent, AccessExclusive}, {DefaultPartition, AccessExclusive}, {Referencing, AccessShare}}, InTx: true},
	{Key: "create_table.partition_of", Kind: "CREATE TABLE PARTITION OF", MinMajor: 15,
		Locks: []Lock{{Parent, AccessExclusive}, {DefaultPartition, AccessExclusive}, {Referencing, ShareRowExclusive}}, InTx: true},

	// ---- drops and maintenance ----
	{Key: "drop_table", Kind: "DROP TABLE", Locks: []Lock{{Target, AccessExclusive}, {Referenced, AccessExclusive}}, InTx: true,
		Finding: "DBS-DROP-TABLE", Message: "DROP TABLE {table} removes the table and its data", Safe: safeDrop},
	{Key: "drop_table.cascade", Kind: "DROP TABLE CASCADE",
		Locks: []Lock{{Target, AccessExclusive}, {Referenced, AccessExclusive}, {Referencing, AccessExclusive}, {Dependents, AccessExclusive}}, InTx: true,
		Finding: "DBS-DROP-TABLE", Message: "DROP TABLE {table} CASCADE removes the table, its data and the foreign keys and views that depend on it", Safe: safeDrop},
	{Key: "drop_view", Kind: "DROP VIEW", Locks: target(AccessExclusive), InTx: true,
		Finding: "DBS-DROP-TABLE", Message: "DROP {detail} {table} breaks clients that query it", Safe: safeDrop},
	{Key: "drop_trigger", Kind: "DROP TRIGGER", Locks: target(AccessExclusive), InTx: true},
	{Key: "truncate", Kind: "TRUNCATE", Locks: target(AccessExclusive), NewStorage: true, InTx: true,
		Finding: "DBS-TRUNCATE", Message: "TRUNCATE {table} deletes every row", Safe: "Delete in batches, or make sure the table is meant to be emptied"},
	{Key: "truncate.cascade", Kind: "TRUNCATE CASCADE", Locks: []Lock{{Target, AccessExclusive}, {Referencing, AccessExclusive}}, NewStorage: true, InTx: true,
		Finding: "DBS-TRUNCATE", Message: "TRUNCATE {table} CASCADE deletes every row of {table} and of the tables that reference it",
		Safe: "Delete in batches, or make sure the table is meant to be emptied"},
	{Key: "vacuum_full", Kind: "VACUUM FULL", Locks: target(AccessExclusive), Work: Rewrite,
		Finding: "DBS-REWRITE", Message: "VACUUM FULL rewrites {table} under ACCESS EXCLUSIVE", Safe: safeRepack},
	{Key: "vacuum", Kind: "VACUUM", Locks: target(ShareUpdateExclusive), Work: Scan},
	{Key: "analyze", Kind: "ANALYZE", Locks: target(ShareUpdateExclusive), InTx: true},
	{Key: "cluster", Kind: "CLUSTER", Locks: target(AccessExclusive), Work: Rewrite, InTx: true,
		Finding: "DBS-REWRITE", Message: "CLUSTER rewrites and sorts {table} under ACCESS EXCLUSIVE", Safe: safeRepack},
	{Key: "cluster.all", Kind: "CLUSTER", Locks: []Lock{{Clustered, AccessExclusive}}, Work: Rewrite,
		Finding: "DBS-REWRITE", Message: "CLUSTER without a table rewrites every previously clustered table under ACCESS EXCLUSIVE", Safe: safeRepack},
	{Key: "refresh_matview", Kind: "REFRESH MATERIALIZED VIEW", Locks: []Lock{{Target, AccessExclusive}, {BaseTables, AccessShare}}, Work: Rewrite, InTx: true,
		Finding: "DBS-REFRESH-MATVIEW", Message: "REFRESH MATERIALIZED VIEW {table} blocks reads of it until the recompute finishes",
		Safe: "REFRESH MATERIALIZED VIEW CONCURRENTLY (needs a unique index on the view and an already populated view)"},
	{Key: "refresh_matview.concurrently", Kind: "REFRESH MATERIALIZED VIEW CONCURRENTLY", Locks: []Lock{{Target, Exclusive}, {BaseTables, AccessShare}}, Work: Scan, InTx: true},
	{Key: "create_trigger", Kind: "CREATE TRIGGER", Locks: target(ShareRowExclusive), InTx: true},
	{Key: "create_view", Kind: "CREATE VIEW", Locks: []Lock{{BaseTables, AccessShare}}, InTx: true},
	{Key: "create_view.replace", Kind: "CREATE OR REPLACE VIEW", Locks: []Lock{{Target, AccessExclusive}, {BaseTables, AccessShare}}, InTx: true},
	{Key: "create_matview", Kind: "CREATE MATERIALIZED VIEW", Locks: []Lock{{BaseTables, AccessShare}}, Work: Scan, InTx: true},
	{Key: "comment", Kind: "COMMENT ON", Locks: target(ShareUpdateExclusive), InTx: true},
	{Key: "lock_table", Kind: "LOCK TABLE", InTx: true,
		Finding: "DBS-LOCK-TABLE", Message: "LOCK TABLE {table} IN {detail} MODE holds the lock until the transaction ends"},
	{Key: "alter_enum.add_value", Kind: "ALTER TYPE ADD VALUE", InTx: true},
	{Key: "alter_enum.rename_value", Kind: "ALTER TYPE RENAME VALUE", InTx: true},
	{Key: "dml", Kind: "DML", Locks: target(RowExclusive), InTx: true},
	{Key: "dml.all_rows", Kind: "DML", Locks: target(RowExclusive), Work: Scan, InTx: true,
		Finding: "DBS-BACKFILL", Message: "{detail} without WHERE touches every row of {table} in one statement",
		Safe: "Batch it (e.g. by primary key ranges) outside the schema migration"},
	{Key: "other", Kind: "", InTx: true},
	{Key: "other.no_tx", Kind: ""},
}

// Lookup returns the rule for key on the given major version.
func Lookup(key string, major int) (Rule, error) {
	for _, r := range Table {
		if r.Key != key {
			continue
		}
		if (r.MinMajor == 0 || major >= r.MinMajor) && (r.MaxMajor == 0 || major <= r.MaxMajor) {
			return r, nil
		}
	}
	return Rule{}, fmt.Errorf("rules: no row for %q on PostgreSQL %d", key, major)
}

// Text fills a rule's placeholders.
func Text(tmpl string, vars map[string]string) string {
	if tmpl == "" {
		return ""
	}
	pairs := make([]string, 0, 2*len(vars))
	for k, v := range vars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}
