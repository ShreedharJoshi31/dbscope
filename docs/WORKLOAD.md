# Workload on the canvas

**Workload** in the canvas toolbar (or the command palette) shows which tables
the database's real traffic uses, as a badge with a heat bar on each table.
It reads statistics PostgreSQL already keeps; it never runs anything against
your tables and never resets a counter.

| Metric | From | Needs |
|---|---|---|
| Query time | `pg_stat_statements`: total time of the statements that use the table | the extension, preloaded |
| Calls | `pg_stat_statements`: calls of those statements | the extension, preloaded |
| Scans | `pg_stat_user_tables`: sequential plus index scans | nothing |
| Writes | `pg_stat_user_tables`: rows inserted, updated and deleted | nothing (always 0 on a replica) |

Heat is on a log scale, so one very busy table does not flatten the rest. The
tooltip of each badge adds the calls and total time of its statements, scan
and write counts, the columns its statements use most, how many calls
`SELECT *` from it, and its slowest statement in total.

## What the numbers are

- **Totals since a reset, not rates.** `pg_stat_statements` counts since
  `pg_stat_statements_info.stats_reset`; table counters since the database's
  statistics reset. The info button next to the metric says since when, and
  flags less than a day of data. Another tool resetting the counters (PgHero's
  capture does) shrinks every number.
- **A statement that uses three tables counts in each**, so percentages of
  query time do not add up to 100.
- **Only queries and data changes count** (SELECT, INSERT, UPDATE, DELETE,
  MERGE). Utility statements such as `COPY` from a backup, `VACUUM`, `EXPLAIN`,
  `DECLARE` (dbscope's own query runner uses these two) and DDL are not tied to
  a table, and statements reading `pg_stat_statements` are left out entirely.
- **Not everything is tied to a table.** Statements whose text this role may
  not read (it lacks `pg_read_all_stats`, part of `pg_monitor`), statements
  that do not parse or whose text pg_stat_statements has dropped, names that
  resolve to no table in the schemas shown, and statements beyond the 2,000
  read are counted as not tied to a table; the info button gives their share
  of calls.
- **Unqualified names resolve through dbscope's connection's `search_path`**
  against the tables the database has, as PostgreSQL would for that role. If
  the application's role uses a different `search_path`, an unqualified name
  may have meant another schema's table; when the first match on the path is a
  table outside the schemas shown, the statement is not tied to a table rather
  than credited to a same-named one. Tables used only inside functions appear
  only in the scan and write counters (with `pg_stat_statements.track = top`).
- **Columns:** a qualified column (`o.total`) counts for its table; an
  unqualified one only when exactly one table in the statement has it.
- **A replica** has its own counters: only the reads that ran on it, and no
  writes.

Reading is one read-only transaction with the connection's statement timeout:
`pg_stat_user_tables`, then `pg_stat_statements_info`, then the 2,000
statements of this database with the most total time (`toplevel` only).
Scanning `pg_stat_statements` holds a shared lock on its hash table and loads
query texts, so dbscope reads it only while Workload is on: when you turn it
on, and again at most once a minute after that.

## Setup

```sql
-- postgresql.conf: shared_preload_libraries = 'pg_stat_statements' (restart)
CREATE EXTENSION pg_stat_statements;
GRANT pg_monitor TO dbscope_reader;  -- to see other roles' statement texts
```

Without the extension the canvas still shows scans and writes, and says why
query time is missing (not installed in this database, or installed but not
preloaded).

## In migration review

Statements that take ACCESS EXCLUSIVE on a table with at least 5% of the
database's table scans, or a SHARE-class lock (which stops only writes) on one
with at least 5% of its row writes, at least 1,000 either way, score two points
higher and get the `DBS-HOT-TABLE` finding; query dependents from `pg_stat_statements`
show their call counts ([MIGRATIONS.md](MIGRATIONS.md)). The scan counters also
travel in `dbscope stats export`, so CI checks without database access see
them too.

Research and sources: [research-workload.md](research-workload.md).
