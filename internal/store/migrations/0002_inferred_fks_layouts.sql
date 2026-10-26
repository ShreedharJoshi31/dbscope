-- User decisions on inferred foreign keys. key is the API key format,
-- e.g. public.posts(user_id)->public.users(id). "unset" deletes the row.
CREATE TABLE inferred_fk_decisions (
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN ('accepted', 'rejected')),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, key)
) STRICT;

-- Canvas node positions and collapsed nodes, keyed by a UI-chosen layout key.
CREATE TABLE layouts (
    profile_id     TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    key            TEXT NOT NULL,
    positions_json TEXT NOT NULL,
    collapsed_json TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (profile_id, key)
) STRICT;
