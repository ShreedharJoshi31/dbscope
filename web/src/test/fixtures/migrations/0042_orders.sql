-- Track parcels per order
ALTER TABLE shop.orders ADD COLUMN tracking_code text;
CREATE INDEX orders_tracking_idx ON shop.orders (tracking_code);
ALTER TABLE shop.customers RENAME COLUMN email TO email_address;
CREATE TABLE shop.parcel_events (
  id bigint PRIMARY KEY,
  order_id bigint NOT NULL REFERENCES shop.orders (id),
  happened_at timestamptz NOT NULL
);
