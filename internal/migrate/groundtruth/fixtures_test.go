package groundtruth

// schemaSQL is the populated schema every fixture starts from: a 10k-row
// table gt.t with indexes, an FK to gt.parent, an FK child gt.child, a view
// and a materialized view on it, a trigger, an enum and a constrained
// domain, and a range-partitioned table with a default partition, an FK
// child and a table ready to attach.
const schemaSQL = `
CREATE SCHEMA gt;
CREATE TYPE gt.mood AS ENUM ('ok', 'meh');
CREATE DOMAIN gt.posint AS integer CHECK (VALUE > 0);

CREATE TABLE gt.parent (id bigint PRIMARY KEY, code text NOT NULL UNIQUE);
INSERT INTO gt.parent SELECT g, 'c' || g FROM generate_series(1, 100) g;

CREATE TABLE gt.t (
    id bigint PRIMARY KEY,
    parent_id bigint REFERENCES gt.parent (id),
    v varchar(20),
    txt text,
    n numeric(10,2),
    i integer,
    c text,
    mood gt.mood DEFAULT 'ok',
    nn text,
    created_at timestamptz DEFAULT now()
);
INSERT INTO gt.t SELECT g, 1 + g % 100, 'v' || g, 't' || g, g / 10.0, g, 'c' || g, 'ok', 'x' || g, now()
FROM generate_series(1, 10000) g;
CREATE INDEX t_i_idx ON gt.t (i);
CREATE INDEX t_v_idx ON gt.t (v);
CREATE INDEX t_n_idx ON gt.t (n);
CREATE UNIQUE INDEX t_c_uidx ON gt.t (c);
ALTER TABLE gt.t CLUSTER ON t_i_idx;

CREATE TABLE gt.child (id bigint PRIMARY KEY, t_id bigint REFERENCES gt.t (id));
INSERT INTO gt.child SELECT g, g FROM generate_series(1, 10000) g;

CREATE TABLE gt.nopk (id bigint NOT NULL, x integer);
INSERT INTO gt.nopk SELECT g, g FROM generate_series(1, 10000) g;

CREATE VIEW gt.t_view AS SELECT id, c FROM gt.t;
CREATE MATERIALIZED VIEW gt.t_mv AS SELECT id, parent_id FROM gt.t;
CREATE UNIQUE INDEX t_mv_id ON gt.t_mv (id);

CREATE FUNCTION gt.trg() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$;
CREATE TRIGGER t_trg BEFORE UPDATE ON gt.t FOR EACH ROW EXECUTE FUNCTION gt.trg();

CREATE TABLE gt.p (id bigint NOT NULL, at date NOT NULL, note text, PRIMARY KEY (id, at)) PARTITION BY RANGE (at);
CREATE TABLE gt.p_2025 PARTITION OF gt.p FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');
CREATE TABLE gt.p_2026 PARTITION OF gt.p FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
CREATE TABLE gt.p_default PARTITION OF gt.p DEFAULT;
INSERT INTO gt.p SELECT g, date '2025-01-01' + (g % 700), 'n' FROM generate_series(1, 10000) g;
CREATE TABLE gt.p_new (id bigint NOT NULL, at date NOT NULL, note text, PRIMARY KEY (id, at));
INSERT INTO gt.p_new SELECT g, date '2027-01-01' + (g % 300), 'n' FROM generate_series(1, 1000) g;
CREATE TABLE gt.p_ref (id bigint PRIMARY KEY, p_id bigint, p_at date, FOREIGN KEY (p_id, p_at) REFERENCES gt.p (id, at));
INSERT INTO gt.p_ref SELECT g, g, date '2025-01-01' + (g % 700) FROM generate_series(366, 465) g;

ANALYZE;
`

// fixture is one DDL statement to measure.
type fixture struct {
	Name string
	// Setup runs (committed) in the fixture's copy of the schema first.
	Setup string
	SQL   string
	// Target is the relation whose storage and scans are checked (default gt.t).
	Target string
	// Block is the relation a third session locks (EXCLUSIVE) to catch the lock request
	// of a statement that cannot run in a transaction (default Target).
	Block string
	// MinMajor skips older servers (syntax or features they lack).
	MinMajor int
	// Rules names the rule rows for SQL the PostgreSQL 17 grammar cannot parse.
	Rules []string
	// Tablespace: the fixture needs the in-place tablespace (PostgreSQL 15+).
	Tablespace bool
}

const tablespace = "dbscope_gt_ts"

// fixtures cover every row of docs/research-migrations.md section 1.2 and
// the extra rows of the rules table.
var fixtures = []fixture{
	{Name: "create_index", SQL: "CREATE INDEX t_txt_idx ON gt.t (txt)"},
	{Name: "create_index_concurrently", SQL: "CREATE INDEX CONCURRENTLY t_txt_cidx ON gt.t (txt)"},
	{Name: "create_index_partitioned", SQL: "CREATE INDEX p_note_idx ON gt.p (note)", Target: "gt.p_2025"},
	{Name: "create_index_concurrently_partitioned", SQL: "CREATE INDEX CONCURRENTLY p_note_cidx ON gt.p (note)", Target: "gt.p"},
	{Name: "drop_index", SQL: "DROP INDEX gt.t_v_idx"},
	{Name: "drop_index_concurrently", SQL: "DROP INDEX CONCURRENTLY gt.t_v_idx"},
	{Name: "reindex_index", SQL: "REINDEX INDEX gt.t_v_idx"},
	{Name: "reindex_table", SQL: "REINDEX TABLE gt.t"},
	{Name: "reindex_index_concurrently", SQL: "REINDEX INDEX CONCURRENTLY gt.t_v_idx"},
	{Name: "rename_index", SQL: "ALTER INDEX gt.t_v_idx RENAME TO t_v_idx2"},

	{Name: "add_column", SQL: "ALTER TABLE gt.t ADD COLUMN a1 integer"},
	{Name: "add_column_constant_default", SQL: "ALTER TABLE gt.t ADD COLUMN a2 integer DEFAULT 42"},
	{Name: "add_column_now_default", SQL: "ALTER TABLE gt.t ADD COLUMN a3 timestamptz DEFAULT now()"},
	{Name: "add_column_clock_timestamp", SQL: "ALTER TABLE gt.t ADD COLUMN a4 timestamptz DEFAULT clock_timestamp()"},
	{Name: "add_column_gen_random_uuid", SQL: "ALTER TABLE gt.t ADD COLUMN a5 uuid DEFAULT gen_random_uuid()"},
	{Name: "add_column_serial", SQL: "ALTER TABLE gt.t ADD COLUMN a6 serial"},
	{Name: "add_column_identity", SQL: "ALTER TABLE gt.t ADD COLUMN a7 bigint GENERATED ALWAYS AS IDENTITY"},
	{Name: "add_column_generated_stored", SQL: "ALTER TABLE gt.t ADD COLUMN a8 integer GENERATED ALWAYS AS (i * 2) STORED"},
	{Name: "add_column_generated_virtual", SQL: "ALTER TABLE gt.t ADD COLUMN a9 integer GENERATED ALWAYS AS (i * 2) VIRTUAL",
		MinMajor: 18, Rules: []string{"add_column.generated_virtual"}},
	{Name: "add_column_constrained_domain", SQL: "ALTER TABLE gt.t ADD COLUMN a10 gt.posint"},
	{Name: "add_column_not_null_constant_default", SQL: "ALTER TABLE gt.t ADD COLUMN a11 integer NOT NULL DEFAULT 0"},
	{Name: "add_column_not_null_no_default", SQL: "ALTER TABLE gt.t ADD COLUMN a12 integer NOT NULL"},
	{Name: "drop_column", SQL: "ALTER TABLE gt.t DROP COLUMN txt"},

	{Name: "type_varchar_widen", SQL: "ALTER TABLE gt.t ALTER COLUMN v TYPE varchar(40)"},
	{Name: "type_varchar_to_text", SQL: "ALTER TABLE gt.t ALTER COLUMN v TYPE text"},
	{Name: "type_text_to_varchar_n", SQL: "ALTER TABLE gt.t ALTER COLUMN txt TYPE varchar(100)"},
	{Name: "type_varchar_narrow", SQL: "ALTER TABLE gt.t ALTER COLUMN v TYPE varchar(10)"},
	{Name: "type_int_to_bigint", SQL: "ALTER TABLE gt.t ALTER COLUMN i TYPE bigint"},
	{Name: "type_numeric_precision_up", SQL: "ALTER TABLE gt.t ALTER COLUMN n TYPE numeric(12,2)"},
	{Name: "type_numeric_scale_change", SQL: "ALTER TABLE gt.t ALTER COLUMN n TYPE numeric(12,3)"},
	{Name: "type_using_expression", SQL: "ALTER TABLE gt.t ALTER COLUMN i TYPE bigint USING i + 0"},

	{Name: "set_not_null", SQL: "ALTER TABLE gt.t ALTER COLUMN nn SET NOT NULL"},
	{Name: "set_not_null_proven_by_check", Setup: "ALTER TABLE gt.t ADD CONSTRAINT t_nn_check CHECK (nn IS NOT NULL)",
		SQL: "ALTER TABLE gt.t ALTER COLUMN nn SET NOT NULL"},
	{Name: "add_not_null_not_valid", SQL: "ALTER TABLE gt.t ADD CONSTRAINT t_nn_nn NOT NULL nn NOT VALID",
		MinMajor: 18, Rules: []string{"add_not_null.not_valid"}},
	{Name: "drop_not_null", Setup: "ALTER TABLE gt.t ALTER COLUMN nn SET NOT NULL", SQL: "ALTER TABLE gt.t ALTER COLUMN nn DROP NOT NULL"},
	{Name: "set_default", SQL: "ALTER TABLE gt.t ALTER COLUMN i SET DEFAULT 7"},
	{Name: "drop_default", SQL: "ALTER TABLE gt.t ALTER COLUMN created_at DROP DEFAULT"},
	{Name: "set_statistics", SQL: "ALTER TABLE gt.t ALTER COLUMN i SET STATISTICS 500"},

	{Name: "add_fk", SQL: "ALTER TABLE gt.t ADD CONSTRAINT t_parent_fk2 FOREIGN KEY (parent_id) REFERENCES gt.parent (id)"},
	{Name: "add_fk_not_valid", SQL: "ALTER TABLE gt.t ADD CONSTRAINT t_parent_fk2 FOREIGN KEY (parent_id) REFERENCES gt.parent (id) NOT VALID"},
	{Name: "add_fk_partitioned", Setup: "CREATE TABLE gt.ids AS SELECT g AS id FROM generate_series(1, 10000) g; ALTER TABLE gt.ids ADD PRIMARY KEY (id)",
		SQL: "ALTER TABLE gt.p ADD CONSTRAINT p_ids_fk FOREIGN KEY (id) REFERENCES gt.ids (id)", Target: "gt.p_2025"},
	{Name: "add_fk_not_valid_partitioned", SQL: "ALTER TABLE gt.p ADD CONSTRAINT p_parent_fk FOREIGN KEY (id) REFERENCES gt.parent (id) NOT VALID",
		Target: "gt.p"},
	{Name: "add_check", SQL: "ALTER TABLE gt.t ADD CONSTRAINT t_i_pos CHECK (i > 0)"},
	{Name: "add_check_not_valid", SQL: "ALTER TABLE gt.t ADD CONSTRAINT t_i_pos CHECK (i > 0) NOT VALID"},
	{Name: "add_check_not_enforced", SQL: "ALTER TABLE gt.t ADD CONSTRAINT t_i_pos CHECK (i > 0) NOT ENFORCED",
		MinMajor: 18, Rules: []string{"add_check.not_enforced"}},
	{Name: "add_unique", SQL: "ALTER TABLE gt.t ADD CONSTRAINT t_txt_key UNIQUE (txt)"},
	{Name: "add_primary_key", SQL: "ALTER TABLE gt.nopk ADD PRIMARY KEY (id)", Target: "gt.nopk"},
	{Name: "add_unique_using_index", SQL: "ALTER TABLE gt.t ADD CONSTRAINT t_c_key UNIQUE USING INDEX t_c_uidx"},
	{Name: "add_primary_key_using_index", Setup: "CREATE UNIQUE INDEX nopk_id_uidx ON gt.nopk (id)",
		SQL: "ALTER TABLE gt.nopk ADD CONSTRAINT nopk_pkey PRIMARY KEY USING INDEX nopk_id_uidx", Target: "gt.nopk"},
	{Name: "add_primary_key_using_index_nullable", Setup: "CREATE UNIQUE INDEX nopk_x_uidx ON gt.nopk (x)",
		SQL: "ALTER TABLE gt.nopk ADD CONSTRAINT nopk_x_pkey PRIMARY KEY USING INDEX nopk_x_uidx", Target: "gt.nopk"},
	{Name: "add_exclude", SQL: "ALTER TABLE gt.t ADD CONSTRAINT t_i_excl EXCLUDE USING btree (i WITH =)"},
	{Name: "validate_check", Setup: "ALTER TABLE gt.t ADD CONSTRAINT t_i_pos CHECK (i > 0) NOT VALID",
		SQL: "ALTER TABLE gt.t VALIDATE CONSTRAINT t_i_pos"},
	{Name: "validate_fk", Setup: "ALTER TABLE gt.t ADD CONSTRAINT t_parent_fk2 FOREIGN KEY (parent_id) REFERENCES gt.parent (id) NOT VALID",
		SQL: "ALTER TABLE gt.t VALIDATE CONSTRAINT t_parent_fk2"},
	{Name: "drop_constraint_fk", SQL: "ALTER TABLE gt.t DROP CONSTRAINT t_parent_id_fkey"},
	{Name: "drop_constraint_check", Setup: "ALTER TABLE gt.t ADD CONSTRAINT t_i_pos CHECK (i > 0)",
		SQL: "ALTER TABLE gt.t DROP CONSTRAINT t_i_pos"},
	{Name: "rename_constraint", SQL: "ALTER TABLE gt.t RENAME CONSTRAINT t_parent_id_fkey TO t_parent_fk"},

	{Name: "rename_column", SQL: "ALTER TABLE gt.t RENAME COLUMN txt TO txt2"},
	{Name: "rename_table", SQL: "ALTER TABLE gt.child RENAME TO child2", Target: "gt.child"},
	{Name: "set_tablespace", SQL: "ALTER TABLE gt.t SET TABLESPACE " + tablespace, MinMajor: 15, Tablespace: true},
	{Name: "set_unlogged", SQL: "ALTER TABLE gt.nopk SET UNLOGGED", Target: "gt.nopk"},
	{Name: "set_fillfactor", SQL: "ALTER TABLE gt.t SET (fillfactor = 80)"},
	{Name: "disable_trigger", SQL: "ALTER TABLE gt.t DISABLE TRIGGER t_trg"},
	{Name: "comment_on_table", SQL: "COMMENT ON TABLE gt.t IS 'measured'"},
	{Name: "lock_table_share", SQL: "LOCK TABLE gt.t IN SHARE MODE"},

	{Name: "attach_partition", SQL: "ALTER TABLE gt.p ATTACH PARTITION gt.p_new FOR VALUES FROM ('2027-01-01') TO ('2028-01-01')",
		Target: "gt.p_new"},
	{Name: "detach_partition", SQL: "ALTER TABLE gt.p DETACH PARTITION gt.p_2025", Target: "gt.p"},
	{Name: "detach_partition_concurrently", Setup: "ALTER TABLE gt.p DETACH PARTITION gt.p_default; DROP TABLE gt.p_default",
		SQL: "ALTER TABLE gt.p DETACH PARTITION gt.p_2025 CONCURRENTLY", Target: "gt.p"},
	{Name: "create_partition_of", SQL: "CREATE TABLE gt.p_2027 PARTITION OF gt.p FOR VALUES FROM ('2027-01-01') TO ('2028-01-01')",
		Target: "gt.p"},
	{Name: "create_table_with_fk", SQL: "CREATE TABLE gt.newt (id bigint PRIMARY KEY, t_id bigint REFERENCES gt.t (id))", Target: "gt.parent"},

	{Name: "vacuum_full", SQL: "VACUUM FULL gt.t"},
	{Name: "vacuum", SQL: "VACUUM gt.t"},
	{Name: "analyze", SQL: "ANALYZE gt.t"},
	{Name: "cluster_table", SQL: "CLUSTER gt.t USING t_i_idx"},
	{Name: "cluster_all", SQL: "CLUSTER"},
	{Name: "create_trigger", SQL: "CREATE TRIGGER t_trg2 AFTER INSERT ON gt.t FOR EACH ROW EXECUTE FUNCTION gt.trg()"},
	{Name: "drop_trigger", SQL: "DROP TRIGGER t_trg ON gt.t"},
	{Name: "alter_type_add_value", SQL: "ALTER TYPE gt.mood ADD VALUE 'great'", Target: "gt.parent"},
	{Name: "refresh_matview", SQL: "REFRESH MATERIALIZED VIEW gt.t_mv", Target: "gt.t_mv"},
	{Name: "refresh_matview_concurrently", SQL: "REFRESH MATERIALIZED VIEW CONCURRENTLY gt.t_mv", Target: "gt.t_mv"},
	{Name: "create_view", SQL: "CREATE VIEW gt.v2 AS SELECT id FROM gt.t", Target: "gt.parent"},
	{Name: "create_or_replace_view", SQL: "CREATE OR REPLACE VIEW gt.t_view AS SELECT id, c FROM gt.t", Target: "gt.parent"},
	{Name: "create_matview", SQL: "CREATE MATERIALIZED VIEW gt.mv2 AS SELECT id FROM gt.t"},
	{Name: "drop_view", SQL: "DROP VIEW gt.t_view", Target: "gt.parent"},

	{Name: "drop_table_fk_child", SQL: "DROP TABLE gt.child", Target: "gt.parent"},
	{Name: "drop_table_restrict_with_dependents", SQL: "DROP TABLE gt.t"},
	{Name: "drop_table_cascade", SQL: "DROP TABLE gt.t CASCADE", Target: "gt.parent"},
	{Name: "truncate", SQL: "TRUNCATE gt.child", Target: "gt.child"},
	{Name: "truncate_cascade", SQL: "TRUNCATE gt.t CASCADE"},
}
