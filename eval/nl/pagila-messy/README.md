# Messy Pagila NL evaluation set

20 questions over a deliberately hard version of [Pagila](https://github.com/devrimgunduz/pagila),
built to measure how much the NL pipeline depends on readable names and declared keys,
and how well it uses the glossary.

`transform.sql` runs on a freshly loaded Pagila (same pinned commit and checksums as
`../pagila`) and builds schema `px`:

- cryptic table names (`t_flm`, `t_cst`, `t_rnt`, ...) and positional columns (`c1`, `c2`, ...);
- no foreign keys (primary keys kept), and `cN` names give FK inference nothing to work with;
- timestamps as UTC text `YYYY-MM-DD HH24:MI:SS`, sign-up dates as `YYYYMMDD`;
- payment amounts in integer cents;
- coded values: film rating `1`-`5`, customer status `'A'`/`'I'`;
- decoys: `t_flm_old` (100 films with stale, doubled rental rates) and an empty `t_rnt_arch`.

It then drops schema `public`, so a model that remembers Sakila cannot query `public.film`.
The transform is plain SQL in one transaction (`CREATE TABLE ... AS SELECT ... ORDER BY`), so
the same dump always gives the same `px`.

`glossary.yaml` maps every table and column back to its meaning; `dbscope eval nl` loads it
as the profile glossary.

| Category | Questions |
|---|---|
| joins | 5 |
| aggregates | 4 |
| windows | 3 |
| dates | 3 |
| nulls | 2 |
| distinct | 2 |
| glossary (a term ambiguous on clean Pagila, defined here) | 1 |

Each gold query is a `../pagila` gold query (named in its `note`) rewritten through the
mapping by hand. Every one was executed against the transformed database on PostgreSQL 14
and 18 (identical results) and recorded in `result`. Each rewrite was also checked against
its original on clean Pagila, after normalising the two intended representation changes:
month buckets are `timestamp` here instead of `timestamptz`, and `m11` returns the rating
code. All 20 gave the same rows.

## Setup

```sh
createdb pagila_messy
./setup.sh 'postgres://user:pass@127.0.0.1:5432/pagila_messy'
dbscope eval nl --dataset eval/nl/pagila-messy --dsn 'postgres://user:pass@127.0.0.1:5432/pagila_messy' --gold-only
```

`setup.sh` calls `../pagila/setup.sh` (download, checksum, load), then `transform.sql`.
It replaces the database's `public` schema, so use a database dedicated to this dataset.

## Licence

The data comes from Pagila, PostgreSQL License, copyright (c) Devrim Gündüz; see
`../pagila/README.md` for the licence history of the pinned commit. The dump is downloaded
at setup time and not stored here. `transform.sql`, the glossary and the questions are
dbscope's own.
