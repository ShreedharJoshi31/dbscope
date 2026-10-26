package introspect

// The catalog queries follow testdata/RESEARCH.md section 2. $1 is the
// text[] of selected schemas. Integer catalog columns whose width changed
// between 14 and 18 are cast, and nullable text is coalesced to ''.

// nonSystem matches the schemas a user can create: names starting with pg_
// are reserved, which covers pg_catalog, pg_toast, pg_temp_N and pg_toast_temp_N.
const nonSystem = `n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'`

// setupQuery reads the server version, applies the statement timeout to the
// rest of the transaction, and lists the schemas a default selection covers:
// non-system schemas the role has USAGE on.
const setupQuery = `SELECT current_setting('server_version_num')::int,
       set_config('statement_timeout', coalesce($1::text, current_setting('statement_timeout')), true),
       ARRAY(SELECT n.nspname::text FROM pg_namespace n
             WHERE ` + nonSystem + ` AND has_schema_privilege(n.oid, 'USAGE')
             ORDER BY n.nspname)`

const namespacesQuery = `SELECT n.nspname, count(c.oid)::int
FROM pg_namespace n
LEFT JOIN pg_class c ON c.relnamespace = n.oid
     AND c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition
WHERE ` + nonSystem + ` AND has_schema_privilege(n.oid, 'USAGE')
GROUP BY n.nspname
ORDER BY n.nspname`

// relationsQuery also returns partitions living outside the selected schemas
// when their partition root is selected, so the root's row estimate is complete.
const relationsQuery = `SELECT c.oid, n.nspname, c.relname, c.relkind::text, c.relispartition,
       CASE WHEN c.relispartition THEN pg_partition_root(c.oid)::oid ELSE 0::oid END AS root,
       CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) ELSE '' END AS partition_key,
       c.reltuples::float8, c.relpages::int8,
       coalesce(d.description, '')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_description d ON d.objoid = c.oid
     AND d.classoid = 'pg_class'::regclass AND d.objsubid = 0
WHERE c.relkind IN ('r','p','v','m','f')
  AND (n.nspname = ANY($1::text[])
       OR (c.relispartition AND pg_partition_root(c.oid)::oid IN (
             SELECT r.oid FROM pg_class r JOIN pg_namespace rn ON rn.oid = r.relnamespace
             WHERE r.relkind = 'p' AND NOT r.relispartition AND rn.nspname = ANY($1::text[]))))
ORDER BY n.nspname, c.relname`

const columnsQuery = `SELECT a.attrelid, a.attname, format_type(a.atttypid, a.atttypmod),
       a.attnotnull OR (t.typtype = 'd' AND t.typnotnull),
       coalesce(pg_get_expr(ad.adbin, ad.adrelid), ''),
       a.attidentity::text, a.attgenerated::text,
       coalesce(d.description, '')
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
ORDER BY a.attrelid, a.attnum`

// constraintsQuery pairs FK columns by ordinal; conparentid = 0 drops the
// per-partition clones. contype 'n' (table NOT NULL in 18) is excluded.
const constraintsQuery = `SELECT con.conrelid, con.conname, con.contype::text, con.convalidated,
       ARRAY(SELECT a.attname::text
             FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
             ORDER BY k.ord) AS columns,
       con.confrelid,
       ARRAY(SELECT a.attname::text
             FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
             ORDER BY k.ord) AS ref_columns,
       CASE WHEN con.contype = 'f' THEN con.confdeltype::text ELSE '' END,
       CASE WHEN con.contype = 'f' THEN con.confupdtype::text ELSE '' END,
       CASE WHEN con.contype = 'c' THEN pg_get_expr(con.conbin, con.conrelid, true) ELSE '' END
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE con.contype IN ('p','u','f','c')
  AND con.conparentid = 0
  AND NOT c.relispartition
  AND n.nspname = ANY($1::text[])
ORDER BY con.conrelid, con.contype, con.conname`

// indexesQuery returns key columns then INCLUDE columns in one array;
// indnkeyatts says where the keys end. Expression elements are decompiled.
const indexesQuery = `SELECT ix.indrelid, ic.relname, coalesce(am.amname::text, ''),
       ix.indisunique, ix.indisprimary, ix.indisvalid, ix.indnkeyatts::int,
       ARRAY(SELECT CASE WHEN k.attnum = 0
                         THEN pg_get_indexdef(ix.indexrelid, k.ord::int, true)
                         ELSE (SELECT a.attname::text FROM pg_attribute a
                               WHERE a.attrelid = ix.indrelid AND a.attnum = k.attnum) END
             FROM unnest(ix.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
             ORDER BY k.ord) AS elements,
       coalesce(pg_get_expr(ix.indpred, ix.indrelid, true), '')
FROM pg_index ix
JOIN pg_class ic ON ic.oid = ix.indexrelid
JOIN pg_class c  ON c.oid = ix.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_am am ON am.oid = ic.relam
WHERE NOT c.relispartition
  AND n.nspname = ANY($1::text[])
ORDER BY ix.indrelid, ic.relname`

// viewDepsQuery reads the relations a view's ON SELECT rule depends on; the
// base may live in any schema, so its name is returned with it.
const viewDepsQuery = `SELECT DISTINCT v.oid, b.oid, bn.nspname, b.relname
FROM pg_class v
JOIN pg_namespace n ON n.oid = v.relnamespace
JOIN pg_rewrite r ON r.ev_class = v.oid AND r.rulename = '_RETURN'
JOIN pg_depend d ON d.classid = 'pg_rewrite'::regclass AND d.objid = r.oid
                AND d.refclassid = 'pg_class'::regclass AND d.refobjid <> v.oid
                AND d.deptype = 'n'
JOIN pg_class b ON b.oid = d.refobjid
JOIN pg_namespace bn ON bn.oid = b.relnamespace
WHERE v.relkind IN ('v','m')
  AND b.relkind IN ('r','p','v','m','f')
  AND n.nspname = ANY($1::text[])
ORDER BY v.oid, bn.nspname, b.relname`

// enumsQuery returns enums in the selected schemas plus enums (or arrays of
// enums) that selected columns use from other schemas.
const enumsQuery = `SELECT n.nspname, t.typname, array_agg(e.enumlabel::text ORDER BY e.enumsortorder)
FROM pg_type t
JOIN pg_namespace n ON n.oid = t.typnamespace
JOIN pg_enum e ON e.enumtypid = t.oid
WHERE n.nspname = ANY($1::text[])
   OR t.oid IN (SELECT coalesce(nullif(ty.typelem, 0), ty.oid)
                FROM pg_attribute a
                JOIN pg_class c ON c.oid = a.attrelid
                JOIN pg_namespace cn ON cn.oid = c.relnamespace
                JOIN pg_type ty ON ty.oid = a.atttypid
                WHERE a.attnum > 0 AND NOT a.attisdropped AND cn.nspname = ANY($1::text[]))
GROUP BY n.nspname, t.typname
ORDER BY n.nspname, t.typname`

// stubsQuery fetches FK targets outside the selected schemas by OID.
const stubsQuery = `SELECT c.oid, n.nspname, c.relname, c.relkind::text,
       ARRAY(SELECT a.attname::text
             FROM pg_constraint con
             CROSS JOIN unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
             WHERE con.conrelid = c.oid AND con.contype = 'p'
             ORDER BY k.ord)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.oid = ANY($1::oid[])
ORDER BY n.nspname, c.relname`
