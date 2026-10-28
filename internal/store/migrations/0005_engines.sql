-- Profiles name their engine (docs/ENGINES.md); existing ones are PostgreSQL.
-- path is the database file of a file-based engine such as SQLite.
ALTER TABLE profiles ADD COLUMN engine TEXT NOT NULL DEFAULT 'postgres';
ALTER TABLE profiles ADD COLUMN path TEXT NOT NULL DEFAULT '';
