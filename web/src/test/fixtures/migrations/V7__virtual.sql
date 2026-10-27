SET lock_timeout = '2s';
ALTER TABLE shop.orders ADD COLUMN total_units int GENERATED ALWAYS AS (total_cents / 100) VIRTUAL;
ALTER TABLE shop.customers ALTER COLUMN email SET NOT NULL;
