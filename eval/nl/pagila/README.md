# Pagila NL evaluation set

40 questions over [Pagila](https://github.com/devrimgunduz/pagila), the PostgreSQL port of
the Sakila DVD-rental sample database.

| Category | Questions | Expected answer |
|---|---|---|
| joins | 7 | SQL |
| aggregates | 6 | SQL |
| windows | 5 | SQL |
| dates (date ranges and date arithmetic) | 5 | SQL |
| nulls (NULL vs empty, anti-joins) | 4 | SQL |
| distinct (DISTINCT and multiplicity traps) | 5 | SQL |
| ambiguous | 5 | a clarifying question |
| unanswerable | 3 | a clarifying question (or no SQL) |

Every gold query was executed against the pinned dump on PostgreSQL 14 and 18 with
identical results. `result` in `questions.yaml` records the row count and an
order-independent checksum of the normalised rows (`internal/eval.Checksum`).
`dbscope eval nl --gold-only` re-runs the gold queries and fails on any drift.

## Setup

```sh
createdb pagila
./setup.sh 'postgres://user:pass@127.0.0.1:5432/pagila'
dbscope eval nl --dataset eval/nl/pagila --dsn 'postgres://user:pass@127.0.0.1:5432/pagila' --gold-only
dbscope eval nl --dataset eval/nl/pagila --dsn '...' --provider anthropic --api-key-env ANTHROPIC_API_KEY
```

`setup.sh` downloads `pagila-schema.sql` and `pagila-data.sql` from commit
`23f7fe7a1e9f5772f85fc14f530583ffa258c6d2` (2026-07-28), checks their SHA-256 sums, and
loads them into an empty database. It drops the dump's single
`SET transaction_timeout` line so PostgreSQL 14-16 load it too. Use a database
nobody else needs: dbscope only reads it, but the setup creates objects.

This commit is the last one before upstream added `uuidv7()` defaults (PostgreSQL 18
only) and a pgvector table, so it loads on a stock PostgreSQL 14+ server.

## Licence

Pagila is distributed under the PostgreSQL License, copyright (c) Devrim Gündüz. The
upstream README names the PostgreSQL License at the pinned commit. The `LICENSE.txt`
file at that commit still carried MIT text; upstream replaced it with the PostgreSQL
License text on 2026-10-08 (commit `deb2a885`), stating that the README already named
that licence. Both licences are permissive. The dump is downloaded at setup time and
not stored in this repository; the questions and gold SQL in this directory are
dbscope's own.

## Notes on the questions

- Dates are absolute (no `now()`), so results do not drift.
- Gold queries with `LIMIT` break ties explicitly; questions with ties at the cut
  are avoided or say how to break them.
- Ordered match is scored only when the gold query has a top-level `ORDER BY`.
- Column order and aliases do not matter to the comparison, but extra or missing
  columns do; the questions name the columns they want where it matters.
- The ambiguous questions follow the clarification policy in `docs/NL.md`: two
  readings that change the result (metric or entity), not defaults such as time
  zones. `amb5` relies on `customer.active` and `customer.activebool` disagreeing
  on 43 rows of this dump.
