ALTER TABLE users ALTER COLUMN email TYPE varchar(100);
CREATE INDEX orders_user_ix ON orders (user_id);
