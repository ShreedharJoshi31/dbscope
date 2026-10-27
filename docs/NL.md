# Natural-language querying

Research notes: `docs/research-nl.md`. Query execution reuses the Phase 2 guard and
executor unchanged (`docs/QUERY-SAFETY.md`): the model never runs anything itself.

## Pipeline

```
question (+ conversation, glossary, assumption overrides, clarification answer)
  → retrieve      BM25 over tables; full schema when it fits the provider's budget
  → generate      one structured-output call (schema below), streamed
  → validate      kind/sql consistency, sql_fragment ⊂ sql, tables exist, 2-4 clarify options
  → guard+EXPLAIN Phase 2 guard, function rule, cost gate (409 → ask the user to confirm)
  → execute       Phase 2 executor (read-only, cursor-bounded, ROLLBACK)
  → repair ×1     only on a guard syntax/function rejection or a database error, with the redacted error
  → summarize     per the profile's summary mode (profile: computed locally, no model call)
```

No self-consistency or multi-candidate voting: the research shows large cost for little or
negative gain on open models. Self-correction is the one cheap, consistent win (+1.2 to +4.4 EX).

## Retrieval

- One document per relation: `schema.name`, table comment, column names + comments, FK target
  names, glossary synonyms. Field weights by repetition: table name ×3, columns ×2.
- Tokeniser: split non-alphanumerics, snake_case, camelCase (acronym-aware), letters/digits;
  lowercase; plural folding (`ies→y`, `es`, `s`, tokens > 3 chars); abbreviation table
  (`cust qty amt dt addr num txn acct org usr prod cat desc`) + glossary terms, keeping raw and expanded.
- BM25 k1 1.2, b 0.75, implemented in-house (~100 lines).
- Budget: Anthropic sends the **full** compact schema when it is ≤ 40,000 estimated tokens (chars/4),
  as a cached system block; otherwise top 20 relations. OpenAI-compatible/local: top 10.
  Both add 1-hop FK neighbours (declared and inferred) and bridge tables on shortest FK paths
  between seeds, capped at 2× the seed count.
- Rendering: compact DDL with PK/FK (inferred FKs marked `-- inferred`), enum labels, comments as
  `-- ` line comments. Stub relations only by name + PK.

## Prompt hygiene (prompt injection, OWASP LLM01/02/05)

- Metadata (comments, names, glossary text) is untrusted data: control characters and newlines
  stripped, comments truncated to 200 chars, wrapped in a labelled data block the system prompt
  says never to follow instructions from.
- Columns whose names match `password|passwd|secret|token|api_?key|ssn|credit_?card|card_number|cvv|salt|hash`
  (case-insensitive) are left out of the prompt by default; the answer card says so.
- Errors sent to the model for repair: SQLSTATE + message with every quoted literal (`'…'`, `"…"`)
  replaced by `<redacted>` + position. Never DETAIL, WHERE, internal query or HINT literals.
- Model output is rendered as plain text in the UI (no markdown links or images).

## Structured output (identical schema for both providers; every field required)

```json
{"type":"object","additionalProperties":false,
 "required":["kind","explanation_steps","assumptions","tables","clarifying_question","sql"],
 "properties":{
  "kind":{"type":"string","enum":["sql","clarify"]},
  "explanation_steps":{"type":"array","items":{"type":"string"}},
  "assumptions":{"type":"array","items":{"type":"object","additionalProperties":false,
     "required":["label","sql_fragment"],
     "properties":{"label":{"type":"string"},"sql_fragment":{"type":"string"}}}},
  "tables":{"type":"array","items":{"type":"string"}},
  "clarifying_question":{"type":"object","additionalProperties":false,"required":["question","options"],
     "properties":{"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"}}}},
  "sql":{"type":"string"}}}
```

`kind` compared case-insensitively. Explanation fields come before `sql` (helps smaller models).
For OpenAI-compatible endpoints the schema is also described in the prompt (llama.cpp grammars
do not inject it).

## Clarification policy

Answer with editable assumption chips by default (time range, timezone, ties, NULL handling).
Ask (kind = clarify, one question, 2-4 options; the UI adds "Other") only when two readings would
change the result and the glossary has no default: an undefined metric or an ambiguous entity.
A user's answer is saved to the profile glossary so it is not asked again.

## Providers

| | Anthropic (BYOK) | OpenAI-compatible (Ollama, LM Studio, vLLM, OpenAI) |
|---|---|---|
| SDK | `github.com/anthropics/anthropic-sdk-go/v2` (v2.3.0, verified to compile) | `github.com/openai/openai-go/v3`, Chat Completions only (Ollama's `/v1/responses` lacks format support) |
| Default model | `claude-opus-5-5`; selectable `claude-sonnet-5-5`, `claude-haiku-5-5` | user-entered model name |
| Summary model | `claude-haiku-5-5` (selectable) | same as generation unless set |
| Structured output | `OutputConfig{Format: JSONOutputFormatParam{Schema}}` | `response_format` json_schema |
| Latency control | `OutputConfig.Effort` (default `medium`; Opus 5.5 cannot disable thinking; never send `temperature`, which is rejected) | none |
| Caching | system prompt + schema block with `CacheControl` ephemeral; schema text byte-stable for a snapshot | n/a |
| Refusals | check `StopReason` (`refusal`, `max_tokens`) before parsing; server-side `fallbacks: "default"` (beta `server-side-fallback-2026-07-01`) enabled for Opus 5.5 / Sonnet 5.5 | n/a |

API keys live in the secrets vault (`llm:anthropic`, `llm:openai_compat`), never in SQLite or logs.

## Summary modes

| Mode | Sent to the model | Notes |
|---|---|---|
| `off` | nothing | the card shows rows only |
| `profile` | nothing: dbscope writes the summary itself from the profile (row count; numeric/date ranges; distinct counts; empty %) | default. A model given only a profile invents row values (observed with qwen2.5-coder:7b: made-up customer names), so it is not asked |
| `rows` | first `maxRowsToLLM` rows (as JSON) | on `prod-replica`, requires `confirmRows: true` once per agent run before rows leave the machine |

## Evaluation

`dbscope eval nl --dataset <dir> --dsn <url> [--provider ... --model ...]` runs every question,
executes gold and predicted SQL through the same read-only executor, and reports:
Set-EX (BIRD-compatible), Multiset-EX, ordered match when gold has a top-level ORDER BY,
clarify precision/recall on questions labelled ambiguous, guard rejections, median latency, tokens.
Datasets live in `eval/nl/<name>/` (`questions.yaml`, `setup.sh` that downloads the database with a
pinned checksum, `README.md` with licence). Pagila (PostgreSQL License) and a deterministic
"messy Pagila" variant (no FKs, cryptic names, glossary-dependent).
