SET lock_timeout = '2s';
DROP TABLE shop.shipments;
ALTER TABLE shop.orders ADD CONSTRAINT orders_total_positive CHECK (total_cents > 0) NOT ENFORCED;
ALTER TABLE shop.orders ALTER COLUMN currency TYPE varchar(3);
