# Research: MySQL and MariaDB support

Collected 2026-10-28 by a research worker from primary sources (cited inline); nothing was run against a server during research, and claims it could not verify are marked "unverified". The design dbscope adopted is in docs/ENGINES.md.

Version scope changed since the task was written:
- **MySQL:** 9.7 is an LTS release, not a 9.x innovation release, and it is the last release on the old numbering. Newer innovation releases use calendar versions (26.7, 26.10). The matrix uses the 8.0, 8.4 and 9.7 manuals. I did not check the 26.x manual.
- **MariaDB:** the newest stable release is 13.0.2, and 12.3 is the newest LTS. The mariadb.com documentation now covers the current release only, with version notes inline.

Findings that affect the dbscope design:
- **Both candidate parsers fail on MariaDB's `ALGORITHM=NOCOPY`.** Neither vitess nor the TiDB parser has a rule for it (§7).
- **The same `ALGORITHM=INPLACE` clause means different things on the two servers.** MySQL forces INPLACE, so `ADD COLUMN ... ALGORITHM=INPLACE` rebuilds the table. MariaDB treats it as a minimum and may run INSTANT instead (§3).
- **On MariaDB 11.2 and later, `LOCK=NONE` no longer means "no table copy"** (§1, §3).
- **Every MySQL ALTER, including INSTANT, briefly needs an exclusive metadata lock.** The default metadata-lock wait (`lock_wait_timeout`) is one year (§2).


# MySQL and MariaDB support for dbscope: research

Citations are short labels in brackets, for example [odl84]. Each label maps to a URL in the Sources list near the end. "Unverified" means I found no primary source this session.

**Versions in scope**
- MySQL 9.7 is an LTS line and the last one with sequential version numbers. Later releases use YY.M numbers [releases].
- The October 2026 releases are 26.10 Innovation, 9.7.4 LTS and 8.4.13 LTS [oct26].
- The dev.mysql.com URL for 9.6 now serves the 9.7 manual. I used the 9.7 manual as the newest 9.x source. I did not check the 26.x manual.
- MariaDB release series listed on mariadb.org [mdb-releases]:
  - 13.2.0 Preview (2026-09-23)
  - 13.1.1 RC (2026-09-15)
  - 13.0.2 Stable (2026-09-15)
  - 12.3, the LTS line from May 2026 [mdb-lts]
- The mariadb.com docs now describe only the current release, with version notes inline.

## 1. Online DDL matrix for InnoDB

Column meanings:
- **MySQL algorithms**: the algorithms the 8.4 and 9.7 manuals list as supported. The two manuals' tables match except for the row-version limit [odl84] [odl97].
- **Rebuild**, **DML** (concurrent DML permitted) and **Meta only** (only modifies metadata) are the manual's own Yes/No values. They describe the best supported algorithm unless a note says otherwise.

| Operation | MySQL algorithms | Rebuild | DML | Meta only | MySQL notes | MariaDB | Source |
|---|---|---|---|---|---|---|---|
| ADD COLUMN | INSTANT (default since 8.0.12), INPLACE | No with INSTANT; Yes with INPLACE | Yes | Yes | Any position from 8.0.29; before that, last position only. Not allowed for ROW_FORMAT=COMPRESSED, tables with a FULLTEXT index, data-dictionary tablespace tables, or TEMPORARY tables (COPY only). Can't be combined with non-INSTANT actions. | INSTANT at any position. Not supported if the table has a hidden FTS_DOC_ID. Leaves a "non-canonical" storage format. 10.3 added INSTANT ADD COLUMN; 10.4 added more INSTANT operations. | [odl80] [odl84] [mdb-instant] [mdb-104] |
| ADD AUTO_INCREMENT column | INPLACE with LOCK=SHARED at minimum | Yes | **No** | No | "Data is reorganized substantially" | Exception to LOCK=NONE. 11.2+ online COPY can add AUTO_INCREMENT to an existing column, with limitations. | [odl84] [mdb-instant] [mdb-112] |
| DROP COLUMN | INSTANT (default from 8.0.29), INPLACE | Yes with INPLACE | Yes | Yes with INSTANT | Same table restrictions as ADD. A column with a functional index can't be dropped INSTANT. | INSTANT (10.4+) | [odl80] [odl84] [mdb-instant] [mdb-104] |
| RENAME COLUMN, same type | INSTANT (8.0.28+), INPLACE | No | Yes | Yes | If another table's FK references the column, only INPLACE works; INSTANT or COPY fails. Renaming while adding or dropping a virtual column needs COPY. | INSTANT, unless the type or attributes also change | [odl80] [mdb-instant] |
| Reorder column (FIRST/AFTER) | INPLACE | Yes | Yes | No | No INSTANT | INSTANT (10.4+) | [odl84] [mdb-instant] [mdb-104] |
| Change data type | COPY only | Yes | **No** | No | | INPLACE, NOCOPY and INSTANT fail with error 1846. On 11.2+, COPY can run with LOCK=NONE. | [odl84] [mdb-overview] [mdb-112] |
| Widen VARCHAR within the same length-byte class (0–255 bytes to ≤255, or ≥256 to larger) | INPLACE | No | Yes | Yes | Length is counted in bytes, so it depends on the character set | INSTANT | [odl84] [mdb-instant] |
| Widen VARCHAR across the 255/256-byte boundary | COPY only | Yes | No | No | Error 0A000 if INPLACE is requested | COPY, except INSTANT for ROW_FORMAT=REDUNDANT, or for COMPACT, DYNAMIC or COMPRESSED when the original length is ≤127 bytes. 128–255 bytes to ≥256 is not INSTANT. | [odl84] [mdb-instant] |
| Shrink VARCHAR | COPY only | Yes | No | No | | COPY (unverified for MariaDB) | [odl84] |
| Make column NULL | INPLACE | Yes | Yes | No | | INSTANT only for ROW_FORMAT=REDUNDANT; otherwise INPLACE with rebuild | [odl84] [mdb-instant] [mdb-inplace] |
| Make column NOT NULL | INPLACE | Yes | Yes | No | Needs strict SQL_MODE. Fails if the column has NULLs. FK-integrity checks apply. | INPLACE with rebuild and strict mode. Not INSTANT or NOCOPY. | [odl84] [mdb-inplace] [mdb-nocopy] |
| SET DEFAULT / DROP DEFAULT | INSTANT, INPLACE | No | Yes | Yes | Defaults are stored in the data dictionary | INSTANT | [odl84] [mdb-instant] |
| AUTO_INCREMENT=N | INPLACE | No | Yes | No* | Changes a value in memory, not the data file | INSTANT | [odl84] [mdb-instant] |
| ENUM/SET: append member at the end, storage size unchanged | INSTANT, INPLACE | No | Yes | Yes | Mid-list insert or a storage-size change (SET with 8 members going to 9) needs COPY | Same rule. INSTANT. | [odl84] [mdb-instant] |
| Add secondary index | INPLACE | No | Yes | No | Finishes only after transactions accessing the table complete | NOCOPY, LOCK=NONE | [odl84] [mdb-nocopy] |
| Drop index | INPLACE | No | Yes | Yes | | INPLACE is metadata only. NOCOPY works "where INSTANT does". The INSTANT details for DROP INDEX are unverified. | [odl84] [mdb-inplace] [mdb-nocopy] |
| Rename index | INPLACE | No | Yes | Yes | | Unverified | [odl84] |
| Change index type (drop and re-add with USING) | INSTANT, INPLACE | No | Yes | Yes | | Unverified | [odl84] |
| Add FULLTEXT index | INPLACE* | No* (the first one rebuilds unless the table has FTS_DOC_ID) | **No** | No | | First one is INPLACE with rebuild; later ones NOCOPY. LOCK=SHARED. Only one per statement. A table with FULLTEXT can't be rebuilt with LOCK=NONE. | [odl84] [mdb-nocopy] [mdb-inplace] |
| Add SPATIAL index | INPLACE (example uses LOCK=SHARED) | No | **No** | No | | NOCOPY, LOCK=SHARED. A table with SPATIAL can't be rebuilt with LOCK=NONE. | [odl84] [mdb-nocopy] [mdb-inplace] |
| Add PRIMARY KEY | INPLACE* | Yes | Yes | No | INPLACE needs strict mode. COPY silently converts NULLs to type defaults. | INPLACE with rebuild, LOCK=NONE | [odl84] [mdb-inplace] |
| Drop PRIMARY KEY alone | COPY only | Yes | **No** | No | | COPY. 11.2+ online COPY can do it with DML. | [odl84] [mdb-inplace] [mdb-112] |
| Drop PK and add PK in one statement | INPLACE | Yes | Yes | No | | INPLACE, LOCK=NONE | [odl84] [mdb-inplace] |
| Add FK, foreign_key_checks=1 | COPY only | Yes (COPY) | No | No | | COPY required | [odl84] [mdb-nocopy] |
| Add FK, foreign_key_checks=0 | INPLACE | No | Yes | Yes | | NOCOPY or INPLACE, metadata only, LOCK=NONE. Not INSTANT. | [odl84] [mdb-nocopy] [mdb-instant] |
| Drop FK (either checks setting) | INPLACE | No | Yes | Yes | | INSTANT | [odl84] [mdb-instant] |
| Table default charset (CHARACTER SET =) | INPLACE | Yes* (if the encoding differs) | Yes | No | | See next row | [odl84] |
| CONVERT TO CHARACTER SET | INPLACE | Yes* (if the encoding differs) | **No** | No | | 10.4 made some conversions INSTANT, notably utf8mb3 to utf8mb4. A collation change on indexed columns rebuilds those indexes but can be NOCOPY. | [odl84] [mdb-104] |
| ROW_FORMAT change | INPLACE | Yes | Yes | No | | INPLACE with rebuild, LOCK=NONE | [odl84] [mdb-inplace] |
| KEY_BLOCK_SIZE change | INPLACE | Yes | Yes | No | | INPLACE with rebuild | [odl84] [mdb-inplace] |
| STATS_PERSISTENT / STATS_* | INPLACE | No | Yes | Yes | | Unverified | [odl84] |
| OPTIMIZE TABLE | INPLACE*; ALGORITHM/LOCK syntax not allowed | Yes | Yes | No | Not INPLACE when the table has FULLTEXT | Same as ALTER TABLE FORCE (INPLACE rebuild) unless innodb_optimize_fulltext_only is ON. From 11.5 it ignores alter_algorithm. | [odl84] [mdb-inplace] |
| ALTER TABLE ... FORCE / ENGINE=InnoDB | INPLACE* | Yes | Yes | No | Not INPLACE with FULLTEXT | INPLACE with rebuild, LOCK=NONE | [odl84] [mdb-inplace] |
| RENAME TABLE | INSTANT, INPLACE | No | Yes | Yes | Privileges are not migrated to the new name | INSTANT, but LOCK=EXCLUSIVE (no concurrent DML) | [odl84] [mdb-instant] |
| Add STORED generated column | COPY only | Yes | No | No | | 11.2+ online COPY | [odl84] [mdb-112] |
| Drop STORED generated column | INPLACE | Yes | Yes | No | | Unverified | [odl84] |
| Add or drop VIRTUAL generated column | INSTANT, INPLACE (non-partitioned tables only) | No | Yes | Yes | Not INPLACE on partitioned tables | Unverified | [odl84] |
| Reorder STORED/VIRTUAL generated column | COPY | Yes | No | No | | Unverified | [odl84] |
| File-per-table ENCRYPTION change | COPY | Yes | No | No | | Unverified | [odl84] |
| PARTITION BY / REMOVE PARTITIONING | COPY | Yes | No | n/a | | 11.2+ online COPY can add or remove partitioning | [odl84] [mdb-112] |
| ADD PARTITION | INPLACE* | n/a | Yes* | n/a | RANGE/LIST allow LOCK=NONE; HASH/KEY do not. Existing data is not copied for RANGE/LIST. | Unverified | [odl84] |
| DROP PARTITION | INPLACE | n/a | Yes | n/a | **Deletes the partition's data** with INPLACE. COPY tries to move rows to another partition. | Unverified | [odl84] |
| TRUNCATE / EXCHANGE / ANALYZE / CHECK / REPAIR PARTITION | INPLACE | n/a | Yes | n/a | | Unverified | [odl84] |
| COALESCE / REORGANIZE / REBUILD PARTITION | INPLACE (LOCK SHARED at most) | n/a | No | n/a | | Unverified | [odl84] |
| OPTIMIZE PARTITION | none (ALGORITHM/LOCK ignored) | Rebuilds the entire table | No | n/a | | Unverified | [odl84] |
| DISCARD / IMPORT PARTITION | DEFAULT only | n/a | No | n/a | | Unverified | [odl84] |

**Version notes**
- **MySQL 8.0.12:** INSTANT became the default for ADD COLUMN [odl80].
- **MySQL 8.0.28:** RENAME COLUMN gained INSTANT [odl80].
- **MySQL 8.0.29:** INSTANT ADD COLUMN works at any position and INSTANT DROP COLUMN became the default. The server now checks row size on INSTANT add (error 4092). System-schema tables were excluded from INSTANT [odl80].
- **Row-version limit:** each INSTANT add or drop creates a row version, tracked in `INFORMATION_SCHEMA.INNODB_TABLES.TOTAL_ROW_VERSIONS`. The limit is 64 in 8.0 [odl80]. The 8.4 page says "64 (255 as of MySQL 9.1.0)" [odl84], and the 9.7 page says 255 [odl97]. At the limit, INSTANT ADD and DROP fail with error 4092. A rebuilding ALTER or OPTIMIZE resets the count to 0.
- **Column cap:** the internal representation is capped at 1022 columns after INSTANT add (error 4158) [odl84].
- **MariaDB 10.3** introduced INSTANT ADD COLUMN and the INSTANT and NOCOPY keywords. **10.4** added INSTANT DROP and reorder, relaxed VARCHAR extension, and some instant charset conversions [mdb-104].
- **MariaDB 11.2** added LOCK=NONE support to ALGORITHM=COPY through an "online change buffer". It takes a short exclusive lock at the end. Tables whose foreign keys use cascade actions are not supported [mdb-112].
- **MariaDB 11.5** ignores the `alter_algorithm` variable and returns warning 4200 when it is set (MDEV-33655) [mdb-overview].
- **MariaDB PAGE_COMPRESSED:** setting PAGE_COMPRESSED=1 is no longer INSTANT or NOCOPY from Community 10.6.26, 10.11.17, 11.4.11, 11.8.7 and 12.3.2 (MDEV-38079) [mdb-instant].
- **MariaDB non-canonical format:** INSTANT add, drop and reorder leave a format that "has the potential to incur some performance or storage overhead" for later DML. `ALTER TABLE ... FORCE` normalizes it [mdb-instant].

## 2. Metadata locks

**MySQL**
- **Three phases.** Initialization takes a shared upgradeable MDL. Execution may upgrade to exclusive briefly during preparation. The commit-table-definition phase always upgrades to exclusive [perf84].
- **INSTANT is not exempt.** INSTANT "may" take an exclusive MDL briefly during execution [alter84].
- **Long transactions block the ALTER.** A transaction holds MDL on every table it used until it ends. This applies to plain SELECTs and to non-transactional tables [mdl84]. In autocommit mode, locks last to the end of the statement [mdl84].
- **The ALTER blocks everyone behind it.** "a pending exclusive metadata lock requested by an online DDL operation blocks subsequent transactions on the table". The manual's example: an open transaction's SELECT, then an ALTER with LOCK=NONE waits, then a new SELECT also waits ("Waiting for table metadata lock") [perf84].
- **Why the queue forms.** Write-lock requests have higher priority than read-lock requests, subject to `max_write_lock_count`, whose default is very large [mdl84].
- **FKs widen the lock.** MDL extends to tables related by a foreign key [mdl84].
- **COPY blocks reads at the end.** A COPY ALTER takes an exclusive lock at the end and blocks reads as well as writes [alter84].
- **Diagnosis.** Use `SHOW FULL PROCESSLIST` and `performance_schema.metadata_locks` [perf84].

**Timeouts**
- `lock_wait_timeout` governs MDL waits. Default 31536000 s (1 year), range 1 to 31536000. It applies to each lock attempt separately, so one statement can wait longer than the value. Scope is global and session, and the SET_VAR hint is allowed [sysvar84].
- `innodb_lock_wait_timeout` is for InnoDB row locks only. Default 50 s. It "does not apply to waits for table locks". On timeout only the statement rolls back, unless `--innodb-rollback-on-timeout` is set [innodbvar84].

**MariaDB**
- Same model. A transaction locks the metadata of the tables it uses until it ends. DDL against a locked table "is queued". Savepoint rollback does not release MDL. The default `lock_wait_timeout` is also 31536000. Timeout gives `ERROR 1205` [mdb-mdl].
- **WAIT/NOWAIT.** `ALTER TABLE tbl [WAIT n|NOWAIT]` sets the timeout for one statement. `WAIT 0` equals NOWAIT. The same syntax exists for CREATE/DROP INDEX, DROP/RENAME/TRUNCATE/OPTIMIZE TABLE, LOCK TABLE, and SELECT ... FOR UPDATE / LOCK IN SHARE MODE [mdb-wait].
- **ALTER ONLINE TABLE** is equivalent to LOCK=NONE [mdb-overview].
- **Locking strategy doesn't avoid MDL.** Whatever the strategy, InnoDB must lock the table exclusively "for a short time at the start and end", so active transactions must finish first [mdb-overview].

## 3. Explicit ALGORITHM and LOCK when the request can't be honoured

**MySQL**
- **No clause:** MySQL tries INSTANT, then INPLACE, then COPY, without warning [alter84].
- **Explicit ALGORITHM:** "requires the operation to use the specified algorithm ... or fail with an error otherwise". Example: `ERROR 0A000: ALGORITHM=INPLACE is not supported. Reason: ... Try ALGORITHM=COPY` [alter84] [odl84].
- **The algorithm is forced, not a floor.** Per [odl84], "The table is rebuilt if ALGORITHM=INPLACE is used to add a column".
- **LOCK too weak:** if LOCK asks for less locking than the operation allows, "the statement fails with an error". Omitting LOCK equals LOCK=DEFAULT, the least locking possible [perf84].
- **old_alter_table** forces COPY [perf84].

**MariaDB**
- **COPY is exact.** Every other value is "the least efficient algorithm that the user is willing to accept", ordered COPY < INPLACE < NOCOPY < INSTANT. So `ALGORITHM=INPLACE` on ADD COLUMN may run INSTANT [mdb-overview].
- **Unsupported requests error.** Error 1846 gives a reason and a suggested algorithm; 1845 says "not supported for this operation" [mdb-overview] [mdb-instant].
- **LOCK=NONE or SHARED that isn't permitted** raises an error [mdb-overview].
- **From 11.2,** COPY supports LOCK=NONE. When InnoDB can't do the change natively, "if possible, a COPY algorithm with Online schema change will be applied" [mdb-112].
- **Consequence for dbscope:** on MariaDB 11.2+, `LOCK=NONE` succeeding does not mean "no table copy".

## 4. Online schema change tools

**gh-ost**
- Latest release v1.1.11, 2026-08-17 [ghost-rel].
- Triggerless: it reads the binlog stream [ghost-readme].
- Requires MySQL 5.7+, a server producing ROW binlogs with FULL row image, and `--assume-rbr` or `--switch-to-rbr` [ghost-req].
- Uses REPEATABLE READ on all its connections [ghost-req].
- The original and new tables must share a PRIMARY or UNIQUE key with no NULLs [ghost-req].
- **Not supported:** foreign keys, triggers, encrypted binlogs, active-active masters, `RENAME TO` [ghost-req].
- MariaDB support is not stated in the README or the requirements doc. **Unverified.**

**pt-online-schema-change**
- Copies rows in chunks and uses triggers to mirror changes, then an atomic RENAME. It "will not work if any triggers are already defined on the table" [ptosc].
- Needs a PRIMARY KEY or UNIQUE INDEX in almost all cases [ptosc].
- Refuses to run if foreign keys reference the table, unless `--alter-foreign-keys-method` is set:
  - `rebuild_constraints` re-creates child FKs, renamed with a leading underscore.
  - `drop_swap` sets FOREIGN_KEY_CHECKS=0 and drops before the rename, so the swap is not atomic.
  - `auto` picks between them [ptosc].
- Sets `innodb_lock_wait_timeout=1` and `lock_wait_timeout=60` [ptosc].
- Pauses on replica lag (`--max-lag`) and on load (`--max-load`, `--critical-load`) [ptosc].
- Ignores GENERATED columns [ptosc].
- I did not check the current Percona Toolkit version, `--preserve-triggers`, or MariaDB support. **Unverified.**

**When dbscope should recommend which tool** (a recommendation built from the facts above, not a sourced rule)
- **Recommend an OSC tool** when the best native path is COPY or a rebuilding INPLACE on a large table.
- **Use gh-ost** when ROW binlogs are available and the table has no FKs and no triggers.
- **Use pt-osc** when the table has FKs (with an explicit method) or binlogs aren't ROW, as long as the table has no triggers.
- **Neither tool** if the table has both FKs and triggers. Fall back to native DDL in a maintenance window.

## 5. Introspection for the ERD

**MySQL tables and columns**
- **TABLES:** `TABLE_ROWS` for InnoDB "may vary from the actual value by as much as 40% to 50%" and is "only a rough estimate". It is NULL for INFORMATION_SCHEMA tables. Views have mostly NULL or 0 values [is-tables84].
- **Stats cache:** TABLES.TABLE_ROWS, DATA_LENGTH, INDEX_LENGTH, AUTO_INCREMENT and others, plus STATISTICS.CARDINALITY, come from a cache that expires after `information_schema_stats_expiry`. The default is 86400 s. Set it to 0 to read the storage engine live, or run ANALYZE TABLE. In read_only, super_read_only, transaction_read_only or innodb_read_only modes, queries don't update the cache [sysvar84] [is-stats84].
- **COLUMNS:**
  - `EXTRA` holds `auto_increment`, `on update CURRENT_TIMESTAMP`, `STORED GENERATED` or `VIRTUAL GENERATED`, `DEFAULT_GENERATED` [is-columns84], and `INVISIBLE` [invcol84].
  - `GENERATION_EXPRESSION` holds the generated-column expression.
  - `COLUMN_DEFAULT` is NULL both for an explicit DEFAULT NULL and for no default.
  - Generated invisible primary keys are shown unless `show_gipk_in_create_table_and_information_schema=OFF` [is-columns84].
- **STATISTICS:**
  - `IS_VISIBLE` flags invisible indexes.
  - For functional key parts, `COLUMN_NAME` is NULL and `EXPRESSION` holds the expression.
  - `SUB_PART` is the prefix length, measured in bytes [is-stats84].
- **KEY_COLUMN_USAGE:** has `REFERENCED_TABLE_SCHEMA`, `REFERENCED_TABLE_NAME`, `REFERENCED_COLUMN_NAME` and `POSITION_IN_UNIQUE_CONSTRAINT` for FKs [is-kcu84].
- **REFERENTIAL_CONSTRAINTS:** has `UPDATE_RULE`, `DELETE_RULE`, `UNIQUE_CONSTRAINT_NAME`, `TABLE_NAME` and `REFERENCED_TABLE_NAME` [is-rc84].
- **CHECK constraints:**
  - `CHECK_CONSTRAINTS` has no table-name column (schema, name and CHECK_CLAUSE only) [is-check84].
  - Join `TABLE_CONSTRAINTS` (`CONSTRAINT_TYPE='CHECK'`), which also has `ENFORCED` [is-tc84].
  - CHECK constraints are enforced only from 8.0.16; before that they were parsed and ignored [cc80].
- **VIEWS:** I did not fetch the VIEWS page. **Unverified.**

**MariaDB differences**
- **CHECK_CONSTRAINTS** includes `TABLE_NAME` and `LEVEL` ('Column' or 'Table') [mdb-is-check].
- **COLUMNS:**
  - Has `IS_GENERATED`, `IS_SYSTEM_TIME_PERIOD_START` and `_END`. `CREATE_OPTIONS` exists from 13.0 [mdb-is-columns].
  - `COLUMN_DEFAULT` quotes literals to tell them apart from expressions. SQL NULL means no default, and the string `NULL` means DEFAULT NULL [mdb-is-columns].
  - `EXTRA` shows `INVISIBLE` [mdb-is-columns] [mdb-invcol].
- **STATISTICS** has `IGNORED` (10.6+ "ignored indexes", MariaDB's equivalent of invisible indexes) rather than `IS_VISIBLE`. A primary key can't be ignored [mdb-is-stats] [mdb-ignored].
- **TABLES:** `TABLE_ROWS` "may store an estimate" for InnoDB [mdb-is-tables].
- **information_schema_stats_expiry:** I found no evidence MariaDB has this variable. **Unverified.**

## 6. Read-only safety for NL-generated SQL

**MySQL**
- **READ ONLY transactions.** `START TRANSACTION READ ONLY` "prevents the transaction from modifying or locking both transactional and nontransactional tables that are visible to other transactions". DML on TEMPORARY tables is still allowed, but DDL is not [commit84].
- **The guard can be escaped.** DDL, `START TRANSACTION`/`BEGIN`, `LOCK TABLES` and similar statements cause an implicit commit [implicit84]. So the read-only guard holds only if dbscope sends one statement at a time. The go-sql-driver option `multiStatements` defaults to false [gomysql-readme].
- **Locking reads.** The doc wording says locking is prohibited. Whether `SELECT ... FOR UPDATE` errors or succeeds in practice is **unverified**, so dbscope should reject it statically.
- **Timeouts.**
  - `max_execution_time` (ms, default 0 = off) and the `/*+ MAX_EXECUTION_TIME(N) */` hint apply only to read-only SELECT [sysvar84] [hints84].
  - They don't apply to a SELECT that calls a data-modifying stored function, or to SELECTs inside stored programs [sysvar84] [hints84].
  - The hint must follow the first SELECT keyword [hints84].
- **SELECT ... INTO OUTFILE** writes a file on the server host and needs the FILE privilege. It can't overwrite an existing file [selinto84].
- **SELECT ... INTO @var** sets session user variables [selinto84]. That is a session-scoped side effect.
- **Other side-effecting functions.** GET_LOCK takes MDL-style user locks [mdl84]. Whether SLEEP, BENCHMARK and similar functions are blocked by READ ONLY is **unverified**. Deny-list them.

**MariaDB**
- **READ ONLY transactions:** documented from 10.11. Unlike MySQL, it also blocks writes and DDL on temporary tables, and the READ_ONLY ADMIN privilege doesn't override it [mdb-start].
- **max_statement_time:**
  - Measured in seconds, as a double. It can kill any statement, not just SELECT.
  - Can be set globally, per session, per user (`GRANT ... MAX_STATEMENT_TIME`), or per query with `SET STATEMENT max_statement_time=N FOR ...`.
  - The abort isn't immediate; the server checks at intervals [mdb-abort].
  - From 12.0 the `MAX_EXECUTION_TIME` hint (ms) works for SELECT. The MySQL `SELECT MAX_STATEMENT_TIME=N` syntax is invalid [mdb-abort].
- **Sequences.** Whether MariaDB sequence functions (NEXTVAL) are blocked in a READ ONLY transaction is **unverified**.

## 7. Go libraries with CGO off

**Driver: go-sql-driver/mysql**
- v1.10.1, released 2026-09-02. Its CI matrix added MariaDB 11.8 and 12.3 [gomysql-rel].
- License MPL-2.0 [gomysql-repo].
- "Native Go implementation. No C-bindings" [gomysql-readme].
- Requires Go 1.25+. Maintainers support MySQL 8.0+ and MariaDB 10.11+ [gomysql-readme].
- `multiStatements` defaults to false [gomysql-readme].
- dbscope's go.mod has no MySQL dependency today. This is based on the file in the workspace.

**Parser: vitess.io/vitess/go/vt/sqlparser**
- Apache-2.0 [vitess-repo]. Latest release v24.0.5, 2026-10-13 [vitess-rel].
- It lives inside the full `vitess.io/vitess` module. That module's go.mod says `go 1.27.3` and lists cloud SDKs (AWS, GCS, Azure) [vitess-gomod]. How much of that gets linked into dbscope is **unverified**.
- **NOCOPY gap:** the ALTER modifier grammar accepts ALGORITHM DEFAULT|INPLACE|COPY|INSTANT and LOCK DEFAULT|NONE|SHARED|EXCLUSIVE. The grammar file contains no `NOCOPY` [vitess-y].
- Coverage of MariaDB `WAIT n`/`NOWAIT` on ALTER and `ALTER ONLINE TABLE` is **unverified**.

**Parser: github.com/pingcap/tidb/pkg/parser**
- A separate module. Apache-2.0 [tidb-repo].
- go.mod on master says `go 1.27.3` and pulls in pingcap/errors, zap and go-sql-driver/mysql v1.7.1 [tidb-gomod].
- **NOCOPY gap:** AlgorithmClause accepts DEFAULT|COPY|INPLACE|INSTANT. Any other identifier raises `ErrUnknownAlterAlgorithm` [tidb-y].
- Parses `FOR UPDATE ... NOWAIT`, `WAIT n` and `SKIP LOCKED` on SELECT [tidb-y]. Parses the `MAX_EXECUTION_TIME` hint [tidb-hint].
- The latest GitHub "latest" release is TiDB v7.5.8 (2026-09-17), a maintenance line [tidb-repo]. How the parser module is tagged is **unverified**.

**Shared caveats**
- Both parser modules declare `go 1.27.3`. dbscope declares `go 1.26.0` with toolchain go1.27.3. Adding either would likely raise dbscope's go line. **Unverified** against the Go toolchain docs this session.
- Binary-size impact is **unverified** for both parsers. It needs a build-and-compare measurement.
- Other parsers (dolthub/vitess fork, xwb1989/sqlparser) were **not evaluated**.

## Sources
- odl80: https://dev.mysql.com/doc/refman/8.0/en/innodb-online-ddl-operations.html
- odl84: https://dev.mysql.com/doc/refman/8.4/en/innodb-online-ddl-operations.html
- odl97: https://dev.mysql.com/doc/refman/9.7/en/innodb-online-ddl-operations.html
- perf84: https://dev.mysql.com/doc/refman/8.4/en/innodb-online-ddl-performance.html
- alter84: https://dev.mysql.com/doc/refman/8.4/en/alter-table.html
- mdl84: https://dev.mysql.com/doc/refman/8.4/en/metadata-locking.html
- sysvar84: https://dev.mysql.com/doc/refman/8.4/en/server-system-variables.html
- innodbvar84: https://dev.mysql.com/doc/refman/8.4/en/innodb-parameters.html
- releases: https://dev.mysql.com/doc/mysql/en/mysql-releases.html
- oct26: https://blogs.oracle.com/mysql/mysql-october-2026-ga-releases-now-available
- is-tables84: https://dev.mysql.com/doc/refman/8.4/en/information-schema-tables-table.html
- is-columns84: https://dev.mysql.com/doc/refman/8.4/en/information-schema-columns-table.html
- is-stats84: https://dev.mysql.com/doc/refman/8.4/en/information-schema-statistics-table.html
- is-check84: https://dev.mysql.com/doc/refman/8.4/en/information-schema-check-constraints-table.html
- is-tc84: https://dev.mysql.com/doc/refman/8.4/en/information-schema-table-constraints-table.html
- is-kcu84: https://dev.mysql.com/doc/refman/8.4/en/information-schema-key-column-usage-table.html
- is-rc84: https://dev.mysql.com/doc/refman/8.4/en/information-schema-referential-constraints-table.html
- invcol84: https://dev.mysql.com/doc/refman/8.4/en/invisible-columns.html
- cc80: https://dev.mysql.com/doc/refman/8.0/en/create-table-check-constraints.html
- commit84: https://dev.mysql.com/doc/refman/8.4/en/commit.html
- implicit84: https://dev.mysql.com/doc/refman/8.4/en/implicit-commit.html
- hints84: https://dev.mysql.com/doc/refman/8.4/en/optimizer-hints.html
- selinto84: https://dev.mysql.com/doc/refman/8.4/en/select-into.html
- mdb-overview: https://mariadb.com/docs/server/server-usage/storage-engines/innodb/innodb-online-ddl/innodb-online-ddl-overview
- mdb-instant: https://mariadb.com/docs/server/server-usage/storage-engines/innodb/innodb-online-ddl/innodb-online-ddl-operations-with-the-instant-alter-algorithm
- mdb-nocopy: https://mariadb.com/docs/server/server-usage/storage-engines/innodb/innodb-online-ddl/innodb-online-ddl-operations-with-the-nocopy-alter-algorithm
- mdb-inplace: https://mariadb.com/docs/server/server-usage/storage-engines/innodb/innodb-online-ddl/innodb-online-ddl-operations-with-the-inplace-alter-algorithm
- mdb-104: https://mariadb.com/resources/blog/alter-table-improvements-in-mariadb-server-10-4/
- mdb-112: https://mariadb.com/resources/blog/alter-table-is-now-universally-online/
- mdb-mdl: https://mariadb.com/kb/en/metadata-locking/
- mdb-wait: https://mariadb.com/kb/en/wait-and-nowait/
- mdb-start: https://mariadb.com/kb/en/start-transaction/
- mdb-abort: https://mariadb.com/kb/en/aborting-statements/
- mdb-is-tables: https://mariadb.com/kb/en/information-schema-tables-table/
- mdb-is-columns: https://mariadb.com/kb/en/information-schema-columns-table/
- mdb-is-stats: https://mariadb.com/kb/en/information-schema-statistics-table/
- mdb-is-check: https://mariadb.com/kb/en/information-schema-check_constraints-table/
- mdb-ignored: https://mariadb.com/kb/en/ignored-indexes/
- mdb-invcol: https://mariadb.com/kb/en/invisible-columns/
- mdb-releases: https://mariadb.org/mariadb/all-releases/
- mdb-lts: https://mariadb.com/resources/blog/announcing-yearly-lts-releases-for-mariadb-community-server/
- ghost-req: https://github.com/github/gh-ost/blob/master/doc/requirements-and-limitations.md
- ghost-readme: https://github.com/github/gh-ost
- ghost-rel: https://github.com/github/gh-ost/releases/tag/v1.1.11
- ptosc: https://docs.percona.com/percona-toolkit/pt-online-schema-change.html
- gomysql-readme: https://github.com/go-sql-driver/mysql/blob/master/README.md
- gomysql-rel: https://github.com/go-sql-driver/mysql/releases/tag/v1.10.1
- gomysql-repo: https://api.github.com/repos/go-sql-driver/mysql
- vitess-y: https://github.com/vitessio/vitess/blob/main/go/vt/sqlparser/sql.y
- vitess-rel: https://github.com/vitessio/vitess/releases/tag/v24.0.5
- vitess-gomod: https://github.com/vitessio/vitess/blob/main/go.mod
- vitess-repo: https://api.github.com/repos/vitessio/vitess
- tidb-y: https://github.com/pingcap/tidb/blob/master/pkg/parser/parser.y
- tidb-hint: https://github.com/pingcap/tidb/blob/master/pkg/parser/hintparser.y
- tidb-gomod: https://github.com/pingcap/tidb/blob/master/pkg/parser/go.mod
- tidb-repo: https://api.github.com/repos/pingcap/tidb

## Facts dbscope must encode
- [ ] **Every MySQL ALTER takes an exclusive MDL**, including INSTANT. A pending one queues every later query on the table. Warn about long-running transactions on every MySQL ALTER, not only rebuilds [perf84] [alter84].
- [ ] **Default `lock_wait_timeout` is 31536000 s on both servers.** Recommend setting a low session value before DDL on MySQL, or `WAIT n`/`NOWAIT` on MariaDB [sysvar84] [mdb-mdl] [mdb-wait].
- [ ] **`innodb_lock_wait_timeout` (50 s) does not govern MDL waits** [innodbvar84].
- [ ] **ALGORITHM semantics differ.** MySQL: an explicit ALGORITHM is exact or errors, and an omitted one falls back silently to COPY. MariaDB: ALGORITHM is a floor, and COPY is exact [alter84] [mdb-overview].
- [ ] **A LOCK level weaker than permitted raises an error** on both servers [perf84] [mdb-overview].
- [ ] **INSTANT ADD COLUMN depends on version.** Any position needs MySQL 8.0.29+; earlier versions allow last position only. Excluded: COMPRESSED row format, tables with a FULLTEXT index, temporary tables. Row-version cap is 64 (8.0, 8.4) or 255 (9.1.0+). Internal column cap is 1022 [odl80] [odl84] [odl97].
- [ ] **INSTANT DROP COLUMN needs 8.0.29+, and INSTANT RENAME COLUMN needs 8.0.28+.** Renaming a column referenced by another table's FK is INPLACE only [odl80].
- [ ] **Column type change is COPY with no DML on MySQL.** On MariaDB 11.2+ it can run online via COPY with LOCK=NONE [odl84] [mdb-112].
- [ ] **VARCHAR widening must be computed in bytes** using the column's charset. The 1-to-2 length-byte boundary means COPY on MySQL. MariaDB has REDUNDANT and ≤127-byte exceptions [odl84] [mdb-instant].
- [ ] **NULL and NOT NULL changes rebuild the table** with INPLACE. NOT NULL needs strict mode and no existing NULLs [odl84].
- [ ] **FK add needs `foreign_key_checks=0` to avoid COPY** [odl84] [mdb-nocopy].
- [ ] **These need COPY only:** dropping a PK without adding one, and adding a STORED generated column. CONVERT TO CHARACTER SET blocks DML [odl84].
- [ ] **FULLTEXT and SPATIAL index adds block DML.** The first FULLTEXT index rebuilds the table [odl84] [mdb-nocopy].
- [ ] **ENUM/SET changes are INSTANT only when appending with no storage-size change** [odl84] [mdb-instant].
- [ ] **DROP PARTITION with INPLACE deletes data.** OPTIMIZE PARTITION rebuilds the entire table [odl84].
- [ ] **MariaDB-only operations:** INSTANT reorder and drop (10.4+), NOCOPY index adds, RENAME TABLE needs LOCK=EXCLUSIVE, `ALTER ONLINE TABLE` means LOCK=NONE, alter_algorithm is ignored from 11.5 [mdb-104] [mdb-instant] [mdb-overview].
- [ ] **OSC tool choice:** gh-ost needs ROW binlogs, no FKs and no triggers. pt-osc needs no existing triggers and an explicit FK method. Both need a PK or UNIQUE key [ghost-req] [ptosc].
- [ ] **ERD row counts:** label InnoDB `TABLE_ROWS` as an estimate (40–50% error possible). Cached up to `information_schema_stats_expiry` (86400 s) on MySQL [is-tables84] [sysvar84].
- [ ] **ERD per-server differences:** read INVISIBLE from `EXTRA`. Read invisible or ignored indexes from `IS_VISIBLE` (MySQL) or `IGNORED` (MariaDB). Read CHECK constraints via `TABLE_CONSTRAINTS` on MySQL and `CHECK_CONSTRAINTS.TABLE_NAME` on MariaDB. Parse `COLUMN_DEFAULT` with per-server quoting rules [is-columns84] [is-stats84] [mdb-is-columns] [mdb-is-stats] [mdb-is-check].
- [ ] **NL SQL guardrails:**
  - One statement per call (`multiStatements=false`).
  - `START TRANSACTION READ ONLY`.
  - Reject INTO OUTFILE/DUMPFILE, FOR UPDATE/SHARE and LOCK IN SHARE MODE. Reject user variables and side-effect functions too.
  - Don't grant the dbscope user FILE.
  - Set the timeout per server: `MAX_EXECUTION_TIME` in ms (MySQL, SELECT only) or `SET STATEMENT max_statement_time=N FOR` in seconds (MariaDB) [commit84] [implicit84] [hints84] [selinto84] [mdb-abort] [gomysql-readme].
- [ ] **Parser gap:** a pure-Go MySQL parser (vitess or TiDB) will reject MariaDB `ALGORITHM=NOCOPY`. dbscope needs a fallback or a pre-pass for MariaDB syntax [vitess-y] [tidb-y].
