// Package groundtruth checks the rules table against real PostgreSQL
// servers (docs/MIGRATIONS.md, "Ground truth"). Its test runs every fixture
// DDL on PostgreSQL 14, 17 and 18 against a populated schema, records the
// locks each statement takes (on every table, not only the target), whether
// it gives the table or its indexes new storage (relfilenode), whether it
// scans the table, and whether it may run in a transaction block, writes the
// measurements to testdata/groundtruth-pg{14,17,18}.json and fails when the
// rules disagree.
//
// Run it with Docker available (or DBSCOPE_TEST_DSN set):
//
//	go test ./internal/migrate/groundtruth -run TestGroundTruth -v
package groundtruth
