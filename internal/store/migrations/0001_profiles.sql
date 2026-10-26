-- Connection profiles. Passwords are never stored here; has_password mirrors
-- whether the secrets vault holds one for the profile.
CREATE TABLE profiles (
    id                   TEXT PRIMARY KEY,
    name                 TEXT NOT NULL UNIQUE COLLATE NOCASE,
    host                 TEXT NOT NULL,
    port                 INTEGER NOT NULL,
    database             TEXT NOT NULL,
    username             TEXT NOT NULL,
    ssl_mode             TEXT NOT NULL,
    ssl_root_cert        TEXT NOT NULL DEFAULT '',
    env                  TEXT NOT NULL,
    schemas              TEXT NOT NULL DEFAULT '[]',
    summary_mode         TEXT NOT NULL,
    max_rows_to_llm      INTEGER NOT NULL,
    statement_timeout_ms INTEGER NOT NULL,
    has_password         INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
) STRICT;
