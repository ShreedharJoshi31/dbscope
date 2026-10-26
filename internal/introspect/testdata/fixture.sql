CREATE SCHEMA app;
CREATE TYPE app.status AS ENUM ('active','suspended','closed');
CREATE TABLE app.accounts (region text NOT NULL, id bigint NOT NULL, email text NOT NULL, status app.status NOT NULL DEFAULT 'active',
  PRIMARY KEY (region, id), UNIQUE (email));
COMMENT ON TABLE app.accounts IS 'customer accounts';
COMMENT ON COLUMN app.accounts.email IS 'login email';
-- composite FK with column order deliberately reversed vs declaration of the columns
CREATE TABLE app.orders (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, acct_id bigint NOT NULL, acct_region text NOT NULL,
  total numeric(12,2) NOT NULL CHECK (total >= 0), created_at timestamptz NOT NULL DEFAULT now(),
  total_cents bigint GENERATED ALWAYS AS ((total * 100)::bigint) STORED,
  FOREIGN KEY (acct_region, acct_id) REFERENCES app.accounts (region, id) ON DELETE CASCADE);
CREATE INDEX orders_recent ON app.orders (created_at DESC) INCLUDE (total) WHERE total > 0;
CREATE INDEX orders_lower ON app.orders (lower(acct_region), acct_id);
CREATE TABLE app.events (id bigint NOT NULL, at date NOT NULL, order_id bigint REFERENCES app.orders(id), PRIMARY KEY (id, at)) PARTITION BY RANGE (at);
CREATE TABLE app.events_2026 PARTITION OF app.events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
CREATE TABLE app.events_2027 PARTITION OF app.events FOR VALUES FROM ('2027-01-01') TO ('2028-01-01');
CREATE VIEW app.big_orders AS SELECT o.id, a.email FROM app.orders o JOIN app.accounts a ON a.region = o.acct_region AND a.id = o.acct_id WHERE o.total > 100;
CREATE MATERIALIZED VIEW app.order_totals AS SELECT acct_id, sum(total) AS s FROM app.orders GROUP BY 1;
CREATE TABLE public.legacy_users (id int PRIMARY KEY, name text);
CREATE TABLE public.legacy_posts (id int PRIMARY KEY, legacy_user_id int, body text); -- no declared FK (inference case)
CREATE ROLE reader LOGIN PASSWORD 'r';
GRANT USAGE ON SCHEMA app TO reader; GRANT SELECT ON ALL TABLES IN SCHEMA app, public TO reader;
CREATE SCHEMA hidden; CREATE TABLE hidden.secret (id int); -- reader has no USAGE here
