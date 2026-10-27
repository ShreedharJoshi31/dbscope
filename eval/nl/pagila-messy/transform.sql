-- Builds the "messy Pagila" schema px from a database where ../pagila/setup.sh
-- has just loaded Pagila into schema public, then removes public's Pagila
-- objects so only px is left. Deterministic: the same dump always gives the
-- same px.
--
-- What changes (glossary.yaml documents all of it):
--   * cryptic table names (t_flm, t_cst, ...) and positional column names (c1, c2, ...)
--   * no foreign keys (primary keys kept), so nothing can be inferred from names
--   * timestamps stored as text 'YYYY-MM-DD HH24:MI:SS' in UTC, dates as 'YYYYMMDD'
--   * payment amounts in integer cents
--   * coded values: film rating 1-5, customer status 'A'/'I'
--   * decoy tables t_flm_old (stale copy, different rates) and t_rnt_arch (empty)
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL TimeZone = 'UTC';
SET LOCAL client_min_messages = warning;
CREATE SCHEMA px;

CREATE TABLE px.t_lng AS SELECT language_id AS c1, name AS c2 FROM public.language ORDER BY 1;
CREATE TABLE px.t_ctr AS SELECT country_id AS c1, country AS c2 FROM public.country ORDER BY 1;
CREATE TABLE px.t_cty AS SELECT city_id AS c1, city AS c2, country_id AS c3 FROM public.city ORDER BY 1;
CREATE TABLE px.t_adr AS
  SELECT address_id AS c1, address AS c2, district AS c3, city_id AS c4, phone AS c5 FROM public.address ORDER BY 1;
CREATE TABLE px.t_sto AS SELECT store_id AS c1, manager_staff_id AS c2, address_id AS c3 FROM public.store ORDER BY 1;
CREATE TABLE px.t_act AS SELECT actor_id AS c1, first_name AS c2, last_name AS c3 FROM public.actor ORDER BY 1;
CREATE TABLE px.t_cat AS SELECT category_id AS c1, name AS c2 FROM public.category ORDER BY 1;
CREATE TABLE px.t_flm AS
  SELECT film_id AS c1, title AS c2, description AS c3, release_year::int AS c4, language_id AS c5,
         rental_duration AS c6, rental_rate AS c7, length AS c8, replacement_cost AS c9,
         (CASE rating WHEN 'G' THEN 1 WHEN 'PG' THEN 2 WHEN 'PG-13' THEN 3 WHEN 'R' THEN 4 WHEN 'NC-17' THEN 5 END)::smallint AS c10
  FROM public.film ORDER BY 1;
CREATE TABLE px.t_fa AS SELECT actor_id AS c1, film_id AS c2 FROM public.film_actor ORDER BY 1, 2;
CREATE TABLE px.t_fc AS SELECT film_id AS c1, category_id AS c2 FROM public.film_category ORDER BY 1, 2;
CREATE TABLE px.t_inv AS SELECT inventory_id AS c1, film_id AS c2, store_id AS c3 FROM public.inventory ORDER BY 1;
CREATE TABLE px.t_cst AS
  SELECT customer_id AS c1, store_id AS c2, first_name AS c3, last_name AS c4, email AS c5, address_id AS c6,
         CASE WHEN activebool THEN 'A' ELSE 'I' END AS c7, to_char(create_date, 'YYYYMMDD') AS c8
  FROM public.customer ORDER BY 1;
CREATE TABLE px.t_rnt AS
  SELECT rental_id AS c1, to_char(rental_date, 'YYYY-MM-DD HH24:MI:SS') AS c2, inventory_id AS c3,
         customer_id AS c4, to_char(return_date, 'YYYY-MM-DD HH24:MI:SS') AS c5, staff_id AS c6
  FROM public.rental ORDER BY 1;
CREATE TABLE px.t_pmt AS
  SELECT payment_id AS c1, customer_id AS c2, staff_id AS c3, rental_id AS c4, (amount * 100)::bigint AS c5,
         to_char(payment_date, 'YYYY-MM-DD HH24:MI:SS') AS c6
  FROM public.payment ORDER BY 1;

-- Decoys.
CREATE TABLE px.t_flm_old AS SELECT c1, c2, c7 * 2 AS c7, c8 FROM px.t_flm WHERE c1 <= 100 ORDER BY 1;
CREATE TABLE px.t_rnt_arch (LIKE px.t_rnt);

ALTER TABLE px.t_lng ADD PRIMARY KEY (c1);
ALTER TABLE px.t_ctr ADD PRIMARY KEY (c1);
ALTER TABLE px.t_cty ADD PRIMARY KEY (c1);
ALTER TABLE px.t_adr ADD PRIMARY KEY (c1);
ALTER TABLE px.t_sto ADD PRIMARY KEY (c1);
ALTER TABLE px.t_act ADD PRIMARY KEY (c1);
ALTER TABLE px.t_cat ADD PRIMARY KEY (c1);
ALTER TABLE px.t_flm ADD PRIMARY KEY (c1);
ALTER TABLE px.t_fa ADD PRIMARY KEY (c1, c2);
ALTER TABLE px.t_fc ADD PRIMARY KEY (c1, c2);
ALTER TABLE px.t_inv ADD PRIMARY KEY (c1);
ALTER TABLE px.t_cst ADD PRIMARY KEY (c1);
ALTER TABLE px.t_rnt ADD PRIMARY KEY (c1);
ALTER TABLE px.t_pmt ADD PRIMARY KEY (c1);
ALTER TABLE px.t_flm_old ADD PRIMARY KEY (c1);

-- Only px remains: a model that knows Sakila from training cannot fall back on public.film.
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
COMMIT;
ANALYZE;
