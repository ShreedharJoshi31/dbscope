# Research: SQLite support

Collected 2026-10-28 by a research worker from primary sources (cited inline); nothing was run against a server during research, and claims it could not verify are marked "unverified". The design dbscope adopted is in docs/ENGINES.md.

These findings matter most for dbscope's design:
- **SQLite 3.53.0 added new ALTER forms.** It added `ALTER COLUMN ... SET/DROP NOT NULL` and `ADD/DROP CONSTRAINT` (CHECK). The rqlite/sql Go parser does not model them yet.
- **The usual "turn foreign keys off" step fails silently inside a transaction.** `PRAGMA foreign_keys=OFF` does nothing there and raises no error. If a migration runner wraps each file in a transaction, the 12-step procedure's `DROP TABLE` can then run cascading foreign key actions on child tables.
- **The rules depend on the app's SQLite version.** dbscope links SQLite 3.54.1 through modernc.org/sqlite v1.61.0. A migration follows the rules of whatever SQLite version the app that runs it uses.


### 1. Introspection for an ERD

**1.1 Objects in `sqlite_schema`** ([schematab](https://www.sqlite.org/schematab.html))

| Column | Meaning |
|---|---|
| `type` | `table`, `index`, `view` or `trigger`. Virtual tables also use `table`. |
| `name` | Object name. Indexes created for UNIQUE and PRIMARY KEY constraints are named `sqlite_autoindex_TABLE_N`. |
| `tbl_name` | For an index, the indexed table. For a trigger, the table or view it fires on. |
| `rootpage` | 0 or NULL for views, triggers and virtual tables. |
| `sql` | Normalized CREATE text. The TEMP keyword and schema qualifier are removed, and later ALTERs are reflected. It is NULL for autoindexes. |

- A WITHOUT ROWID table's PRIMARY KEY has no `sqlite_schema` row. Its autoindex name is still reserved, which shifts the numbering of later UNIQUE autoindexes. An INTEGER PRIMARY KEY never gets an autoindex ([schematab](https://www.sqlite.org/schematab.html)).
- A virtual table is a `type='table'` row whose `sql` is `CREATE VIRTUAL TABLE ...` ([schematab](https://www.sqlite.org/schematab.html)).
- The name `sqlite_master` still works as an alias. `sqlite_temp_schema` and `sqlite_temp_master` work only for TEMP ([schematab](https://www.sqlite.org/schematab.html)).
- The name `sqlite_schema` dates from 3.33.0, according to an [sqlite.org forum post](https://sqlite.org/forum/info/889b616b87a7020a). I did not find this in the [changes.html](https://www.sqlite.org/changes.html) entry for 3.33.0, so the version is only partly verified.

**1.2 PRAGMAs and their table-valued forms**

You can call any PRAGMA that returns rows and has no side effects as `pragma_NAME(arg, schema)`. The schema goes last and is optional. This form arrived in 3.16.0 (2017-01-02) ([pragma.html, "PRAGMA functions"](https://www.sqlite.org/pragma.html)). Column names come from [mkpragmatab.tcl](https://github.com/sqlite/sqlite/blob/master/tool/mkpragmatab.tcl).

| PRAGMA | Columns | Notes | Since |
|---|---|---|---|
| `table_list` | schema, name, type, ncol, wr, strict | `type` is table, view, shadow or virtual. `wr`=1 means WITHOUT ROWID. `strict`=1 means STRICT. It covers all schemas unless you give one ([pragma](https://www.sqlite.org/pragma.html#pragma_table_list)). | 3.37.0 (2021-11-27) |
| `table_info` | cid, name, type, notnull, dflt_value, pk | Leaves out generated and hidden columns. `pk` is the 1-based position in the primary key, or 0. It also works on views ([pragma](https://www.sqlite.org/pragma.html#pragma_table_info)). | unverified |
| `table_xinfo` | the above plus hidden | Includes generated and hidden columns ([pragma](https://www.sqlite.org/pragma.html#pragma_table_xinfo)). | 3.26.0 (2018-12-01) ([changes](https://www.sqlite.org/changes.html)) |
| `index_list` | seq, name, unique, origin, partial | `origin` is `c` (CREATE INDEX), `u` (UNIQUE) or `pk` ([pragma](https://www.sqlite.org/pragma.html#pragma_index_list)). | unverified, including when origin and partial were added |
| `index_info` | seqno, cid, name | Key columns only. `cid` -1 means rowid and -2 means an expression. `name` is NULL in both cases ([pragma](https://www.sqlite.org/pragma.html#pragma_index_info)). | unverified |
| `index_xinfo` | seqno, cid, name, desc, coll, key | Key and auxiliary columns. `key`=0 marks an auxiliary column ([pragma](https://www.sqlite.org/pragma.html#pragma_index_xinfo)). | 3.8.9 (2015-04-08) ([changes](https://www.sqlite.org/changes.html)) |
| `foreign_key_list` | id, seq, table, from, to, on_update, on_delete, match | One row per REFERENCES column ([pragma](https://www.sqlite.org/pragma.html#pragma_foreign_key_list), [mkpragmatab](https://github.com/sqlite/sqlite/blob/master/tool/mkpragmatab.tcl)). | unverified |
| `foreign_key_check` | table, rowid, parent, fkid | `rowid` is NULL for WITHOUT ROWID child tables. `fkid` matches `foreign_key_list.id` ([pragma](https://www.sqlite.org/pragma.html#pragma_foreign_key_check)). | unverified |
| `database_list` | seq, name, file | `main`, `temp`, then attached schemas ([pragma](https://www.sqlite.org/pragma.html#pragma_database_list)). | unverified |

- **Hidden values.** The docs say 0 is a normal column, 1 is a hidden column in a virtual table, and "2 or 3" is a generated column ([pragma](https://www.sqlite.org/pragma.html#pragma_table_xinfo)). The source settles which is which: 2 is `GENERATED ... VIRTUAL` and 3 is `GENERATED ... STORED` ([pragma.c](https://github.com/sqlite/sqlite/blob/master/src/pragma.c)).
- **cid differs between the two pragmas.** Based on pragma.c, `table_info` renumbers cid after skipping hidden columns, while `table_xinfo` uses the raw column index. Generated columns report `dflt_value` NULL. No PRAGMA I found exposes a generated column's expression, so dbscope appears to need to parse `sqlite_schema.sql` for it.
- **WITHOUT ROWID tables in index pragmas.** Since 3.30.0, `index_info` and `index_xinfo` accept a WITHOUT ROWID table's name and return its on-disk key layout ([pragma](https://www.sqlite.org/pragma.html#pragma_index_xinfo)).
- **Virtual table hidden columns** are declared with the HIDDEN type keyword. They are left out of `table_info`, `SELECT *` and implicit INSERT column lists ([vtab §2.1.1](https://www.sqlite.org/vtab.html)).
- **Shadow tables** are the real tables that hold a virtual table's content (FTS3/5, RTREE). They are named `<vtab>_<suffix>` and show `type='shadow'` in `table_list` ([vtab §2.21](https://www.sqlite.org/vtab.html)).

**1.3 Table properties and how to detect them**

| Property | Detection | Since | Source |
|---|---|---|---|
| STRICT | `table_list.strict`=1 | 3.37.0 | [stricttables](https://www.sqlite.org/stricttables.html) |
| WITHOUT ROWID | `table_list.wr`=1 | 3.8.2 (2013-12-06) | [withoutrowid](https://www.sqlite.org/withoutrowid.html) |
| Generated column | `table_xinfo.hidden` is 2 or 3 | 3.31.0 (2020-01-22) | [gencol](https://www.sqlite.org/gencol.html) |
| View | `sqlite_schema.type='view'` or `table_list.type='view'` | unverified | [schematab](https://www.sqlite.org/schematab.html) |
| Trigger | `sqlite_schema.type='trigger'`. `tbl_name` is the target. | unverified | [schematab](https://www.sqlite.org/schematab.html) |
| Virtual table | `table_list.type='virtual'`, or `sql` starts with CREATE VIRTUAL TABLE | unverified | [pragma](https://www.sqlite.org/pragma.html#pragma_table_list) |
| Rowid alias (INTEGER PRIMARY KEY) | Table is not WITHOUT ROWID, has a single-column PK, the declared type is exactly "INTEGER" (any case), and the column is not declared `INTEGER PRIMARY KEY DESC`. `INT` or `BIGINT` do not qualify. | n/a | [lang_createtable §5](https://www.sqlite.org/lang_createtable.html) |
| AUTOINCREMENT | No PRAGMA reports it that I could find. Scan `sql` for the keyword. It is only allowed on INTEGER PRIMARY KEY and never on WITHOUT ROWID tables. `sqlite_sequence` is created with the table, but its row for that table appears only on first write. | unverified | [autoinc](https://www.sqlite.org/autoinc.html) |

- **A derived rowid-alias test.** A rowid table with a PK but no `index_list` row with `origin='pk'` should have an INTEGER PRIMARY KEY. This follows from [schematab](https://www.sqlite.org/schematab.html) and [lang_createtable §3.6](https://www.sqlite.org/lang_createtable.html), but I did not test it.
- **PRIMARY KEY columns can hold NULL.** That is true unless the column is an INTEGER PRIMARY KEY, is declared NOT NULL, or the table is STRICT or WITHOUT ROWID ([lang_createtable §3.5](https://www.sqlite.org/lang_createtable.html)).
- **Pragma availability depends on the library, not the file.** dbscope's driver, modernc.org/sqlite v1.61.0, lists SQLite 3.54.1 on every platform ([pkg.go.dev](https://pkg.go.dev/modernc.org/sqlite@v1.61.0)). So all the pragmas above should work on any file dbscope opens. This is my inference.

**1.4 Attached databases**

- Schema names are `main`, `temp` and the `AS` name. `main` and `temp` cannot be attached or detached ([lang_attach](https://www.sqlite.org/lang_attach.html)).
- If two attached schemas hold a table with the same name, an unqualified reference picks the database that was attached least recently ([lang_attach](https://www.sqlite.org/lang_attach.html)).
- Pass the schema as `schema.pragma` or as the last argument of the table-valued form ([pragma](https://www.sqlite.org/pragma.html)). `table_list` covers all schemas by default.
- Foreign keys cannot cross schemas. `REFERENCES X` resolves only within its own schema ([foreignkeys §6](https://www.sqlite.org/foreignkeys.html)).

### 2. ALTER TABLE

**2.1 Supported forms** ([lang_altertable](https://www.sqlite.org/lang_altertable.html), [changes](https://www.sqlite.org/changes.html))

| Form | Version | Data work |
|---|---|---|
| RENAME TO | First version unverified. References in triggers and views are rewritten since 3.25.0. FK references are always rewritten since 3.26.0. | Schema text only, constant time |
| RENAME COLUMN | 3.25.0 (2018-09-15) | Schema text only |
| ADD COLUMN | First version unverified. Files where it has run are unreadable by 3.1.3 and earlier. New constraints are checked against existing rows since 3.37.0. | Constant time, unless it adds a CHECK or a NOT NULL generated column. Those read every row. |
| DROP COLUMN | 3.35.0 (2021-03-12). Corruption fixes landed in 3.35.1 and 3.35.5. | Rewrites every row |
| ALTER COLUMN SET / DROP NOT NULL | 3.53.0 (2026-04-09) | Whether it scans rows is unverified |
| ADD CONSTRAINT ... CHECK / DROP CONSTRAINT | In the syntax diagram. The 3.53.0 notes say ALTER can now add and remove NOT NULL and CHECK constraints. | Restrictions and row checking are unverified |

- Since 3.54.0, ALTER raises an error when you add, drop or rename a column named ROWID, _ROWID_ or OID ([changes](https://www.sqlite.org/changes.html)).
- Since 3.38.0, `writable_schema=ON` makes ALTER skip `sqlite_schema` rows that do not parse. Without it, any unparseable row makes ALTER fail ([lang_altertable §7](https://www.sqlite.org/lang_altertable.html)).
- ALTER edits the stored CREATE text and then reparses the whole schema. It fails if the schema becomes invalid ([§5.1](https://www.sqlite.org/lang_altertable.html)).

**2.2 ADD COLUMN restrictions** ([§4](https://www.sqlite.org/lang_altertable.html))
- No PRIMARY KEY or UNIQUE.
- No default of CURRENT_TIME, CURRENT_DATE, CURRENT_TIMESTAMP, or a parenthesized expression.
- NOT NULL needs a non-NULL default.
- With foreign keys enabled, a column with REFERENCES must default to NULL. This is also in [foreignkeys §5](https://www.sqlite.org/foreignkeys.html).
- No `GENERATED ALWAYS ... STORED`. VIRTUAL generated columns are allowed ([gencol](https://www.sqlite.org/gencol.html)).
- Inference: on a STRICT table, the new column likely needs one of INT, INTEGER, REAL, TEXT, BLOB or ANY as its type ([stricttables](https://www.sqlite.org/stricttables.html)). I did not test this.

**2.3 DROP COLUMN fails if the column** ([§5](https://www.sqlite.org/lang_altertable.html))
- is in the PRIMARY KEY, or has a UNIQUE constraint,
- is indexed, or appears in a partial index's WHERE clause,
- appears in a CHECK constraint that is not attached to that column,
- is used in a foreign key,
- is used in a generated column's expression,
- appears in a trigger or view.

**2.4 Renames and legacy_alter_table**
- RENAME TO cannot move a table between attached databases. Triggers and indexes stay attached to the renamed table ([§2](https://www.sqlite.org/lang_altertable.html)).
- RENAME COLUMN updates the table, indexes, triggers and views. It fails, with no change, if the new name would make a trigger or view ambiguous ([§3](https://www.sqlite.org/lang_altertable.html)).
- `legacy_alter_table=ON` brings back RENAME behaviour from 3.24.0 and earlier ([pragma](https://www.sqlite.org/pragma.html#pragma_legacy_alter_table)):
  - It rewrites only the first occurrence of the name in the CREATE TABLE and in its own indexes and triggers.
  - It leaves trigger and view bodies, CHECK constraints and partial-index WHERE clauses unchanged.
  - The default is OFF. The setting is per connection, not persistent, and applies to all attached files.
  - SQLite turns it on temporarily while a virtual table's xRename runs.
- Whether a parent-table rename updates foreign key references ([§2](https://www.sqlite.org/lang_altertable.html)):

| foreign_keys | legacy_alter_table | FK refs updated | Version |
|---|---|---|---|
| Off | Off | No | before 3.26.0 |
| Off | Off | Yes | 3.26.0 and later |
| On | Off | Yes | all |
| Off | On | No | all |
| On | On | Yes | all |

### 3. The 12-step generalized ALTER procedure

Paraphrased from [lang_altertable §8](https://www.sqlite.org/lang_altertable.html), with reasons from the cited pages.

1. **If foreign keys are on, turn them off with `PRAGMA foreign_keys=OFF`.** This must happen outside any transaction, because the pragma does nothing inside one and gives no error ([pragma](https://www.sqlite.org/pragma.html#pragma_foreign_keys), [foreignkeys §2](https://www.sqlite.org/foreignkeys.html)). With foreign keys on, step 6's DROP TABLE runs an implicit DELETE. That DELETE fires foreign key actions such as CASCADE, or fails ([lang_droptable](https://www.sqlite.org/lang_droptable.html)).
2. **Start a transaction.**
3. **Save the definitions of every index, trigger and view tied to X**, for example with `SELECT type, sql FROM sqlite_schema WHERE tbl_name='X'`. DROP TABLE deletes the table's indexes and triggers ([lang_droptable](https://www.sqlite.org/lang_droptable.html)).
4. **CREATE TABLE new_X with the new shape.** Pick a name that does not collide with an existing table.
5. **Copy the rows** with `INSERT INTO new_X SELECT ... FROM X`.
6. **DROP TABLE X.**
7. **`ALTER TABLE new_X RENAME TO X`.**
8. **Recreate the indexes, triggers and views** with CREATE INDEX, CREATE TRIGGER and CREATE VIEW, adjusted as needed.
9. **Drop and recreate any view that the change affects.**
10. **If foreign keys were on at the start, run `PRAGMA foreign_key_check`.** Enforcement was off during the copy, so violations would not have been caught.
11. **Commit.**
12. **If foreign keys were on at the start, turn them back on.**

**Order matters.** The docs warn against "rename old, create new, copy, drop". Renaming the old table first can corrupt references to it in triggers, views and foreign keys under the 3.25 and 3.26 rename behaviour. The safe order is create new, copy, drop old, rename new.

The 12 steps suit any change, such as dropping a column, reordering, adding or removing UNIQUE or PRIMARY KEY, adding CHECK, FOREIGN KEY or NOT NULL, or changing a type ([§8](https://www.sqlite.org/lang_altertable.html)).

**The simpler writable_schema procedure.** The docs allow it only for removing CHECK, FOREIGN KEY or NOT NULL constraints, or for changing defaults:
- BEGIN.
- Read `PRAGMA schema_version`.
- Set `writable_schema=ON`.
- UPDATE `sqlite_schema.sql` for X and any other object that depends on it.
- Set `schema_version` to the old value plus 1.
- Set `writable_schema=OFF`.
- Optionally run `integrity_check`.
- COMMIT.

**Cautions for the simpler procedure:**
- A syntax error in the UPDATE "will render the database corrupt and unreadable", so test on a blank database first ([§8](https://www.sqlite.org/lang_altertable.html)).
- Changing schema_version wrongly can make statements run against a stale schema, giving wrong answers or corruption ([pragma](https://www.sqlite.org/pragma.html#pragma_schema_version)).
- In defensive mode, writes to schema_version are a silent no-op (since 3.40.0, per [changes](https://www.sqlite.org/changes.html)). `writable_schema` edits also require defensive mode to be off ([pragma](https://www.sqlite.org/pragma.html#pragma_writable_schema)).
- VACUUM counts as a schema change for schema_version ([pragma](https://www.sqlite.org/pragma.html#pragma_schema_version)).

### 4. Locking and risk

**4.1 Rollback-journal mode** ([lockingv3](https://www.sqlite.org/lockingv3.html))
- Locks cover the whole database file. The states are UNLOCKED, SHARED, RESERVED, PENDING and EXCLUSIVE.
- `BEGIN` (DEFERRED) takes no lock. The first read takes SHARED. The first write takes RESERVED. Only one RESERVED lock can exist, and readers continue while it is held.
- Writing to the database file needs PENDING and then EXCLUSIVE. PENDING stops new readers. EXCLUSIVE waits for existing readers to finish.
- This happens at COMMIT, or earlier when the page cache spills. After a spill, EXCLUSIVE is held until commit, so no other process can read in that window ([lockingv3 §5](https://www.sqlite.org/lockingv3.html), [cache_spill](https://www.sqlite.org/pragma.html#pragma_cache_spill)).
- The default cache is -2000, about 2 MB ([cache_size](https://www.sqlite.org/pragma.html#pragma_cache_size)). Inference: a table copy larger than that will probably spill, which blocks readers for the rest of the copy.
- `BEGIN EXCLUSIVE` blocks readers from the start in rollback mode. In WAL mode it behaves like IMMEDIATE ([lang_transaction](https://www.sqlite.org/lang_transaction.html)).

**4.2 WAL mode** ([wal](https://www.sqlite.org/wal.html))
- Readers and the writer do not block each other. There is still only one writer at a time.
- Each reader sees a snapshot fixed when its read starts (the "end mark").
- A long-running reader stops the checkpoint from progressing, so the WAL file grows.
- The page says WAL "does not work well" for transactions over about 100 MB and may fail over 1 GB. The same page also says that since 3.11.0, WAL handles large transactions as efficiently as rollback mode.
- Transactions that span attached databases are atomic only per file in WAL mode ([lang_attach](https://www.sqlite.org/lang_attach.html)).
- If another connection has already written, upgrading a read transaction to a write fails with SQLITE_BUSY ([lang_transaction](https://www.sqlite.org/lang_transaction.html)).

**4.3 busy_timeout**
- A connection has one busy handler. It sleeps until at least N ms have accumulated, then returns SQLITE_BUSY. A value of 0 or less disables it ([c3ref](https://www.sqlite.org/c3ref/busy_timeout.html)). The maximum is 2147483647 ms ([pragma](https://www.sqlite.org/pragma.html#pragma_busy_timeout)).
- Unverified: the default when no handler is set, and whether the modernc driver sets one.

**4.4 How long each operation blocks**

| Operation | Readers (rollback) | Readers (WAL) | Other writers |
|---|---|---|---|
| RENAME, RENAME COLUMN, plain ADD COLUMN | Brief, at commit | Not blocked | Blocked for the short transaction |
| DROP COLUMN, ADD with CHECK or generated NOT NULL | Time grows with table size once the cache spills | Not blocked | Blocked for the whole run |
| 12-step rebuild | From the first spill until COMMIT | Not blocked, but the WAL grows by every changed page | Blocked for the whole run |
| VACUUM | Like a full-database write | Snapshot readers continue | Blocked |

Data-work facts come from [lang_altertable §4](https://www.sqlite.org/lang_altertable.html). Locking facts come from 4.1 and 4.2. The table combines them, and I did not test it.

**4.5 What makes a migration dangerous on a large file**
- **Full table copy.** A 12-step rebuild or DROP COLUMN rewrites all rows. Its indexes are rebuilt in step 8.
- **File growth.** Deleted pages go to the freelist and the file does not shrink. Shrinking needs `auto_vacuum`, which must be set before any table exists or applied through VACUUM ([auto_vacuum](https://www.sqlite.org/pragma.html#pragma_auto_vacuum)).
- **VACUUM itself.** It rebuilds the whole file and can need up to twice the file size in free disk space. It fails inside an open transaction. It may change rowids of tables that lack an explicit INTEGER PRIMARY KEY. `VACUUM INTO` leaves the original file alone ([lang_vacuum](https://www.sqlite.org/lang_vacuum.html)).
- **Leftover journal and WAL files.** `journal_size_limit` defaults to -1 (no limit). After a large transaction, a persistent journal or the WAL can stay large ([pragma](https://www.sqlite.org/pragma.html#pragma_journal_size_limit)).
- **Unsafe journal modes.** `journal_mode=OFF` or `MEMORY` can corrupt the file on a crash. With OFF, a failed CREATE UNIQUE INDEX can leave a corrupt index ([journal_mode](https://www.sqlite.org/pragma.html#pragma_journal_mode)).
- **`writable_schema` edits.** One typo corrupts the database (section 3).

### 5. Foreign keys ([foreignkeys](https://www.sqlite.org/foreignkeys.html))

**Enabling**
- Enforcement exists since 3.6.19 (2009-10-14). It is off by default and must be turned on per connection. A compile-time option, SQLITE_DEFAULT_FOREIGN_KEYS, can change the default ([pragma](https://www.sqlite.org/pragma.html#pragma_foreign_keys)).
- Switching it inside a multi-statement transaction does nothing and gives no error ([§2](https://www.sqlite.org/foreignkeys.html)).

**Deferred constraints**
- Only `DEFERRABLE INITIALLY DEFERRED` makes a constraint deferred. Every other form is immediate ([§4.2](https://www.sqlite.org/foreignkeys.html)).
- Deferred constraints are checked at COMMIT. A failing COMMIT leaves the transaction open.
- You cannot switch a single constraint between deferred and immediate at runtime ([§6](https://www.sqlite.org/foreignkeys.html)).
- `defer_foreign_keys=ON` defers every constraint until the outermost COMMIT, and resets at each COMMIT or ROLLBACK ([pragma](https://www.sqlite.org/pragma.html#pragma_defer_foreign_keys)).
- Setting `defer_foreign_keys=0` mid-transaction resets FK tracking, which can let a transaction commit with violations ([pragma](https://www.sqlite.org/pragma.html#pragma_defer_foreign_keys)).
- The [changes](https://www.sqlite.org/changes.html) entry that appears to be 3.12.0 says `defer_foreign_keys=ON` also disables RESTRICT actions.
- RESTRICT acts at once, even on a deferred constraint ([§4.3](https://www.sqlite.org/foreignkeys.html)).

**Existing violating rows**
- CREATE TABLE does not check the parent table at all ([§5](https://www.sqlite.org/foreignkeys.html)).
- Nothing stops violations while enforcement is off.
- `foreign_key_check` lists violations. `integrity_check` does not find them ([pragma](https://www.sqlite.org/pragma.html#pragma_integrity_check)).
- I found no statement that turning `foreign_keys=ON` checks existing rows. It appears not to (unverified).

**Other rules**
- The parent key must be the PK or covered by UNIQUE with matching collation, and cannot be the rowid. A mismatch shows up as a "foreign key mismatch" DML error, not at CREATE time ([§3](https://www.sqlite.org/foreignkeys.html)).
- Child key columns are not indexed automatically. Without an index, each parent delete scans the child table.
- MATCH is parsed but not enforced ([§6](https://www.sqlite.org/foreignkeys.html)).

### 6. Go SQL parsers with CGO off

| Option | License | Maintenance | Coverage |
|---|---|---|---|
| [rqlite/sql](https://github.com/rqlite/sql) | MIT ([GitHub API](https://api.github.com/repos/rqlite/sql)) | Last push 2026-10-16, 0 open issues, not archived | Its README states the goal of full SQLite syntax. In `master`'s [ast.go](https://raw.githubusercontent.com/rqlite/sql/master/ast.go), `AlterTableStatement` covers only RENAME TO, RENAME COLUMN, ADD COLUMN and DROP COLUMN. There are no 3.53.0 forms. |
| [ANTLR grammars-v4 sql/sqlite](https://github.com/antlr/grammars-v4/tree/master/sql/sqlite) | MIT grammar ([SQLiteParser.g4](https://raw.githubusercontent.com/antlr/grammars-v4/master/sql/sqlite/SQLiteParser.g4)) | Unverified | `alter_table_stmt` includes ADD and DROP CONSTRAINT and ALTER COLUMN SET/DROP NOT NULL. You must generate the Go code with the ANTLR tool. The Go runtime's license and its CGO-free status are unverified. |
| sqlc's SQLite engine | n/a | n/a | It lives under [`internal/`](https://github.com/sqlc-dev/sqlc/tree/main/internal/engine/sqlite), so other modules cannot import it. |
| SQLite itself, through the modernc.org/sqlite dependency dbscope already has (bundles 3.54.1) | BSD-3 for modernc (unverified) | Active | Exact grammar for 3.54.1. Run the migration against a scratch in-memory copy of the schema, then compare the pragma output before and after. You get no AST. With no rows in the scratch copy, data-dependent failures are missed. Virtual tables whose module is missing may fail (unverified). |
| [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) | MIT | Pushed 2026-10-26 | A wasm-based, CGO-free engine, not a parser. Not needed, since modernc is already present. |
| liteparser, go-sqlite-createtable-parser | n/a | n/a | Both appear to be C or C bindings ([search results](https://github.com/sqliteai/liteparser)), so they need CGO. Excluded. |

**Verdict.** I found no pure-Go AST parser confirmed complete for 3.53/3.54 DDL. A workable option is rqlite/sql, or ANTLR, to classify statements, plus a dry run in a modernc scratch database to validate them. That choice belongs to the orchestrator.

### Facts dbscope must encode

- [ ] Use `pragma_table_xinfo`, not `table_info`. Hidden 1 = virtual table hidden column, 2 = VIRTUAL generated, 3 = STORED generated. cid differs between the two pragmas.
- [ ] Read STRICT and WITHOUT ROWID from `pragma_table_list` (`strict`, `wr`). The `type` column also gives view, virtual and shadow.
- [ ] Generated column expressions and AUTOINCREMENT appear only in `sqlite_schema.sql`.
- [ ] Rowid alias rule: rowid table, single-column PK, declared type exactly INTEGER, and not `INTEGER PRIMARY KEY DESC`.
- [ ] A WITHOUT ROWID table's PK has no `sqlite_schema` row. Autoindexes have NULL `sql`.
- [ ] Foreign key edges can point at missing tables or columns. Foreign keys cannot cross attached schemas.
- [ ] Pass the schema name to every pragma call, and enumerate schemas with `pragma_database_list`.
- [ ] ALTER version gates: RENAME COLUMN 3.25.0. FK rewrite on rename 3.26.0. DROP COLUMN 3.35.0, safe from 3.35.5. ADD COLUMN constraint checks 3.37.0. ALTER COLUMN NOT NULL and ADD/DROP CONSTRAINT 3.53.0. ROWID-name error 3.54.0.
- [ ] Flag ADD COLUMN that uses PK, UNIQUE, CURRENT_* or `(expr)` defaults, NOT NULL with no default, REFERENCES with a non-NULL default while foreign keys are on, or STORED generated.
- [ ] Flag DROP COLUMN on a column that is PK, UNIQUE, indexed, in a partial-index WHERE, in a CHECK, in a foreign key, in a generated expression, or in a trigger or view.
- [ ] Flag `PRAGMA foreign_keys=OFF` inside BEGIN or SAVEPOINT as a silent no-op. Flag DROP TABLE while foreign keys are on, since it can cascade.
- [ ] Recognize the 12-step order. Flag "rename old first".
- [ ] Rate `writable_schema`, `schema_version=`, `journal_mode=OFF/MEMORY`, `defer_foreign_keys=0` and VACUUM as high risk.
- [ ] A table copy or DROP COLUMN costs time and I/O proportional to table size. In rollback mode it blocks readers after a cache spill. In both modes it blocks other writers.
- [ ] VACUUM needs up to twice the file size in free disk, cannot run in a transaction, and may renumber rowids.
- [ ] Validate migration rules against the target app's SQLite version, not dbscope's 3.54.1.
