-- EXPLAIN cost limit per profile. NULL means the env default
-- (prod-replica 1e6, staging 1e7, local none); 0 means no limit.
ALTER TABLE profiles ADD COLUMN max_cost REAL CHECK (max_cost IS NULL OR max_cost >= 0);

-- Queries run from the UI: SQL text and metadata only, never row data.
CREATE TABLE query_history (
    id          TEXT PRIMARY KEY, -- ULID, so ordering by id is ordering by start
    profile_id  TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    sql         TEXT NOT NULL,
    started_at  TEXT NOT NULL,
    duration_ms REAL NOT NULL,
    row_count   INTEGER NOT NULL DEFAULT 0, -- rows returned; 0 unless status = 'ok'
    status      TEXT NOT NULL CHECK (status IN ('ok', 'error', 'rejected', 'cancelled')),
    error_code  TEXT
) STRICT;
CREATE INDEX query_history_profile ON query_history (profile_id, id DESC);
