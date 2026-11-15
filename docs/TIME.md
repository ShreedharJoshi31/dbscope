# Time

**Time** is partly built. Built: per-table detection of native AS OF
support (see "Per-table detection (built)"), AS OF queries and their
bounds on Oracle, SQL Server and MariaDB in the agent (see "AS OF queries
(built)"), and the "As of" picker in the SQL editor (see "Time picker
(built)"). Not built: MongoDB, and the schema timeline described next. The
rest records the design and the measurements it rests on, so the
distinction between "measured" and "planned" matters throughout.

It has two parts:

- **Schema timeline.** A history of a connection's schema snapshots, with a
  canvas scrubber that steps through them and highlights what changed.
- **Native AS OF queries.** Letting a query read a table as it stood at a
  past instant, using each engine's own historical-read syntax instead of a
  restore or an export.

AS OF is planned for four engines that have native, query-level historical
reads: Oracle, SQL Server, MariaDB, and MongoDB. MySQL and ClickHouse are
covered below because they were measured and found to have no such syntax
(MySQL 8.4 has no system versioning at all; ClickHouse has none for
MergeTree, only for Iceberg tables). PostgreSQL is out of scope for native
AS OF; see "PostgreSQL" below for why. Every fact in this document that
comes from a container run is marked "measured on" the image and version
tested. Facts that are reasoned rather than run are marked "not measured" or
"planned".

## Schema timeline

### What is stored today

`schema_snapshots` (`internal/store/migrations/0007_schema_snapshots.sql`)
holds one row per observed schema structure: `id`, `profile_id`, `taken_at`,
`hash`, `relations`, `schemas_json`, and the full gzipped `schema.Model` JSON
in `model`. It is not a diff. `SaveSnapshot`
(`internal/store/snapshots.go:28-62`) compares the new hash only against the
newest row for that profile; if they match, nothing is written. Pruning runs
in the same transaction and deletes every row past the newest
`SnapshotsKept` (a constant, 20 today, `snapshots.go:15`) by `id`, not by
age. The only writer today is the `/schema` route
(`internal/agent/schema_handlers.go:101-132`); other routes that introspect
the same data, such as workload (`schema_handlers.go:303`), do not save a
snapshot, and there is no scheduler anywhere in the codebase (confirmed by
searching for `NewTicker`, `Tick(`, `AfterFunc`, cron and scheduler; the only
ticker found is an SSH keepalive in `internal/sshtunnel/sshtunnel.go:351`).

Because dedup only compares against the newest row, there is no record of
"the structure was observed again, unchanged." A gap between two rows today
could mean the schema changed once, or that dbscope simply never looked
during that window. The timeline as designed needs to tell those apart.

### The planned change

`schema_snapshots` gains a `last_seen_at` column. Whenever an introspection
produces a hash equal to the newest snapshot's hash, dbscope updates that
row's `last_seen_at` instead of skipping the write entirely. Each timeline
step then reads as "observed from `taken_at` to `last_seen_at`," and any gap
after `last_seen_at` and before the next snapshot's `taken_at` means "not
observed," never "no change." This removes the ambiguity above without
storing a row per observation.

Retention becomes a per-connection setting, `snapshots_kept`, with a default
of 20 (matching today's constant) and a range of 2 to 500, stored the way
other per-connection settings are (as a column on `profiles`, edited through
`PUT /api/profiles/{id}`, alongside `statement_timeout_ms` and `max_cost`;
see `internal/store/profiles.go` and migrations `0001`, `0003`, `0006`,
`0009`). Pruning stays an `id`-ordered delete in the same transaction as each
save; the setting only changes how many rows it keeps.

No background job is planned. Snapshots are saved only from routes that
already introspect the same schema selection, so adding a save call to a
route (as a design, not a scheduled task) is enough; the save is cheap
relative to the introspection that already ran. This matches the project's
existing approach, which has no scheduler of any kind.

### Canvas scrubber

A scrubber is planned on the canvas that steps through a connection's
snapshots and highlights added, removed, and changed tables and columns
against the previous step, reusing the pieces that already do this for the
Drift tab: `diffModels` and `compareRelation`
(`web/src/entities/schema/model/drift.ts:31,63`), the `DiffOverlay` held by
`useCanvasStore` (`web/src/entities/schema/model/canvas-store.ts:54,398`),
and the table-card badges in `web/src/entities/schema/ui/table-node.tsx`.

Two gaps in the existing pieces will need design decisions, not measured
facts:

- `DiffOverlay.relations` only has `added`, `altered`, and `dropped`. A
  timeline step that was "not observed" is a fourth state the type does not
  have yet.
- Dropped tables are not drawn on the canvas today, because the canvas
  graph comes from the current model (`getSchemaGraph`,
  `web/src/entities/schema/model/graph.ts:85,123,126`), not from the
  snapshot being viewed. Showing a dropped table on a timeline step may need
  the graph to come from the snapshot's own model instead of the live one.

## Native AS OF per engine

Each subsection quotes SQL exactly as it appears in the throwaway test file
for that engine (`internal/<pkg>/zz_time_<pkg>_test.go` in the measurement
worktree), and states only what the test runs showed. Anything inferred
rather than run is marked "not measured."

### Per-table detection (built)

Introspection now marks which relations support a native AS OF read, as
`Relation.TimeTravel` (`kind`, `available`, `retention`, `historyOf`,
`note`), and says per model whether the server has such reads at all, as
`Model.TimeTravel` (`supported`, `note`). The `timeTravel` capability is true
for Oracle, SQL Server and MySQL (the MySQL engine also covers MariaDB; the
model fields tell them apart). None of these fields is part of
`schema.StructureHash`, so turning detection on, or a grant changing, does
not record a new snapshot (`TestStructureHashIgnoresTimeTravel`). The canvas
shows an "as of" badge on each flagged table card, muted when `available` is
false, and the table details panel has a "Time travel" row. The query
rewrite is described in "AS OF queries (built)" and the editor's picker in
"Time picker (built)".

Each engine adds a fixed number of queries per introspection, not one per
table. `TestTimeTravelDetection` in each package checks the flags on real
servers, and for Oracle and SQL Server also checks that each flag matches
whether an AS OF read by that user succeeds.

- **SQL Server** (`internal/mssql/introspect.go`, `temporalTables`): one
  query, run on SQL Server 2017 and later (`history_retention_period` first
  appears in 2017) and on Azure SQL:

  ```sql
  SELECT s.name, t.name, t.temporal_type, t.history_retention_period, t.history_retention_period_unit_desc,
         OBJECT_SCHEMA_NAME(t.history_table_id) + '.' + OBJECT_NAME(t.history_table_id),
         HAS_PERMS_BY_NAME(QUOTENAME(OBJECT_SCHEMA_NAME(t.history_table_id)) + '.' + QUOTENAME(OBJECT_NAME(t.history_table_id)), 'OBJECT', 'SELECT'),
         (SELECT SCHEMA_NAME(p.schema_id) + '.' + p.name FROM sys.tables p WHERE p.history_table_id = t.object_id)
  FROM sys.tables t JOIN sys.schemas s ON s.schema_id = t.schema_id
  WHERE t.temporal_type IN (1, 2) AND s.name IN (...)
  ```

  `temporal_type` 2 gives kind `temporal`, with retention such as
  `3 months`, or `infinite` for `-1`. It is `available` only when the user
  has SELECT on the history table. When the user cannot see the history
  table's name at all, the note is "Needs SELECT on the history table, whose
  name this user cannot see". When the name is visible (for example through
  VIEW DEFINITION) but SELECT is missing, the note is "Needs SELECT on the
  history table dbo.acct_history". `temporal_type` 1 gives kind `history`
  with `historyOf` set and `available` true. Measured on 2025 and 2022 as
  sa, as a `db_datareader` user, as a user with SELECT on `dbo.acct` only,
  and as that user plus VIEW DEFINITION on `dbo.acct_history`.
- **MariaDB** (`internal/mysql/introspect.go`): the existing
  `information_schema.TABLES` query also reads `CREATE_OPTIONS`.
  `TABLE_TYPE = 'SYSTEM VERSIONED'` gives kind `system-versioned`,
  `available` true (SELECT is enough). The relation's kind stays `table`.
  Only when a versioned table's `CREATE_OPTIONS` contains `partitioned`,
  one more query runs:

  ```sql
  SELECT DISTINCT TABLE_SCHEMA, TABLE_NAME FROM information_schema.PARTITIONS
  WHERE TABLE_SCHEMA IN (...) AND PARTITION_METHOD = 'SYSTEM_TIME'
  ```

  Those tables get the note "History is partitioned; dropped history
  partitions read as empty, not as an error". A versioned table partitioned
  BY HASH gets no note. Measured on 11.8 as root and as a user with
  `SELECT ON app.*`.
- **MySQL**: no table is flagged, and the model carries
  `{"supported": false, "note": "MySQL has no system-versioned tables; MariaDB does (WITH SYSTEM VERSIONING)"}`.
  The flavor comes from the existing `Describe`. Measured on 8.4.
- **Oracle** (`internal/oracle/introspect.go`, `flashback`): every table and
  view read gets kind `flashback` and no retention (bounds come later). It
  uses at most two queries:

  ```sql
  SELECT USER, (SELECT count(*) FROM session_privs WHERE privilege = 'FLASHBACK ANY TABLE') FROM dual
  -- only when FLASHBACK ANY TABLE is missing:
  SELECT DISTINCT table_schema, table_name FROM all_tab_privs
  WHERE privilege = 'FLASHBACK' AND table_schema IN (...)
    AND (grantee IN (USER, 'PUBLIC') OR grantee IN (SELECT role FROM session_roles))
  ```

  `available` is true for the owner, for a user with FLASHBACK ANY TABLE,
  and for a user with a FLASHBACK grant on the object, directly or through
  an enabled role. Otherwise the note is "Needs the FLASHBACK privilege on
  this table, or FLASHBACK ANY TABLE". Measured on 23.26 Free with a non-DBA
  user for each case: the owner; READ only; FLASHBACK on the table and on a
  view; FLASHBACK ANY TABLE; FLASHBACK through a role; and FLASHBACK through
  a role granted to another role. `ALL_TAB_PRIVS` listed the role and the
  nested-role grants for that user, and the AS OF read succeeded in exactly
  the cases flagged available. Not measured: global temporary tables and
  external tables, which are flagged like any other table.

### AS OF queries (built)

`POST /api/profiles/{id}/query` and `/explain` take an optional `asOf`
(RFC 3339 with an offset or `Z`; docs/API.md). Without it, nothing changes.
With it, the agent (`internal/agent/timetravel_handlers.go`):

1. refuses (501) unless the profile has the `timeTravel` capability;
2. parses `asOf`, converts it to UTC, and refuses (400) a time more than 1 s
   after the agent's clock;
3. runs the engine's `Check` on the text as written, so a statement the
   guard refuses gets the same refusal with or without `asOf`;
4. rewrites the text with the engine's rewriter (`internal/oracle/asof.go`,
   `internal/mssql/asof.go`, `internal/mysql/asof.go`, sharing the walk in
   `internal/timetravel`);
5. runs the engine's `Check` again on the rewritten text, then `Run`
   exactly as without `asOf`. `executedSql` is the rewritten text; history
   keeps the text as written.

`Check` and `Run` are not changed. The only tokenizer change is a byte
offset on SQL Server tokens, which Oracle's and MySQL's already had.

**Where the clause goes.** The rewriter walks each engine's own tokens. At
every nesting level (subqueries, derived tables, UNION branches, CTE
bodies), the table reference after `FROM`, `JOIN`, `APPLY`,
`STRAIGHT_JOIN` and each comma of a FROM list gets the clause inserted
straight after its name and before any alias or hint. It is not inserted
after a subquery's parentheses, a CTE name (scoped to the level that
defines it; on MySQL and MariaDB a WITH query's own name inside its body is
the table, unless the WITH says RECURSIVE), a table function (a name
followed by `(`), or `DUAL`. MySQL index hints (`USE INDEX FOR JOIN (...)`)
do not end the FROM clause. Strings
and comments are tokens or gaps the walk never inserts into. `FROM` inside
a function call, such as `EXTRACT(YEAR FROM d)`, is not a FROM clause,
because no `SELECT` opened that level. The rewrite only inserts the
fixed clause below, built by Go from the validated time.

| Engine | Clause after each table name |
|---|---|
| Oracle | `AS OF TIMESTAMP CAST(FROM_TZ(TIMESTAMP 'YYYY-MM-DD HH24:MI:SS.FFFFFF', 'UTC') AT TIME ZONE TO_CHAR(SYSTIMESTAMP, 'TZH:TZM') AS TIMESTAMP)` |
| SQL Server | `FOR SYSTEM_TIME AS OF 'YYYY-MM-DDTHH:MM:SS.fffffff'` (UTC digits, no offset) |
| MariaDB | `FOR SYSTEM_TIME AS OF FROM_UNIXTIME(<seconds>.<microseconds>)` |

The Oracle form differs from the first design, `FROM_TZ(..., 'UTC')`
alone. Measured on 23.26 Free (`TestAsOf` in `internal/oracle`): AS OF
TIMESTAMP drops the zone of a TIMESTAMP WITH TIME ZONE and reads its
digits in the zone of the server's clock (`SYSTIMESTAMP`), not the session
time zone. On a server with a UTC clock, `FROM_TZ(<UTC digits>, 'UTC')`
read checkpoint A correctly and `FROM_TZ(<+05:30 digits>, '+05:30')` gave
`ORA-08186` (read as 5.5 hours in the future). On a server whose clock was
`TZ=Asia/Kolkata`, the reverse held: the UTC form gave `ORA-01466` (read as
5.5 hours earlier) and the +05:30 form read A. The built clause therefore
moves the UTC instant into the clock's current offset first. It read
checkpoints A and B correctly on both servers, with a session time zone of
+05:30 in each. Not measured: a server whose clock zone has DST, where the
current offset can differ from the offset at the past instant.

SQL Server and MariaDB: measured on 2025 and 2022, and on MariaDB 11.8,
reading checkpoints A and B, joins of two versioned tables (`JOIN` and
comma), and, on MariaDB, under session time zones UTC and +05:30, including
one microsecond before and exactly at a row's `ROW_START`.

**Which tables are versioned.** The agent does not cache the schema model,
and the newest snapshot can be stale (time-travel flags are not part of the
snapshot hash, so a grant or a new versioned table may never write a new
snapshot). So each `asOf` request resolves only the names the query
references, live, with the same catalog reads introspection uses:

- Oracle: `ALL_OBJECTS` for the names (an unqualified name in
  `SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')`), then the `flashback`
  privilege check from introspection;
- SQL Server: `OBJECT_ID` on each name, which resolves an unqualified name
  the way the query will, then `temporalTables` from introspection;
- MariaDB: `information_schema.TABLES` for the names (an unqualified name
  in `DATABASE()`).

This is a few small queries per request (at most five), independent of
schema size.

**Refusals.** Each is a 400 with a message naming the tables:

- a table that cannot be read as of a time: on Oracle, no FLASHBACK
  privilege (`available` false); on SQL Server, not a temporal table, a
  history table, or no SELECT on the history table; on MariaDB, not
  system-versioned;
- a view on SQL Server or MariaDB (a view can mix versioned and current
  tables); Oracle views are allowed, since AS OF applies to them given
  FLASHBACK on the view (measured);
- a name that does not resolve: unknown, not visible, a synonym, more than
  two parts (another database or a database link);
- text that already reads history (`AS OF`, `FOR SYSTEM_TIME`, `VERSIONS
  BETWEEN`, `VERSIONS PERIOD`);
- anything the walk cannot account for: a statement other than SELECT or
  WITH, a parenthesized join, `PARTITION`, `SUBPARTITION`, `SAMPLE`,
  `TABLESAMPLE` or `@dblink` after a name, Oracle `ONLY (...)` and
  `CONTAINERS (...)`, SQL Server `CONTAINSTABLE` and similar functions that
  read a named table, a SQL Server hint written as `t (NOLOCK)` without
  `WITH`, a MySQL `/*! ... */` comment, unbalanced parentheses;
- text the engine's tokenizer may read differently from the server: an
  Oracle `NQ'...'` string (the tokenizer reads `q'` quoting only at the
  start of a token), and MariaDB with `ANSI_QUOTES` or
  `NO_BACKSLASH_ESCAPES` in the session `sql_mode`;
- on MariaDB, an instant the session time zone gives twice (the repeated
  hour when clocks go back), found by checking that
  `UNIX_TIMESTAMP(FROM_UNIXTIME(x))` gives back `x` (not measured with a
  zone that has DST);
- MySQL, always: "MySQL has no system-versioned tables, so it cannot read
  as of a past time";
- MariaDB before 1970-01-01T00:00:01Z, which `FROM_UNIXTIME` cannot express.

A statement that reads no table (`SELECT 1 FROM dual`) runs unchanged.

**Known gap.** A function the query calls reads current data: a table
function in FROM (left unchanged by design) or a scalar function anywhere.
Built-in functions read no tables, and on Oracle `Run` already refuses a
query that reaches user PL/SQL. On SQL Server and MariaDB, a user-defined
function that reads a table is neither rewritten nor refused.

**Errors after the rewrite.** With `asOf`, Oracle `ORA-01555`, `ORA-08180`
and `ORA-01466` read "This table cannot be read that far back (ORA-nnnnn):
choose a later time"; `ORA-41900` names the FLASHBACK privilege; `ORA-08186`
says the time is in the future by the database's clock. SQL Server Msg
13544 and MariaDB error 4124 (should not happen after the refusals) read as
"not system-versioned".

**Bounds.** `GET /api/profiles/{id}/time-travel?tables=schema.a,schema.b`
(docs/API.md) gives each table's oldest readable time, measured against the
database server's clock (`now` in the response):

- Oracle: `oldest = max(now - max(undo_retention, latest
  V$UNDOSTAT.TUNED_UNDORETENTION), LAST_DDL_TIME + 5 s)`, `source`
  `undo_retention` or `tuned_undo_retention`, whichever is larger. Without
  `SELECT_CATALOG_ROLE` the V$ views give ORA-00942, so 900 s (the default
  `undo_retention`) is assumed with `source` `assumed_default` and a note
  saying so. The 5 s margin is measured: `LAST_DDL_TIME + 1 s` after a
  `DROP COLUMN` still gave ORA-01466, `+ 2 s` read the old rows.
  `LAST_DDL_TIME` can also move on DDL that AS OF survives, such as adding
  a column (not measured here), so the bound can be later than necessary. The response note says undo retention is a target, not a
  promise, unless the undo tablespace has RETENTION GUARANTEE.
- SQL Server: a finite `history_retention_period` gives `oldest =
  DATEADD(<unit>, -period, SYSUTCDATETIME())` on the server, `source`
  `history_retention`; INFINITE gives `oldest` null, `source` `none`, note
  "History is kept until removed".
- MariaDB: `oldest` null, `source` `none`, with a note that a time before
  the history began, a dropped history partition, or DELETE HISTORY read as
  empty, not as an error.

A table that cannot be read as of a time (or does not resolve) has
`available` false, `oldest` null, `source` `none` and a note saying why.

### Time picker (built)

The SQL editor toolbar has an "As of" control
(`web/src/features/time-travel`) when the profile has the `timeTravel`
capability; other engines show no control. When the model says
`timeTravel.supported` is false (MySQL), the popover shows only the
model's note. Otherwise it holds a date and time input (seconds, in the
browser's zone), Now (the database server's time from the bounds), Clear,
and a list of tables with their kind, whether this user can read them as
of a time (or the note saying why not), and the oldest readable time. An
`assumed_default` bound reads "about 15 minutes, assumed: undo retention
could not be read".

The tables are the versioned relations whose name the SQL mentions as a
whole identifier, or, when it mentions none, every versioned relation in
the model (at most 50). This is a text match for the bounds request only;
the agent decides which tables a query reads and refuses what it cannot
rewrite, and the editor does not repeat those checks. The bounds are read
each time the popover opens. The input's range runs from the latest
`oldest` of the available tables to the server's `now`; a time outside it
shows why and disables Apply.

Apply stores the instant on the SQL tab, as UTC with `Z`, and the tab
saves it with its text, so it survives a reload. Switching to another
connection clears it. While it is set, the toolbar shows "As of <local
time>" with a button to clear it, Run and Explain send `asOf`, and the
results header and run status name the time the results were read as of.

### Oracle

Measured on `container-registry.oracle.com/database/free:latest-lite`,
Oracle AI Database 26ai Free 23.26.3.0.0, service FREEPDB1, session time
zone +05:30, database time zone +00:00.

**Syntax.** Flashback query, either form:

```sql
SELECT bal FROM tt.acct AS OF TIMESTAMP TO_TIMESTAMP('2026-11-14 19:29:03.000000','YYYY-MM-DD HH24:MI:SS.FF6') WHERE id = 1
SELECT bal FROM tt.acct AS OF SCN 2978771 WHERE id = 1
```

Also measured as working: `VERSIONS BETWEEN SCN ... AND MAXVALUE`,
`TIMESTAMP_TO_SCN(...)`, `SCN_TO_TIMESTAMP(...)`, and `ORA_ROWSCN`. AS OF
accepts an expression, not just a literal:

```sql
SELECT bal FROM tt.acct AS OF TIMESTAMP (SYSTIMESTAMP - INTERVAL '10' SECOND) WHERE id = 1
```

Interval literals over 99 units need an explicit precision:
`INTERVAL '365' DAY` fails with `ORA-01873: the leading precision of the
interval is too small`; `INTERVAL '365' DAY(3)` is required.

**Detection.** AS OF applies to every ordinary table; there is no per-table
catalog flag to query, unlike SQL Server or MariaDB.

**Privileges.** READ alone is not enough. A user with only
`GRANT READ ON tt.acct` gets `ORA-41900: missing READ privilege on
"TT"."ACCT"` on both AS OF forms (the message names READ even though the
user holds READ) while a plain SELECT by the same user works. Any one of
three things is enough instead: `GRANT FLASHBACK ON tt.acct`, `GRANT
FLASHBACK ANY TABLE`, or being the table's owner. A view needs FLASHBACK on
the view itself; FLASHBACK on the view's base table is neither needed nor
sufficient (`GRANT FLASHBACK ON tt.v_acct` fixed a user who had no
FLASHBACK on the base table at all). Through `oracle.Check` then
`oracle.Run` (plan gate, `EXPLAIN PLAN`, then a `SET TRANSACTION READ ONLY`
transaction), the FLASHBACK-granted user got rows back; the READ-only user
failed inside EXPLAIN with `ORA-41900: missing FLASHBACK privilege on
"TT"."ACCT"`; on this path the message names the right privilege, unlike
the direct-query error.

**Oldest readable point and how dbscope bounds it.** Undo retention governs
this: `undo_retention`=900 (seconds), `undo_management`=AUTO,
`LOCAL_UNDO_ENABLED`=TRUE, the undo tablespace is `NOGUARANTEE` (a target,
not a guarantee). `V$UNDOSTAT.TUNED_UNDORETENTION` read 900 on the one row
measured. Reading these views (`v$parameter`, `v$undostat`,
`dba_tablespaces`) needs `SELECT_CATALOG_ROLE`; without it, a user gets
`ORA-00942: table or view "SYS"."V_$PARAMETER" does not exist` on all three,
and `USER_TABLESPACES` returns 0 rows. The design bounds the picker at
roughly `now − max(TUNED_UNDORETENTION, undo_retention)`, falling back to a
fixed default when the role lacks `SELECT_CATALOG_ROLE` (the built bounds use
900 s, the default `undo_retention`; a design choice, not a measured fact).
The picker must never go before the table's `last_ddl_time` (see traps
below) and never into the future.

**Errors and what they mean.**

| Error | Meaning |
|---|---|
| `ORA-41900: missing READ/FLASHBACK privilege on "..."` | not privileged for that table or view |
| `ORA-01466: unable to read data - table definition has changed` | the requested time is before the table's last DDL |
| `ORA-08180: no snapshot found based on specified time` | too far in the past for undo to reach |
| `ORA-01555: snapshot too old: rollback segment ... too small` | undo was overwritten; forced in testing with a 10 MB non-autoextend undo tablespace and 80 updates of 300 × CHAR(1000) rows |
| `ORA-08186: invalid timestamp specified` | the timestamp is in the future |
| `ORA-08181: specified number is not a valid system change number` | the SCN is in the future, or otherwise invalid |

One cause can surface as either ORA-01466 or ORA-01555 for the same moment
depending on whether the query uses AS OF TIMESTAMP or AS OF SCN (both were
seen for the same forced-too-old read).

**Traps.**

- Time before the table's last DDL always gives ORA-01466, not ORA-08180.
  `ADD (note VARCHAR2(10))` survives (AS OF before it still worked); `DROP
  COLUMN`, `TRUNCATE TABLE`, and `MODIFY (v VARCHAR2(50))` all give
  ORA-01466, by TIMESTAMP and by SCN. A design bounding the picker purely by
  undo retention would still need to account for the table's own DDL
  history, since that limit can be much more recent than the undo window.
- `TIMESTAMP_TO_SCN` appears to clamp rather than fail for small offsets:
  −1 HOUR, −1 DAY, and −30 DAY all returned the same SCN (2972134) in one
  run; −365 DAY(3) gave ORA-08180. Where the clamp turns into a failure,
  between 30 and 365 days, was not measured.
- `DBMS_FLASHBACK.ENABLE_AT_TIME` is a session-level switch and is not used
  by the design (see "Read-only safety" below): it breaks `oracle.Run` with
  `ORA-08182: operation not supported while in Flashback mode` once enabled,
  because both `SET TRANSACTION READ ONLY` and `EXPLAIN PLAN` fail in that
  mode. Granting only `EXECUTE` on `DBMS_FLASHBACK` (no FLASHBACK privilege
  at all) let a READ-only user read past data through a plain SELECT once
  flashback mode was enabled: a second way around the per-table FLASHBACK
  check that the design must not reintroduce. `oracle.Check` already
  refuses the token `flashback`, which blocks this package by name.

### SQL Server

Measured on `mcr.microsoft.com/mssql/server:2025-latest` (17.0.5035.5,
RTM-CU10, Enterprise Developer) and `2022-latest` (16.0.4305.5, Developer).
Both gave the same results in every measurement below. The container clock
is UTC.

**Syntax.** Temporal table, created with:

```sql
CREATE TABLE dbo.acct (id int PRIMARY KEY, bal int,
	valid_from datetime2 GENERATED ALWAYS AS ROW START NOT NULL,
	valid_to datetime2 GENERATED ALWAYS AS ROW END NOT NULL,
	PERIOD FOR SYSTEM_TIME (valid_from, valid_to))
	WITH (SYSTEM_VERSIONING = ON (HISTORY_TABLE = dbo.acct_history, HISTORY_RETENTION_PERIOD = 3 MONTHS))
```

Then:

```sql
SELECT id, bal FROM dbo.acct FOR SYSTEM_TIME AS OF '2026-11-14T19:39:30.4500000' ORDER BY id
```

`ALL`, `BETWEEN ... AND ...`, and `FROM ... TO ...` also work. Only a
literal or a driver variable is allowed after AS OF:
`AS OF DATEADD(second, -1, SYSUTCDATETIME())` fails with `Msg 102, Level 15,
State 1: Incorrect syntax near 'DATEADD'` (the same for `CAST`), while
`AS OF @p1` with a bound parameter works.

**Detection.**

```sql
SELECT SCHEMA_NAME(t.schema_id) + '.' + t.name tbl, t.temporal_type, t.temporal_type_desc,
       OBJECT_NAME(t.history_table_id) history,
       t.history_retention_period, t.history_retention_period_unit, t.history_retention_period_unit_desc
FROM sys.tables t ORDER BY t.name
```

`temporal_type` is 2 for `SYSTEM_VERSIONED_TEMPORAL_TABLE`, 1 for the
history table, 0 otherwise. `OBJECTPROPERTY(id, 'TableTemporalType')`
returns the same 2/1/0. `sys.databases.is_temporal_history_retention_enabled`
is true by default and can be toggled with `ALTER DATABASE ... SET
TEMPORAL_HISTORY_RETENTION OFF/ON`.

**Privileges.** A `db_datareader`-only user can read all of this detection
metadata and can query AS OF, ALL, and the history table directly. A user
with SELECT on `dbo.acct` only (no grant on the history table) gets `Msg
229, Level 14, State 5: The SELECT permission was denied on the object
'acct_history'` on AS OF and ALL, and sees `OBJECT_NAME(history_table_id)`
as NULL. Separately, `mssql.Run` itself needs the `SHOWPLAN` permission for
every query it runs, not only temporal ones: without it, `Msg 262: SHOWPLAN
permission denied in database 'tt'`. `db_datareader` does not include
SHOWPLAN; it must be granted on top.

**Oldest readable point and how dbscope bounds it.** There is no lower
error bound: AS OF before the table's history, or before `0001-01-01`,
returns 0 rows with no error at all. The real bound is retention. With a
finite `HISTORY_RETENTION_PERIOD`, rows older than `now − retention`
disappear immediately once that period elapses, not at some later cleanup
pass: loading history rows dated 2020 and setting `HISTORY_RETENTION_PERIOD
= 1 DAY` made `AS OF '2020-03-01'` return 0 rows right away, even with the
database-level `TEMPORAL_HISTORY_RETENTION` flag turned OFF. With `INFINITE`
retention (the default when none is set), the same query returned the row.
The design clamps the picker at `now − history_retention_period` per table,
reading `-1/-1` (`INFINITE`) as no lower bound.

**Errors and what they mean.**

| Message | Meaning |
|---|---|
| `Msg 102: Incorrect syntax near '...'` | AS OF was given an expression, not a literal or variable |
| `Msg 13744: '...' is not a valid history retention period unit` | MINUTE or HOUR was used; DAY (and larger) works |
| `Msg 13765: Setting finite retention period failed ... does not contain required clustered index` | the history table has no clustered index, needed for finite retention |
| `Msg 229: The SELECT permission was denied on the object 'acct_history'` | SELECT is missing on the history table |
| `Msg 262: SHOWPLAN permission denied in database '...'` | `mssql.Run`'s plan gate failed; not temporal-specific |
| `Msg 13544: Temporal FOR SYSTEM_TIME clause can only be used with system-versioned tables. '...' is not a system-versioned table.` | the target is not a system-versioned table (a plain table, the history table itself, or a ledger table without PERIOD) |

**Traps.** The time zone offset on a literal is silently dropped: writing
the same instant as A with a `+05:30` offset returned the *current* row
(300) instead of A's 100. The server appears to treat the clock digits as
UTC regardless of the offset written, which can push the parsed time into
the future. The design's mitigation is to always send a UTC literal with no
offset, never a local time with an offset attached (this is a design
choice; it was not itself run as a fix, only the failure mode was
measured). Ledger tables are a second trap: `LEDGER = ON` without a `PERIOD`
clause gives a ledger with `temporal_type_desc` NON_TEMPORAL_TABLE, and `FOR
SYSTEM_TIME` on it fails with Msg 13544, so a ledger alone offers no AS OF. A
ledger table that *also* declares `PERIOD FOR SYSTEM_TIME` does support AS
OF normally.

### MariaDB

Measured on `public.ecr.aws/docker/library/mariadb:11.8`
(11.8.9-MariaDB-ubu2404), UTC system clock.

**Syntax.**

```sql
CREATE TABLE acct (id INT PRIMARY KEY, bal INT) WITH SYSTEM VERSIONING
SELECT id, bal FROM acct FOR SYSTEM_TIME AS OF TIMESTAMP '2026-11-15T...' ORDER BY id
```

The `TIMESTAMP` keyword is optional (`AS OF '...'` also works). `ALL`,
`BETWEEN ... AND ...`, and `FROM ... TO ...` work. Expressions are allowed:
`FOR SYSTEM_TIME AS OF NOW() - INTERVAL 2 SECOND` works.

**Detection.**

```sql
SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = 'app'
```

`TABLE_TYPE` reads exactly `'SYSTEM VERSIONED'` for a versioned table,
including partitioned ones (whose `CREATE_OPTIONS` reads `partitioned`).
`SHOW FULL TABLES` still says `BASE TABLE` regardless; `SHOW CREATE TABLE`
ends with `WITH SYSTEM VERSIONING`.

**Privileges.** `SELECT ON app.*` is enough for AS OF, ALL, and `AS OF
TRANSACTION`. It passes `mysql.Check` then `mysql.Run` (`READ ONLY,
REPEATABLE READ` transaction, `EXPLAIN` first) and returns correct rows as
that reader.

**Oldest readable point and how dbscope bounds it.** There is no lower
error bound either: AS OF before the table existed, or before versioning
was added to it, returns 0 rows with no error. Unlike SQL Server, dropped
history is silent in a different way: dropping a history partition
(`ALTER TABLE ph DROP PARTITION p0`) or running `DELETE HISTORY ... BEFORE
SYSTEM_TIME` both make AS OF return 0 rows for that period with no error at
all. There is no catalog-level retention setting on a plain (non-partitioned)
system-versioned table to bound the picker against; the design has no
measured lower bound to clamp to beyond watching for 0-row results (not
measured: whether a zero-row result should be shown to the user as
"possibly pruned" versus "genuinely empty" is a design choice).

**Errors and what they mean.**

| Error | Meaning |
|---|---|
| `Error 4124 (HY000): Table '...' is not system-versioned` | AS OF was used on a plain table |
| `Error 4119: Not allowed for system-versioned '...'. Change @@system_versioning_alter_history to proceed with ALTER.` | an ALTER that needs `system_versioning_alter_history = KEEP` |
| `Error 4137: System-versioned tables do not support TRUNCATE TABLE` | TRUNCATE is blocked on a versioned table |
| `Error 4111: Transaction-precise system versioning for '...' is not supported` | `AS OF TRANSACTION` was used on a timestamp-versioned table |
| `Error 1305: FUNCTION app.TRT_COMMIT_ID does not exist` | `TRT_COMMIT_ID()` is not a real function (the test guessed wrong; `mysql.transaction_registry` is the real source) |
| `Error 1142: SELECT command denied ... for table mysql.transaction_registry` | the reader lacks SELECT on the registry table, though `AS OF TIMESTAMP` on a transaction-precise table still worked through it |

**Traps.** The AS OF literal is read in the session time zone, not a fixed
zone. A literal written for checkpoint A under UTC returned 0 rows when the
session time zone was changed to `+05:30` before running the same query
text. The design's literal is `FROM_UNIXTIME(epoch)`, which converts an
epoch value using the session's time zone rather than assuming the text is
already in it; this is the design's planned mitigation and was not itself
run as a fix in testing (only the failure mode with a raw UTC-written
literal under a different session zone was measured). A second trap is the
session variable `system_versioning_asof`: setting it turns every plain
SELECT into an AS OF read for that session, and it is refused by
`mysql.Check` on the `SET` keyword, but it still leaked across a pooled
connection into a later, unrelated `mysql.Run` call that returned old rows
with no AS OF clause in its text. This is why the design rewrites each table
reference with its own `FOR SYSTEM_TIME AS OF` clause rather than using the
session variable.

### MySQL

Measured on `mysql:8.4` (8.4.13).

MySQL 8.4 has no system versioning or temporal tables.
`FOR SYSTEM_TIME AS OF TIMESTAMP '2026-01-01 00:00:00'` on a plain table
gives:

```
Error 1064 (42000): You have an error in your SQL syntax; check the manual
that corresponds to your MySQL server version for the right syntax to use
near 'SYSTEM_TIME AS OF TIMESTAMP '2026-01-01 00:00:00'' at line 1
```

`FOR SYSTEM_TIME ALL`, Oracle-style `AS OF TIMESTAMP '...'`, and `CREATE
TABLE v (id INT) WITH SYSTEM VERSIONING` all give the same error 1064.
`information_schema.TABLES.TABLE_TYPE` only takes the values `BASE TABLE`,
`SYSTEM VIEW`, and `VIEW`; there is no `SYSTEM VERSIONED` value to detect.
`mysql.Check` passes the AS OF text through (its token rules do not
recognize it as unsafe), and `mysql.Run` fails with the same 1064. MySQL is
marked "no native time travel" in the picker; offering it would either error
on every query or, if the error were silently swallowed, show current data
labelled as history.

### MongoDB

Measured on `public.ecr.aws/docker/library/mongo:8.0` (8.0.34), as a
one-member replica set with a keyfile (auth on).

**Syntax.** A command-level readConcern, not a query-language clause:

```json
{"find": "acct", "filter": {}, "sort": {"_id": 1},
 "readConcern": {"level": "snapshot", "atClusterTime": {"$timestamp": {"t": 1799...,"i": 24}}}}
```

Confirmed to work outside a transaction: `find`, `aggregate` (including
`$lookup` and `$count` stages), `distinct`, and `getMore` after
`batchSize: 1`. A standalone server (no replica set) refuses the readConcern
entirely: `code 123 (NotAReplicaSet): node needs to be a replica set member
to use readConcern: snapshot`, and its write replies carry no
`operationTime` to build a timestamp from in the first place.

**Detection.** There is no per-collection catalog flag; whether snapshot
reads are possible at all depends on the deployment being a replica set
(standalone fails with code 123 above), which a connection-level check can
determine once, not per table.

**Privileges.** The `read` role on the target database is enough for
snapshot `find`, `aggregate`, and `distinct`. `getParameter` and
`setParameter` (used to read or change the history window) need more:
`code 13 (Unauthorized)` for a plain `read` role; `clusterMonitor` can run
`getParameter`. Through `mongo.Parse`, `mongo.Check`, then `mongo.Run` as a
`read`-role user: `find` and `aggregate` pass and return documents (the
explain step also works, with plan FETCH), `distinct` passes and its result
also carries `atClusterTime`, `$clusterTime`, and `operationTime` fields.

**Oldest readable point and how dbscope bounds it.** The server's history
window, `minSnapshotHistoryWindowInSeconds`
(`serverStatus.wiredTiger["snapshot-window-settings"]`), defaults to 300
seconds. The design clamps the picker at `now − minSnapshotHistoryWindowInSeconds`.
`Timestamp(1,0)` and a timestamp one hour old both gave `code 239
(SnapshotTooOld): Read timestamp ... is older than the oldest available
timestamp.` After lowering the setting to 5 seconds at runtime, an
8-second-old timestamp failed with 239 within 7 ms, while now−2s still
worked and now−30s failed, consistent with the shorter window taking effect
immediately.

**Errors and what they mean.**

| Error | Meaning |
|---|---|
| `code 123 (NotAReplicaSet)` | the deployment is standalone, not a replica set |
| `code 72 (InvalidOptions)` on `count` | `count` does not support readConcern snapshot at all; must be rewritten as `aggregate` with `$count` |
| `code 72`, atClusterTime with level majority/local | atClusterTime is only valid with level snapshot |
| `code 72`, atClusterTime in the future | atClusterTime cannot exceed the current cluster time |
| `code 14 (TypeMismatch)` | atClusterTime was a date value instead of a BSON Timestamp |
| `code 239 (SnapshotTooOld)` | the requested time is before the retained window |
| `code 2 (BadValue)` on a hint | the hinted index did not exist yet at that time |

**Traps.** `count` is the one command that passes `mongo.Check` and the
explain step, then fails only in `mongo.Run` with code 72; the design
rewrites a user's `count` into `aggregate` with a `$count` stage before it
reaches the guard, rather than letting it fail at run time. A dropped and
recreated collection returns its *old* documents when read at a time before
the drop, which is correct point-in-time behavior but can be surprising
since the same name now also has new data at the current time. A collection
created after the requested time, or one that never existed, returns an
empty result with no error at all rather than failing, which the UI needs
to distinguish from "the read worked but there was nothing there."

### ClickHouse

Measured on `clickhouse/clickhouse-server:26.8` (26.8.36.3) and `25.8`
(25.8.33.6). Both gave the same results.

There is no AS OF syntax for MergeTree tables. Every form tried failed with
the same parse error:

```sql
SELECT * FROM acct FOR SYSTEM_TIME AS OF '2026-11-15 ...'
-- code: 62, message: Syntax error: failed at position 24 (SYSTEM_TIME) ... Expected one of: FINAL, SAMPLE, ...
```

`FOR SYSTEM_TIME ALL`, Oracle-style `AS OF TIMESTAMP '...'`, `AS OF '...'`,
`TIMESTAMP AS OF '...'`, `VERSION AS OF 1`, and `AT (TIMESTAMP => '...')` all
gave the same code 62. The Iceberg-table settings `iceberg_timestamp_ms` and
`iceberg_snapshot_id` are accepted as query settings on a MergeTree table
but have no effect: the query still returned current rows, not historical
ones. A setting named `as_of_timestamp` does not exist (`code 115`, unknown
setting). No MergeTree engine setting matched `snapshot`, `history`, or
`versioning` by name. Superseded parts after a mutation stay inactive for
`old_parts_lifetime` (480 seconds measured), but no query interface was
found to read them as of a past time. Through `clickhouse.Run` (readonly
mode), setting `iceberg_timestamp_ms` on a MergeTree table failed with
`code: 164, message: Cannot modify 'iceberg_timestamp_ms' setting in
readonly mode`; `clickhouse.Check` separately refuses the `iceberg()` table
function outright.

ClickHouse is marked "no native time travel" for MergeTree tables in the
picker, for the same reason as MySQL: the Iceberg settings are accepted but
ineffective on MergeTree, so offering them would show current data labelled
as history.

## Engines without native history

These are the other engines dbscope supports (or researched for context)
that have no query-level AS OF read. Their closest options all require a
restore, a background process, or an operator-managed log, none of which is
a query dbscope's read-only guard could run. Citations are from
`others/result.md`'s docs survey.

| Engine | Native as-of | Closest option | Retention | Citation |
|---|---|---|---|---|
| SQLite | Partial, C API only | `sqlite3_snapshot_open` on a snapshot handle already held; needs a handle captured earlier, so no past timestamp can be chosen | until a checkpoint overwrites it | [snapshot_open](https://www.sqlite.org/c3ref/snapshot_open.html) |
| Redis / Valkey | No | RDB/AOF; AOF timestamp annotations for PITR restore | size of the AOF history | [redis.conf](https://raw.githubusercontent.com/redis/redis/unstable/redis.conf), [valkey.conf](https://raw.githubusercontent.com/valkey-io/valkey/unstable/valkey.conf) |
| Cassandra 5 | No | snapshots, incremental backups, CDC | operator-managed | [backups](https://cassandra.apache.org/doc/stable/cassandra/managing/operating/backups.html), [CDC](https://cassandra.apache.org/doc/latest/cassandra/managing/operating/cdc.html) |
| ScyllaDB | No | CDC log table with optional pre-images and post-images | `ttl` default 86400 s | [cdc-intro](https://docs.scylladb.com/manual/stable/features/cdc/cdc-intro.html) |
| DynamoDB | No | PITR restores into a new table; Streams keep before/after images | PITR 1–35 days; Streams 24 h | [PITR](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Point-in-time-recovery.html), [Streams](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.html) |
| Elasticsearch / OpenSearch | No | point in time (PIT) freezes segments from when it is opened, going forward only | `keep_alive` | [ES PIT](https://www.elastic.co/guide/en/elasticsearch/reference/8.19/point-in-time-api.html), [OS PIT](https://docs.opensearch.org/latest/search-plugins/searching-data/point-in-time/) |
| Neo4j | No | CDC from an enriched transaction log (`db.cdc.query`) | follows transaction log retention (source to be checked) | [Neo4j CDC](https://neo4j.com/docs/cdc/current/get-started/self-managed/) |
| Memgraph | No | snapshots and WAL, for recovery only | count of retained snapshots | [durability](https://memgraph.com/docs/fundamentals/data-durability) |
| Apache AGE | No | same as PostgreSQL, since it is a PostgreSQL extension | same as PostgreSQL | [age.apache.org](https://age.apache.org/) |

As reference points for what a purpose-built "native" AS OF looks like in
engines dbscope does not support: CockroachDB's `AS OF SYSTEM TIME` (default
garbage-collection window `gc.ttlseconds` 14400 s) and YugabyteDB's `SET
yb_read_time` (default retention 900 s) both work at the query or session
level without a restore
([AS OF SYSTEM TIME](https://docs.cockroachlabs.com/docs/stable/as-of-system-time),
[Inspect at PIT](https://docs.yugabyte.com/stable/manage/backup-restore/point-in-time-recovery/inspect/)).

## PostgreSQL

Native query-level AS OF is out of scope for PostgreSQL.

There is no query-level historical read in PostgreSQL core; the
`timetravel` contrib extension that once offered something adjacent was
removed in PostgreSQL 12. The only built-in way to see data as it stood at
a past moment is Point-in-Time Recovery (PITR): restore a base backup and
replay WAL up to a `recovery_target_time`
([continuous-archiving](https://www.postgresql.org/docs/current/continuous-archiving.html)).
That rebuilds an entire cluster; it is not something dbscope's read-only
query path could run as part of answering a query.

The closest thing to a live, in-place history source is logical decoding,
and that is also out of scope, for an operational reason rather than a
missing feature: logical decoding needs `wal_level = logical`
([runtime-config-wal.html](https://www.postgresql.org/docs/current/runtime-config-wal.html))
and a role with the `REPLICATION` attribute (or superuser) to create a
replication slot
([sql-createrole.html](https://www.postgresql.org/docs/current/sql-createrole.html),
[logicaldecoding-explanation.html](https://www.postgresql.org/docs/current/logicaldecoding-explanation.html)).
A replication slot is not a view of history; it is a server-side marker
that pins WAL for anyone to read from later. If the consumer of that slot
goes away, the server does not reclaim the WAL: the slot keeps every WAL
segment since its creation on disk until something advances it
([warm-standby.html](https://www.postgresql.org/docs/current/warm-standby.html)).
This is exactly the risk dbscope must not create: a user closes their
laptop with a dbscope connection open, the slot keeps accumulating WAL, and
a production primary's disk fills. dbscope's own Health tab already treats
an inactive replication slot as a danger-level check for this reason
(see [HEALTH.md](HEALTH.md)); Time should not be the thing that creates one.

### Safe variants considered

| Variant | Gives an as-of read? | Needs on the server | What can go wrong |
|---|---|---|---|
| (a) A temporary replication slot that dies with the session | Not on its own: a logical slot streams a change feed from slot creation forward, it does not let you ask for an arbitrary past timestamp. A temporary slot avoids leaking WAL retention past the session (`pg_create_logical_replication_slot(..., temporary := true)` or the `CREATE_REPLICATION_SLOT ... TEMPORARY` protocol option; functions documented at [functions-admin.html](https://www.postgresql.org/docs/current/functions-admin.html), protocol option at source to be checked) | Still needs `wal_level=logical` and the `REPLICATION` role on the server, a bigger ask than a read-only user; and it answers "what changed," not "what did this table look like at time T" |
| (b) Reading `pg_wal` with a `pg_waldump`-style tool | Partial, and only forward from whenever WAL retention happens to start: `pg_waldump` ([app-pgwaldump.html](https://www.postgresql.org/docs/current/app-pgwaldump.html)) decodes WAL records, which is not the same as reconstructing a consistent row image at a timestamp | Needs filesystem access to the WAL directory, which an application-level SQL role does not have, and typically superuser or OS-level access | Reconstructing a table's state from raw WAL is building a second, unverified replay engine inside dbscope; wrong offsets or record parsing would show wrong data with no server-side check on it |
| (c) `track_commit_timestamp` | No: it gives the commit time of a transaction via `pg_xact_commit_timestamp(xid)`, not the data as it stood at a timestamp. It could date a row version if full row history already existed, which PostgreSQL does not keep | Needs the setting on and a server restart ([runtime-config-replication.html](https://www.postgresql.org/docs/current/runtime-config-replication.html)) | By itself it answers "when was this committed," never "what did this look like then"; it would need to be paired with something that actually keeps old row versions |
| (d) Extensions (`pg_dirtyread`, `temporal_tables`, `periods`) | `pg_dirtyread` reads dead-but-not-yet-vacuumed tuples directly; it can show recently deleted/updated rows but not a chosen arbitrary timestamp, and only until vacuum reclaims them. `temporal_tables`/`periods` give true AS OF reads, but only for rows written *after* the extension and its triggers were installed on that table (source to be checked for all three; none of these are PostgreSQL core and no postgresql.org/docs URL covers them) | `pg_dirtyread` needs superuser and reads physical heap internals; the trigger-based extensions need the extension installed and triggers added to every table ahead of time, which is a schema change dbscope cannot make unasked | `pg_dirtyread` can return tuples that were never actually committed (e.g. from a rolled-back transaction) since it does not check visibility; the trigger-based extensions do nothing for a table set up before they existed, so they cannot retroactively answer "what did this look like last month" |
| (e) Asking users to keep their own history tables | Yes, if the user already has one, in whatever shape they built it | Nothing from dbscope beyond reading an existing table | Depends entirely on the user having set this up already, with no dbscope involvement in making it correct or complete; dbscope would just be querying a table with a name convention it can detect |

None of these is safe enough to build as native AS OF for PostgreSQL in the
sense the other four engines have it. (a) still needs `wal_level=logical`
and the `REPLICATION` role, which is the same operational ask the whole
design avoids, for an answer that is not actually a point-in-time read. (b)
needs filesystem access no SQL role has and asks dbscope to reimplement WAL
replay. (c) does not keep old data at all. (d) either cannot target an
arbitrary past timestamp (`pg_dirtyread`) or only works for tables set up in
advance (`temporal_tables`/`periods`), and the latter is a schema change
dbscope should not make on a user's behalf. (e) is the only variant that is
unconditionally safe, because it adds nothing new to the server; it is also
not a feature dbscope builds, just a table dbscope could read if a user
already has one. PostgreSQL stays out of scope for native AS OF.

## Read-only safety

Time must never weaken the read-only guard. The rewrite that adds an
engine's AS OF clause to a query happens before `Check` runs, inside
`execute()` in `internal/agent/query_handlers.go`, which calls
`rewriteAsOf` and then the unchanged per-engine branches (built; see "AS OF
queries (built)"). `Check` also runs on the text as written, before the
rewrite. `TestAsOfKeepsGuard` (in `internal/agent` and each engine package)
checks that DELETE, UPDATE, INSERT ... SELECT, SELECT ... FOR UPDATE,
SELECT INTO and multi-statement text get the same refusal with `asOf` as
without. `Check` and `Run` are unchanged by Time: `Check` sees the
exact text that will run, including the AS OF clause, and judges it the same
way it judges any other query. Rewriting inside `Run` instead (after
`Check`) was considered and rejected, because `Check` would then be
inspecting different text than what actually executes, which is the one
thing the four-layer guard in [QUERY-SAFETY.md](QUERY-SAFETY.md) and this
project's per-engine `Check`/`Run` split both depend on not happening.

Measured, the native AS OF clauses already pass each engine's existing
`Check` unmodified, reasoned from the token rules (not independently
re-verified beyond what the test runs above exercised): Oracle's `Check`
(`internal/oracle/check.go:113`) allows a single SELECT/WITH statement and
only refuses the token `flashback` and non-safe `dbms_` packages, and `AS OF
TIMESTAMP`/`AS OF SCN` use only `as`, `of`, `timestamp`, and `scn`, none of
which are refused. SQL Server's `Check` (`internal/mssql/check.go:111`)
refuses `SET`, `DECLARE`, and a handful of other keywords, but not `FOR`,
`SYSTEM_TIME`, `AS`, or `OF`. MariaDB's `Check`
(`internal/mysql/check.go:117`) requires the statement to start with
`select`/`with`/etc. and refuses a short list of side-effecting tokens;
`FOR SYSTEM_TIME AS OF` is not on that list. MongoDB's `Check` passes `find`,
`aggregate`, and `distinct` with a `readConcern` object already, as
measured above; only `count` needs rewriting to `aggregate` with `$count`
before it reaches `Check`; the measurements above show `count` otherwise
getting all the way to `Run` before failing.

Two session-level mechanisms were measured and are deliberately not used,
because they would weaken or bypass the guard rather than work alongside it:

- **Oracle's `DBMS_FLASHBACK.ENABLE_AT_TIME`.** It breaks `oracle.Run`
  outright, since `SET TRANSACTION READ ONLY` and `EXPLAIN PLAN` both fail
  with `ORA-08182` once flashback mode is on, so the existing plan-gate and
  read-only-transaction layers stop working entirely. It is also a second
  path to historical data that does not require the per-table FLASHBACK
  privilege (only `EXECUTE` on the package), which would undercut the
  privilege check Time relies on. `oracle.Check` already refuses the token
  `flashback`, so this path is blocked before Time is even involved.
- **MariaDB's `system_versioning_asof` session variable.** Measured to leak
  across a pooled connection: setting it in one `mysql.Run` call left it
  active for a later, unrelated call on the same connection, silently
  turning a plain SELECT into a historical read with no AS OF clause visible
  in that later query's text. `mysql.Check` already refuses `SET`. Time
  rewrites the per-table `FOR SYSTEM_TIME AS OF` clause into each query's
  text instead, which carries no state past the single query.

Two implementation details follow from the existing code, not from the
measurements; both are now handled:

- The query request's JSON decoder rejects unknown fields
  (`internal/agent/server.go:180`), so `asOf` was added to `queryBody`
  (`internal/agent/query_handlers.go`).
- `Capabilities` are static per engine (`internal/conn/profile.go:67`), with
  no per-table flag, and MySQL and MariaDB share one `Engine` value even
  though only MariaDB has system versioning. A connection-level "does this
  engine/version support AS OF" capability is enough for MySQL vs. MariaDB
  (that distinction needs runtime detection, e.g. via `Describe`'s flavor,
  not a static capability), but a per-table "is this specific table
  versioned" signal for SQL Server and MariaDB has to come from
  introspection or from the request itself, not from `Capabilities`.
  Introspection now provides it; see "Per-table detection (built)".
