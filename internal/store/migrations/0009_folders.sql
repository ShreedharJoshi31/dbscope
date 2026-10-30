-- Local folders the agent reads for a profile: migration files and application code; empty for none.
ALTER TABLE profiles ADD COLUMN migrations_dir TEXT NOT NULL DEFAULT '';
ALTER TABLE profiles ADD COLUMN project_dir TEXT NOT NULL DEFAULT '';
