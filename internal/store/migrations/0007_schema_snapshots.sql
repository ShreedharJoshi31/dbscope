-- Schemas as read, kept so a connection can be compared with its own past.
-- A new row is written only when the structure changes (hash); the newest
-- 20 per profile are kept. model is the schema.Model JSON, gzipped.
CREATE TABLE schema_snapshots (
    id           INTEGER PRIMARY KEY,
    profile_id   TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    taken_at     TEXT NOT NULL,
    hash         TEXT NOT NULL,
    relations    INTEGER NOT NULL,
    schemas_json TEXT NOT NULL,
    model        BLOB NOT NULL
) STRICT;
CREATE INDEX schema_snapshots_profile ON schema_snapshots (profile_id, id);
