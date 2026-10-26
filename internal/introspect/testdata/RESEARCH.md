Let me check the PostgREST pg_depend issue and the Prisma introspection query source.## Result

### 1. How mature tools write the catalog queries

Sources read:
- supabase/postgres-meta: [columns.sql.ts](https://github.com/supabase/postgres-meta/blob/master/src/lib/sql/columns.sql.ts), [table.sql.ts](https://github.com/supabase/postgres-meta/blob/master/src/lib/sql/table.sql.ts), [table_relationships.sql.ts](https://github.com/supabase/postgres-meta/blob/master/src/lib/sql/table_relationships.sql.ts), [indexes.sql.ts](https://github.com/supabase/postgres-meta/blob/master/src/lib/sql/indexes.sql.ts), [views_key_dependencies.sql.ts](https://github.com/supabase/postgres-meta/blob/master/src/lib/sql/views_key_dependencies.sql.ts). The files are now `.sql.ts`, not `.sql`.
- pgAdmin 4: [columns/sql/default/properties.sql](https://github.com/pgadmin-org/pgadmin4/blob/master/web/pgadmin/browser/server_groups/servers/databases/schemas/tables/templates/columns/sql/default/properties.sql). The column templates are split into `default`, `12_plus`, `16_plus` and `17_plus` folders.
- Prisma: [tables_query.sql, indexes_query.sql, constraints_query.sql](https://github.com/prisma/prisma-engines/tree/main/schema-engine/sql-schema-describer/src/postgres), plus the FK query in [postgres.rs](https://github.com/prisma/prisma-engines/blob/main/schema-engine/sql-schema-describer/src/postgres.rs).
- drizzle-kit: [pgSerializer.ts](https://github.com/drizzle-team/drizzle-orm/blob/main/drizzle-kit/src/serializer/pgSerializer.ts).

**Where the tools agree:**
- **Columns.** They read `pg_attribute` filtered with `attnum > 0 AND NOT attisdropped`, joined to `pg_class`, `pg_namespace` and `pg_type`. Defaults come from `LEFT JOIN pg_attrdef ON adrelid=attrelid AND adnum=attnum` and `pg_get_expr(adbin, adrelid)`. Types come from `format_type(atttypid, atttypmod)`. Comments come from `col_description` / `obj_description` or a `pg_description` join (pgAdmin).
- **Relation kinds.** All of them filter on `relkind`: `('r','p')` for tables, plus `'v','m','f'` for views, matviews and foreign tables. Prisma drops partitions with `relkind='r' AND NOT relispartition`, keeping `relkind='p'` parents.
- **Identity and generated columns.** Identity is `attidentity IN ('a','d')`. Generated is `attgenerated IN ('s')` in both postgres-meta and pgAdmin. Based on these files, neither handles PG 18's `'v'` (virtual) value. drizzle-kit reads `information_schema.columns.is_generated` instead.
- **Constraints.** `pg_constraint` filtered by `contype`. Check text comes from `pg_get_constraintdef(oid, true)`. FK actions are decoded from the `confdeltype`/`confupdtype` letters (drizzle-kit, Prisma).
- **Composite FK column order.** postgres-meta `table_relationships` uses `unnest(conkey, confkey) WITH ORDINALITY` and filters `conparentid = 0`, which removes per-partition clones. Prisma uses `unnest` + `generate_subscripts` with an explicit `ORDER BY`. Its code comment says the optimizer may reorder unnested rows otherwise.
- **Indexes.** `pg_index` joined to `pg_class` (index), `pg_am` and `pg_attribute`. Prisma unnests `indkey`/`indclass`/`indoption` and decodes ASC/DESC and NULLS FIRST from the `indoption` bits. Predicates come from `pg_get_expr(indpred, indrelid)`. postgres-meta also returns `indisvalid` and `pg_indexes.indexdef`.
- **Privilege filter.** postgres-meta copies information_schema here: `pg_has_role(relowner,'USAGE') OR has_table_privilege(...) OR has_any_column_privilege(...)`, evaluated per row.

**Defects to avoid** (these are my reading of the files; I did not run them):
- **postgres-meta `table.sql.ts` and drizzle-kit FK queries.** Both join `attnum = ANY(conkey)` and `attnum = ANY(confkey)` independently. For composite FKs that appears to give a cross product and loses the column pairing.
- **postgres-meta `indexes.sql.ts`.** It joins `pg_attribute` on the index's own OID with `attnum = ANY(indkey)`, which mixes index and table attnums. It also joins `pg_indexes` on index name only, without schema.
- **drizzle-kit.** It issues several queries per table (an N+1 pattern), mixes in `information_schema` (which is privilege-filtered), and string-interpolates schema and table names.
- **postgres-meta view dependencies.** It parses `pg_rewrite.ev_action` text with chained `replace()`/`regexp_replace()` to get column lineage. This is fragile. Use `pg_depend` unless column-level key lineage is required.

### 2. Recommended SQL (parameter `$1 = text[]` of schema names)

Run all six in one `REPEATABLE READ READ ONLY` transaction, sent as one `pgx.Batch`. Choose the version-gated columns from `server_version_num`.

**(a) Relations**
```sql
SELECT c.oid, n.nspname AS schema, c.relname AS name, c.relkind::text AS kind,
       c.relispartition,
       inh.inhparent AS partition_parent,
       CASE WHEN c.relispartition THEN pg_get_expr(c.relpartbound, c.oid) END AS partition_bound,
       CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) END AS partition_key,
       c.reltuples::float8 AS reltuples,   -- < 0 means never analyzed (14+)
       c.relpages::int8 AS relpages,
       c.relpersistence::text AS persistence, c.relrowsecurity,
       d.description AS comment
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_inherits inh ON inh.inhrelid = c.oid AND c.relispartition
LEFT JOIN pg_description d ON d.objoid = c.oid
     AND d.classoid = 'pg_class'::regclass AND d.objsubid = 0
WHERE c.relkind IN ('r','p','v','m','f')
  AND n.nspname = ANY($1::text[])
ORDER BY n.nspname, c.relname;
```

**(b) Columns** (partition children skipped; their columns match the parent)
```sql
SELECT a.attrelid AS rel_oid, a.attnum::int AS num, a.attname AS name,
       format_type(a.atttypid, a.atttypmod) AS type,
       a.atttypid AS type_oid, t.typtype::text AS typtype, t.typelem AS elem_type_oid,
       a.attnotnull AS not_null,
       (t.typtype = 'd' AND t.typnotnull) AS domain_not_null,
       CASE WHEN a.attgenerated::text = '' THEN pg_get_expr(ad.adbin, ad.adrelid) END AS default_expr,
       CASE WHEN a.attgenerated::text <> '' THEN pg_get_expr(ad.adbin, ad.adrelid) END AS generated_expr,
       NULLIF(a.attidentity::text, '')  AS identity,   -- 'a' always | 'd' by default
       NULLIF(a.attgenerated::text, '') AS generated,  -- 's' stored | 'v' virtual (18+)
       a.attislocal, a.attinhcount::int AS inhcount,
       d.description AS comment
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
LEFT JOIN pg_description d ON d.objoid = a.attrelid
     AND d.classoid = 'pg_class'::regclass AND d.objsubid = a.attnum
WHERE a.attnum > 0 AND NOT a.attisdropped
  AND c.relkind IN ('r','p','v','m','f')
  AND NOT c.relispartition
  AND n.nspname = ANY($1::text[])
ORDER BY a.attrelid, a.attnum;
```

**(c) PK / FK / unique / check / exclusion constraints**
```sql
SELECT con.oid, con.conrelid AS rel_oid, con.conname AS name, con.contype::text AS type,
       con.condeferrable, con.condeferred, con.convalidated,
       ARRAY(SELECT a.attname
             FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
             ORDER BY k.ord) AS columns,
       NULLIF(con.confrelid, 0) AS ref_rel_oid,
       ARRAY(SELECT a.attname
             FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
             ORDER BY k.ord) AS ref_columns,          -- same ordinal = paired column
       CASE WHEN con.contype = 'f' THEN con.confdeltype::text END AS on_delete,  -- a r c n d
       CASE WHEN con.contype = 'f' THEN con.confupdtype::text END AS on_update,
       CASE WHEN con.contype = 'f' THEN con.confmatchtype::text END AS match_type,
       CASE WHEN con.contype IN ('c','x') THEN pg_get_constraintdef(con.oid, true) END AS definition,
       d.description AS comment
       -- PG 15+: , con.confdelsetcols
       -- PG 18+: , con.conenforced, con.conperiod
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_description d ON d.objoid = con.oid AND d.classoid = 'pg_constraint'::regclass
WHERE con.contype IN ('p','u','f','c','x')   -- excludes 'n' (18) and 't'
  AND con.conparentid = 0                     -- drop per-partition clones
  AND NOT c.relispartition
  AND n.nspname = ANY($1::text[])
ORDER BY con.conrelid, con.contype, con.conname;
```
`ref_rel_oid` can point into a schema outside `$1`. Resolve it client-side, or with one small follow-up query by OID.

**(d) Indexes**
```sql
SELECT ix.indexrelid AS index_oid, ix.indrelid AS rel_oid, ic.relname AS name,
       am.amname AS method,
       ix.indisunique, ix.indisprimary, ix.indisexclusion, ix.indisvalid, ix.indisready,
       ix.indnkeyatts::int AS key_count,        -- elements after key_count are INCLUDE cols
       ARRAY(SELECT CASE WHEN k.attnum = 0
                         THEN pg_get_indexdef(ix.indexrelid, k.ord::int, true)
                         ELSE (SELECT a.attname FROM pg_attribute a
                               WHERE a.attrelid = ix.indrelid AND a.attnum = k.attnum) END
             FROM unnest(ix.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
             ORDER BY k.ord) AS elements,
       ix.indoption::int2[] AS options,         -- bit 1 = DESC, bit 2 = NULLS FIRST (btree)
       pg_get_expr(ix.indpred, ix.indrelid, true) AS predicate,
       con.oid AS constraint_oid
       -- PG 15+: , ix.indnullsnotdistinct
FROM pg_index ix
JOIN pg_class ic ON ic.oid = ix.indexrelid
JOIN pg_class c  ON c.oid = ix.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_am am ON am.oid = ic.relam
LEFT JOIN pg_constraint con ON con.conindid = ix.indexrelid
     AND con.conrelid = ix.indrelid AND con.contype IN ('p','u','x')
WHERE NOT c.relispartition
  AND n.nspname = ANY($1::text[])
ORDER BY ix.indrelid, ic.relname;
```
The extra `con.conrelid = ix.indrelid` condition matters. An FK's `conindid` points at the referenced table's unique index, so without it that index would match the FK.

**(e) View and matview dependencies on base relations**
```sql
SELECT v.oid AS view_oid, v.relkind::text AS view_kind,
       d.refobjid AS base_oid,
       array_agg(DISTINCT d.refobjsubid::int) FILTER (WHERE d.refobjsubid > 0) AS base_attnums
FROM pg_class v
JOIN pg_namespace n ON n.oid = v.relnamespace
JOIN pg_rewrite r ON r.ev_class = v.oid AND r.rulename = '_RETURN'
JOIN pg_depend d ON d.classid = 'pg_rewrite'::regclass AND d.objid = r.oid
                AND d.refclassid = 'pg_class'::regclass AND d.refobjid <> v.oid
                AND d.deptype = 'n'
WHERE v.relkind IN ('v','m')
  AND n.nspname = ANY($1::text[])
GROUP BY v.oid, v.relkind, d.refobjid;
```
The [pg_depend docs](https://www.postgresql.org/docs/18/catalog-pg-depend.html) explain why this works. A view's ON SELECT rule depends internally on the view, and the rule's own dependencies (the tables it reads) act as the view's dependencies.

**(f) Enums**
```sql
SELECT t.oid AS type_oid, n.nspname AS schema, t.typname AS name,
       array_agg(e.enumlabel ORDER BY e.enumsortorder) AS labels
FROM pg_type t
JOIN pg_namespace n ON n.oid = t.typnamespace
JOIN pg_enum e ON e.enumtypid = t.oid
WHERE n.nspname = ANY($1::text[])
GROUP BY t.oid, n.nspname, t.typname;
```
Enum types used by columns can live in other schemas, and array columns point to the array type (`typelem`). A safer filter is `t.oid = ANY($2::oid[])`, with the type OIDs collected from query (b).

**Version notes (14 to 18), from the per-version catalog docs:**
- **`reltuples`.** The [PG 14 docs](https://www.postgresql.org/docs/14/catalog-pg-class.html) say -1 means never vacuumed or analyzed. The [PG 13 docs](https://www.postgresql.org/docs/13/catalog-pg-class.html) have no such note. Treat values below 0 as unknown.
- **`attgenerated`.** In 14 the only value is `s`. In [18](https://www.postgresql.org/docs/18/catalog-pg-attribute.html) it is `s` or `v`, and [CREATE TABLE in 18](https://www.postgresql.org/docs/18/sql-createtable.html) says VIRTUAL is the default when neither keyword is given. Virtual columns are physically stored as null. The expression is in `pg_attrdef`, as for stored columns.
- **`pg_constraint.contype = 'n'`.** In 17 this is domains only. In [18](https://www.postgresql.org/docs/18/catalog-pg-constraint.html) table NOT NULL constraints also appear here. Filter them out, as (c) does.
- **`conenforced` and `conperiod`.** Present in 18, absent from the [17 docs](https://www.postgresql.org/docs/17/catalog-pg-constraint.html).
- **`confdelsetcols`.** Present in 17 and 18, absent from the 14 docs, so treat it as 15+ (not checked on the 15 page itself).
- **`attnotnull`.** In 18 it means "has a (possibly invalid) not-null constraint". For validation state, join the `contype='n'` row's `convalidated` (18 only). The `convalidated` note "can be false only for FKs and CHECK" appears in 14 and 17 but not in 18.
- **`indnullsnotdistinct`.** Present in the 15 docs, absent from 14.
- **Integer widths changed.** `coninhcount` is int4 in 14 and int2 in 17/18. `attinhcount` and `attndims` are int4 in 14 and int2 in 18. `attstattarget` is int4 in 14 and int2, nullable, in 18. Cast in SQL (as above) so the pgx scan targets stay the same. Never `SELECT *` from catalogs: `attcacheoff` exists in 14 and is gone in 18, and 18 adds `relallfrozen`.
- **Stable across 14 to 18:** `relkind` letters, `confdeltype`/`confupdtype` codes, `attidentity` codes, and `unnest ... WITH ORDINALITY`.

### 3. Privileges

- **Core catalogs.** A SELECT-only role can, as far as I can tell, read every catalog used above: `pg_class`, `pg_attribute`, `pg_attrdef`, `pg_constraint`, `pg_index`, `pg_inherits`, `pg_depend`, `pg_rewrite`, `pg_type`, `pg_enum`, `pg_namespace`, `pg_description` and `pg_am`. The `pg_get_*`, `format_type` and `obj_description` functions should also work. This is standard PostgreSQL behavior and not stated in one doc sentence; check it with `\dp pg_catalog.pg_class`.
- **Exceptions in core.** [`pg_statistic`](https://www.postgresql.org/docs/18/catalog-pg-statistic.html) is not public; `pg_stats` shows only tables the user can read. `pg_authid` is not public; `pg_roles` blanks the password.
- **Without USAGE on a schema.** That schema's objects are still listed in `pg_class` and `pg_attribute`, because catalogs have no row filtering. They are hidden from `information_schema`: its [`tables` view](https://www.postgresql.org/docs/18/infoschema-tables.html) shows only objects the user owns or has some privilege on. They are also hidden from `pg_stats`, and from any tool that filters with `has_*_privilege` (postgres-meta does). The [schema docs](https://www.postgresql.org/docs/18/ddl-schemas.html) say USAGE is needed to access objects in the schema.
- **Name-based lookups.** Text-to-regclass casts such as `'s.t'::regclass`, and functions given `format('%I.%I')` names (postgres-meta's `pg_total_relation_size`), appear to need USAGE and may error. Use OIDs throughout.
- **RDS and Aurora.** AWS says RDS [restricts access to certain system procedures and tables that require advanced privileges](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/CHAP_PostgreSQL.html). The known case is `pg_authid`, which even `rds_superuser` cannot read (StackOverflow, not AWS docs). None of the catalogs above appear to be restricted.
- **Supabase.** Supabase [documents](https://supabase.com/docs/guides/troubleshooting/custom-role-inherits-privileges-that-were-not-explicitly-granted-ddaa1c) that a custom role inherits access it was not explicitly granted. For example, `has_table_privilege` returns true for objects in the `net` schema. So a privilege-based filter will pull in platform schemas; exclude them by name instead. Supabase's April 2025 restrictions on the auth, storage and realtime schemas concern changing those objects. I did not find any restriction on reading catalogs.
- **Neon.** Roles created with SQL get the same privileges as in standalone Postgres ([Neon compatibility page](https://neon.com/docs/reference/compatibility)). I found no documented catalog-read restriction.
- **Optional grants.** If row counts from `pg_stat_*` are needed, [`pg_read_all_stats`](https://www.postgresql.org/docs/18/predefined-roles.html) grants that without data access. `pg_read_all_data` grants SELECT and schema USAGE everywhere, which is more than dbscope needs.

### 4. Performance

- **Per-entity queries over N+1.** Prisma runs one query per catalog, filtered by `nspname = ANY($1)`. drizzle-kit runs several queries per table. The per-catalog form is the one that scales. Use about 6 queries joined client-side by OID, sent as one `pgx.Batch`, which should take roughly one network round trip. One giant query with nested JSON aggregation (postgres-meta `table.sql.ts` style) makes the planner join many catalogs at once and is harder to tune.
- **Partitions.** These dominate the 1,000-partition case. Filter `NOT relispartition` in the column, constraint and index queries, and `conparentid = 0` for FKs (postgres-meta does this). That leaves one cheap row per partition in query (a).
- **`pg_get_*` calls.** They decompile node trees on every call. Only call them where needed: defaults, check and exclusion definitions, predicates and expression elements. Skip `pg_get_indexdef` for whole indexes and `pg_get_viewdef` unless the UI asks for them. postgres-meta's maintainers note that their `ev_action` parser was heavily tuned to run on schemas with 250+ views.
- **Privilege checks.** `has_*_privilege` and `pg_has_role` are evaluated once per row. postgres-meta calls `has_column_privilege` for every column. Skip privilege filtering, since it is not needed to read catalogs. If needed, compute `has_table_privilege` once per relation in query (a).
- **Catalog bloat.** [PostgREST #3212](https://github.com/PostgREST/postgrest/issues/3212) reports a `pg_depend` with about 1.07M dead tuples making a schema-cache query take about 4.6 s. Set `SET LOCAL statement_timeout` and report a clear error. Avoid `pg_total_relation_size`, which postgres-meta calls per table, and use `relpages` instead.
- **Reference point.** [PostgREST #3704](https://github.com/PostgREST/postgrest/issues/3704) logs "Schema cache queried in 547.9 milliseconds" for 1,286 relations and 4,921 relationships on PG 13. That suggests dbscope's targets are achievable with catalog queries alone.

### 5. Inferring missing foreign keys

- **SchemaSpy implied relationships** (v5 `DbAnalyzer.getImpliedConstraints`, read from a third-party [mirror](https://github.com/infoplat/SchemaSpy_5.0.0)). It collects single-column PKs keyed by name (case-insensitive), type and length. Any non-FK column elsewhere with the same name, type and length becomes an implied FK to that PK, unless the reverse link already exists. It gives up entirely when duplicate PK names outnumber distinct ones, for example when every PK is named `id`.
- **SchemaSpy Rails mode** (current [master](https://github.com/schemaspy/schemaspy/blob/master/src/main/java/org/schemaspy/DbAnalyzer.java), [NmPrimaryTable](https://github.com/schemaspy/schemaspy/blob/master/src/main/java/org/schemaspy/util/rails/NmPrimaryTable.java)). A column ending in `_id` has the suffix stripped and the rest pluralized to find the target table, then links to that table's PK. It skips columns that are already FKs.
- **tbls `detectVirtualRelations`** ([README](https://github.com/k1LoW/tbls)). Strategies: `default` (`x.user_id` to `users.id`), `singularTableName` (`user.id`), `identical` (`users.user_id`), `identicalSingularTableName`, and `invertedSingularTableName` (`id_user` to `user.id`).
- **DBeaver.** [Virtual keys](https://dbeaver.com/docs/dbeaver/Virtual-Keys/) are user-defined and stored in `data-sources.json`, not inferred. Relationships drawn in custom diagrams are saved as virtual FKs.
- **Liam ERD.** I found no inference logic in its docs or repo. It announces tbls integration, so tbls-detected relations might flow through; this is unverified.
- **Suggested scoring for dbscope** (my synthesis, not taken from one tool):
  - Candidate columns: `<x>_id`, `<x>Id`, or a column named like the target's PK.
  - Target table: singular or plural of `<x>`, same schema first.
  - The target column must be a single-column PK or unique key, and the types must match (same OID, or same integer family, or both uuid).
  - Skip columns already covered by a declared FK.
  - Treat a sibling `<x>_type` column as a Rails polymorphic association, not an FK.
  - Raise confidence when the child column is indexed.
  - Show inferred links with a distinct style (for example dashed) and let users override them.

### Unverified

- That every core catalog above is `GRANT SELECT TO PUBLIC` on all of 14 to 18 and on RDS, Aurora, Supabase and Neon. Probe at startup.
- That a text-to-regclass cast fails without schema USAGE. This is based on how namespace lookup works; not tested.
- Whether `pg_get_viewdef` and `pg_get_expr` succeed on objects in schemas without USAGE.
- What `reltuples` holds for partitioned parents (`relkind = 'p'`). Summing the children's `GREATEST(reltuples, 0)` is safer.
- Whether `pg_get_*` decompilers read through the system cache instead of the transaction snapshot, which could give inconsistent output under concurrent DDL.
- Whether `confdeltype` is a space or a zero byte on non-FK constraints. The SQL above avoids depending on it.
- Exactly which major version (15, 16 or 17) changed each integer width. I only compared the 14, 17 and 18 docs.
- drizzle-kit's handling of PG 18 virtual columns. A newer drizzle-kit v1 introspector may differ from the file I read.
- SchemaSpy CLI flag names for implied and Rails modes, and whether current master still runs the v5 implied algorithm.
- Liam ERD inference, and RDS restrictions beyond `pg_authid`.
- Per-row costs of `has_*_privilege` and `pg_get_*`. These are qualitative; benchmark them.
- None of the SQL was executed. No shell or database was available.

```json
{"status":"done","summary":"Research only, no code or files written. Read postgres-meta (.sql.ts files), a pgAdmin 4 column template, Prisma's schema-describer SQL, drizzle-kit's pgSerializer, the PostgreSQL 13/14/15/17/18 catalog docs, PostgREST issues, SchemaSpy source, the tbls README and DBeaver docs. The tools converge on pg_class/pg_attribute/pg_constraint/pg_index joins with attnum>0, NOT attisdropped, relkind filters, format_type, pg_get_expr and pg_get_constraintdef. Several have composite-FK cross-product bugs, and postgres-meta and pgAdmin only test attgenerated='s'. Provides six SQL queries for relations, columns, constraints, indexes, view dependencies and enums, with notes on what changes across 14-18: reltuples -1 from 14, virtual generated columns ('v', and the default) in 18, contype 'n' table not-null plus conenforced/conperiod in 18, confdelsetcols and indnullsnotdistinct from 15, integer width changes. Covers privilege behavior and managed-provider notes, performance guidance (per-catalog queries in one pgx batch, skip partitions and conparentid<>0, avoid per-row privilege checks, watch pg_depend bloat), and FK-inference heuristics from SchemaSpy, tbls and DBeaver.","artifacts":[],"tests":{"command":"","passed":null,"output_tail":"No SQL executed; shell and database access unavailable."},"claims_to_verify":["PG18 pg_attribute.attgenerated allows 'v' and CREATE TABLE in 18 says VIRTUAL is the default","PG18 pg_constraint has conenforced and conperiod, which are absent in the PG17 docs","PG18 contype 'n' covers table not-null constraints; PG17 says domains only","pg_constraint.confdelsetcols is absent from the PG14 docs; pg_index.indnullsnotdistinct is in PG15 and absent from PG14","PG14 docs state reltuples = -1 for never-analyzed tables; the PG13 docs do not","postgres-meta columns.sql.ts and pgAdmin properties.sql only test attgenerated IN ('s')","postgres-meta table.sql.ts and drizzle-kit FK queries join conkey and confkey via independent ANY(), giving a cross product for composite FKs","Every core catalog used is readable by a fresh SELECT-only role on PG 14-18 and on RDS, Aurora, Supabase and Neon","All six SQL blocks run without error on PG 14 and PG 18 (with the version-gated columns added only on 15+/18+)","An FK's conindid can point to the referenced table's unique index, so the index-to-constraint join needs conrelid = indrelid"],"open_questions":["Should dbscope show partition-local constraints and indexes that are not inherited from the parent? The recommended queries skip them.","Should inferred FKs be on by default, or opt-in like tbls detectVirtualRelations?"]}
```