-- The SSH tunnel a profile is reached through, as JSON; empty for none.
ALTER TABLE profiles ADD COLUMN ssh TEXT NOT NULL DEFAULT '';
