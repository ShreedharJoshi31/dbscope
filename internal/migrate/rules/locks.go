// Package rules holds dbscope's knowledge of what each DDL statement does in
// PostgreSQL 14-18 (the lock it takes on each table, whether it scans,
// rebuilds an index or rewrites the table, and whether it may run in a
// transaction block) as a data table, plus the classifier that maps a parsed
// statement onto rows of that table. The table is checked against real
// servers by internal/migrate/groundtruth.
package rules

import "fmt"

// Mode is a PostgreSQL table-level lock mode, weakest first.
type Mode int

const (
	NoLock Mode = iota
	AccessShare
	RowShare
	RowExclusive
	ShareUpdateExclusive
	Share
	ShareRowExclusive
	Exclusive
	AccessExclusive
)

var modeNames = [...]string{"", "ACCESS SHARE", "ROW SHARE", "ROW EXCLUSIVE", "SHARE UPDATE EXCLUSIVE",
	"SHARE", "SHARE ROW EXCLUSIVE", "EXCLUSIVE", "ACCESS EXCLUSIVE"}

var lockNames = [...]string{"", "AccessShareLock", "RowShareLock", "RowExclusiveLock", "ShareUpdateExclusiveLock",
	"ShareLock", "ShareRowExclusiveLock", "ExclusiveLock", "AccessExclusiveLock"}

// String is the SQL name used by LOCK TABLE and the docs, e.g. "ACCESS EXCLUSIVE".
func (m Mode) String() string {
	if m < 0 || int(m) >= len(modeNames) {
		return fmt.Sprintf("Mode(%d)", int(m))
	}
	return modeNames[m]
}

// LockName is the pg_locks.mode spelling, e.g. "AccessExclusiveLock".
func (m Mode) LockName() string { return lockNames[m] }

// ModeFromLockName parses a pg_locks.mode value.
func ModeFromLockName(s string) Mode {
	for i, n := range lockNames {
		if n == s && i > 0 {
			return Mode(i)
		}
	}
	return NoLock
}

// Class is the L term of the score (docs/MIGRATIONS.md): 0 for locks that
// only conflict with ACCESS EXCLUSIVE-level maintenance (ACCESS SHARE, ROW
// SHARE, ROW EXCLUSIVE), 1 for SHARE UPDATE EXCLUSIVE, 2 for locks that
// block writes, 3 for ACCESS EXCLUSIVE, which blocks reads too.
func (m Mode) Class() int {
	switch {
	case m == AccessExclusive:
		return 3
	case m >= Share:
		return 2
	case m == ShareUpdateExclusive:
		return 1
	default:
		return 0
	}
}

// Work is how much data a statement touches.
type Work int

const (
	None Work = iota
	Scan
	IndexBuild
	Rewrite
)

var workNames = [...]string{"none", "scan", "index_build", "rewrite"}

func (w Work) String() string { return workNames[w] }

// W is the score's work term: 0 catalog only, 1 scan or index build, 2 rewrite.
func (w Work) W() int {
	switch w {
	case Rewrite:
		return 2
	case Scan, IndexBuild:
		return 1
	}
	return 0
}

// Role names which relation a lock in a rule applies to.
type Role string

const (
	// Target is the relation the statement names (for DROP INDEX, the index's table).
	Target Role = "target"
	// Referenced are the tables a foreign key in the statement points at,
	// or that the target's foreign keys point at (DROP TABLE).
	Referenced Role = "referenced"
	// Referencing are the tables whose foreign keys point at the target.
	Referencing Role = "referencing"
	// Partition is the partition named by ATTACH/DETACH PARTITION.
	Partition Role = "partition"
	// DefaultPartition is the target's DEFAULT partition, when it has one.
	DefaultPartition Role = "default_partition"
	// Partitions are all partitions of a partitioned target.
	Partitions Role = "partitions"
	// Index is the index the statement names.
	Index Role = "index"
	// Indexes are all indexes of the target.
	Indexes Role = "indexes"
	// BaseTables are the relations a view or materialized view query reads.
	BaseTables Role = "base_tables"
	// Parent is the partitioned table of a CREATE TABLE ... PARTITION OF.
	Parent Role = "parent"
	// Dependents are the views and materialized views that read the target
	// (dropped by CASCADE).
	Dependents Role = "dependents"
	// Clustered are the tables with a clustered index (bare CLUSTER).
	Clustered Role = "clustered"
)

// Lock is a lock a rule takes on the relations in a role.
type Lock struct {
	Role Role
	Mode Mode
}
