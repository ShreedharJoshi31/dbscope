ALTER TABLE app.orders ADD COLUMN total_virtual bigint GENERATED ALWAYS AS ((total * 10)::bigint) VIRTUAL;
