SET lock_timeout = '2s';
ALTER TABLE orders ADD COLUMN status text DEFAULT 'new' NOT NULL;
