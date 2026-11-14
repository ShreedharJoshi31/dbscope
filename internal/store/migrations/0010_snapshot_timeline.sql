-- When a snapshot's structure was last read: an unchanged read moves the
-- newest row's last_seen_at forward, so a timeline can tell "no change" from
-- "not read".
ALTER TABLE schema_snapshots ADD COLUMN last_seen_at TEXT;
UPDATE schema_snapshots SET last_seen_at = taken_at;
-- How many schema snapshots a profile keeps; older ones are deleted.
ALTER TABLE profiles ADD COLUMN snapshots_kept INTEGER NOT NULL DEFAULT 20 CHECK (snapshots_kept BETWEEN 2 AND 500);
