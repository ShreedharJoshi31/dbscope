I did not create or change any files, and I ran no tests. Every number below comes from the page linked next to it. Anything I could not confirm is marked "(unverified)" and repeated in the Unverified list.

## Result

### 1. Techniques that measurably help (2024-2026), and which fit an interactive tool

| Technique | Evidence (execution accuracy, EX) | Cost per question | dbscope recommendation |
|---|---|---|---|
| Execution-guided self-correction (retry on error) | On BIRD dev (n=1534), self-correction was the only part with a consistent gain: +1.2 to +4.4 points at 1.1 to 1.9x tokens ([arXiv 2606.29733 v2](https://arxiv.org/abs/2606.29733)). Removing CHASE-SQL's query fixer cost 3.78 points ([CHASE-SQL](https://arxiv.org/html/2410.01943v1)). CHESS's revision step added 6.80 points, on a 147-question subset ([CHESS](https://arxiv.org/html/2405.16755v1)). DIN-SQL: 69.9 vs 67.3 on Spider dev with CodeX, and a "generic" fix prompt can hurt GPT-4 ([DIN-SQL](https://arxiv.org/html/2304.11015)). | One extra LLM call, only when needed | **Use it.** Retry at most once, on a SQL error or zero rows, using the redacted error from section 6. Run `EXPLAIN` first (no execution) so syntax errors are caught locally in milliseconds. That last part is my suggestion, not a measured result. |
| Schema linking / pruning | Results conflict. CHESS: table selection +6.12 points (GPT-4 era, small subset). "Death of Schema Linking": skip linking when the schema fits in context, 71.83% BIRD ([arXiv 2408.07702](https://arxiv.org/abs/2408.07702)). E-SQL: filtering gave no gain with top proprietary models ([arXiv 2409.16751](https://arxiv.org/abs/2409.16751)). On-prem study: linking never helped significantly, and lexical linking cost Llama-3.3-70B 4.2 points (2606.29733). | Cheap if done locally (BM25) | **Prune for recall, not precision.** Send the full schema to strong cloud models when it fits a token budget. Prune hard only for local models (see section 4 for latency). |
| Value hints (matching question words to stored values) | CHASE-SQL without LSH value retrieval: -2.92. CHESS entity and context retrieval: +4.76. | Local lookup, about 10 ms (unverified) | **Opt-in only.** Matched values are database contents, which conflicts with the "schema metadata only" default. |
| Few-shot examples built from the schema | CHASE-SQL's online synthetic-example generator alone reached 68.02%, +10.27 over its baseline prompt, with Gemini 1.5 Pro. | One LLM call per question in the paper | **Possible adaptation (not evaluated anywhere):** generate about 20 question/SQL pairs per database once at connect time, validate them locally, and pick examples per question with BM25. |
| Self-consistency / candidate selection | CHASE-SQL: single query 63.01, self-consistency 68.84 (+5.84), oracle 82.79. A trained pairwise selector beat self-consistency by about 4.2 points. The on-prem study saw at most 0.13 points of change at about 5x tokens. | 5 to 20x calls | **Do not use as the default.** Too slow and costly for interactive use, and the gains do not appear for weaker or open models. |
| Clarification / interaction | BIRD-Interact: GPT-5 completes 8.67% (c-Interact) and 17.00% (a-Interact) of tasks ([arXiv 2510.05318](https://arxiv.org/abs/2510.05318)). | One user round-trip | See section 7. |

Two caveats about leaderboards:
- BIRD Mini-Dev appears to have a 52.8% annotation error rate and Spider 2.0-Snow 62.8%. Re-scoring 16 agents on corrected data moved scores by -7% to +31% (relative) and ranks by up to 9 places ([uiuc-kang-lab, VLDB 2026](https://github.com/uiuc-kang-lab/text_to_sql_benchmarks)).
- The best Spider 2.0 paper result I found is ReFoRCE at 35.83 (Snow) and 36.56 (Lite) ([arXiv 2502.00675](https://arxiv.org/html/2502.00675v5)). The leaderboard has likely moved since (unverified).

**Recommended pipeline (no extra LLM calls in the normal case):** BM25 retrieval → one structured generation call → guard + `EXPLAIN` → execute → at most one repair call.

### 2. Schema retrieval for 250+ tables without an embeddings API

**What to retrieve over**
- One document per table: schema.table name, table comment, column names, column comments, glossary synonyms, and FK target table names.
- Weight fields by repeating them: table name ×3, column names ×2.
- Optionally add one document per column, for column-level ranking.

**Tokenisation** (this matters more than the BM25 variant)
- Split on non-alphanumerics, `snake_case`, and camelCase including acronyms (`HTTPServer` → `http`, `server`).
- Split digits from letters and lowercase everything.
- Light plural folding: `ies`→`y`, `es`, `s`, only on tokens longer than 3 characters.
- Abbreviation expansion: a built-in table (`cust`, `qty`, `amt`, `dt`, `addr`, `num`) plus the user's glossary. Keep both the raw and expanded token.
- Query expansion for cryptic names: if an identifier token of 3+ characters is a prefix of a question word, add it.
- Drop question stop-words (show, list, how, many, per).
- Use standard BM25 with k1=1.2 and b=0.75, then tune on your eval set.
- These tokenisation rules are engineering suggestions, not measured results.

**How much context**
- Cloud: if the compact full schema fits a budget (for example 30 to 50k tokens), send all of it and use prompt caching. Cache reads cost 5% of base input on Opus 5.5 and Sonnet 5.5 ([models overview](https://platform.claude.com/docs/en/about-claude/models/overview.md)).
- Cloud, over budget: top about 20 tables by BM25, plus FK neighbours.
- Local: top 8 to 12 tables plus FK neighbours, about 3 to 4k tokens.
- I found no study that fixes an ideal top-k. RESDSQL/CodeS-style values (around 4 to 6 tables) are from memory (unverified).

**FK-neighbour expansion**
- Add 1-hop FK neighbours in both directions.
- Add bridge tables on the shortest FK path between seed tables. GRAST-SQL uses a similar connectivity-preserving (Steiner-tree) heuristic and reports near-perfect recall and sub-second median latency on schemas with 23,000+ columns ([arXiv 2512.16083](https://arxiv.org/abs/2512.16083)).
- With no FKs, infer join candidates from names (`x_id` ↔ `x.id`) and matching types, and label them "inferred" in the prompt.

**Rendering the schema**
- Use compact DDL with comments: column types, PK/FK annotations, and short column comments.
- DAIL-SQL found GPT-4 zero-shot accuracy on Spider dev varied little across formats (72.3 to 74.3 EX). Foreign keys added +2.6 EX to the basic prompt. Code-style DDL was best for open models (68.5%) ([arXiv 2308.15363](https://arxiv.org/html/2308.15363)).
- OmniSQL is trained on DDL with SQL comments ([OmniSQL](https://github.com/RUCKBReasoning/OmniSQL)).

**Go BM25**
- Write about 100 lines in-house. The formula is `IDF = ln(1 + (N − n + 0.5)/(n + 0.5))` and term score `tf·(k1+1)/(tf + k1·(1 − b + b·|D|/avgdl))`.
- Alternative: [richardwooding/bm25](https://github.com/richardwooding/bm25). It is MIT-licensed, has no dependencies, and exposes a low-level `Scorer` that accepts your own tokens. Its maturity is unverified.
- Retrieval over about 3,000 documents should take well under 10 ms (not measured).

### 3. Structured output for the generation step

Suggested schema. Every field is required, there are no `anyOf` unions, and the explanation fields come before `sql`:

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

**Anthropic** (`output_config.format` with `type: "json_schema"`; Go: `anthropic.OutputConfigParam{Format: anthropic.JSONOutputFormatParam{Schema: ...}}`), per the [structured outputs docs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs.md):
- Supported: all basic types, `enum` (scalars only), `const`, `anyOf`/`allOf` with limits, local `$ref`, `required`, and `additionalProperties` (must be `false`).
- `minItems` accepts only 0 or 1.
- Not supported: recursion, numeric and string length constraints, other array constraints.
- Limits: 24 optional parameters and 16 union-typed parameters across all strict schemas. This schema uses zero of each.
- Output order: required properties first, in schema order. This schema keeps its order because everything is required.
- `enum` casing is not guaranteed, so compare `kind` case-insensitively.
- A field that asks for the model's "reasoning" may trigger a `reasoning_extraction` refusal. Describe `explanation_steps` as short, user-facing steps.
- The first use of a schema adds grammar-compile latency; compiled grammars are cached for 24 hours. Changing `output_config.format` invalidates the prompt cache, so keep the schema constant.
- Prefill and citations are incompatible with this feature.

**Ollama**
- Structured outputs are documented through the native `format` field and the OpenAI-compatible `response_format` ([docs](https://docs.ollama.com/capabilities/structured-outputs), [OpenAI compat](https://docs.ollama.com/api/openai-compatibility)). Neither page lists supported JSON-schema keywords.
- Ollama appears to use llama.cpp's JSON-schema-to-grammar converter, based on [issue #8444](https://github.com/ollama/ollama/issues/8444) (unverified for current versions). Its documented limits ([grammars README](https://github.com/ggml-org/llama.cpp/blob/master/grammars/README.md)):
  - unsupported features are skipped silently;
  - properties cannot be mixed with `anyOf`;
  - nested `$ref`s are broken;
  - patterns need `^…$`;
  - `additionalProperties` defaults to false;
  - the schema is not injected into the prompt, so describe it in the prompt too.
- llama-server has been reported to fail open when grammar parsing fails ([#19051](https://github.com/ggml-org/llama.cpp/issues/19051)).

**Checks to run in Go for both providers**
- `kind` and `sql` are consistent with each other.
- Every `sql_fragment` is a substring of `sql`.
- Every entry in `tables` exists in the catalog.
- Clarify responses have 2 to 4 options.

**Field order matters for small models.** Putting the reasoning-like fields first matched free-form accuracy, while answer-first ordering hurt small models ([arXiv 2610.12056](https://arxiv.org/html/2610.12056)). Format constraints cost little for strong models but a lot for weaker ones ([arXiv 2606.09410](https://arxiv.org/pdf/2606.09410)).

### 4. Models

**Anthropic** (all have 1M context and support structured outputs; [models overview](https://platform.claude.com/docs/en/about-claude/models/overview.md)):

| Model ID | Price (input / output per MTok) | Suggested role |
|---|---|---|
| `claude-sonnet-5-5` | $2 / $10 | Default for generation |
| `claude-haiku-5-5` | from $0.10 / $0.50 | Result summaries and repair retries |
| `claude-opus-5-5` | $4 / $20 | Optional "high accuracy" mode |
| `claude-fable-5-1` | $10 / $50 | Probably overkill here |

Sonnet 5.5 rejects `thinking: disabled` but accepts `between_tools`. Use the effort parameter to manage latency.

**Local models with published text-to-SQL scores**
- Qwen2.5-Coder 7B / 14B / 32B: 42.6 / 52.0 / 54.9 on BIRD dev under one zero-shot protocol (2606.29733).
- Arctic-Text2SQL-R1-7B: 68.9% BIRD dev per its [model card](https://huggingface.co/Snowflake/Arctic-Text2SQL-R1-7B). It is trained on SQLite. The only Ollama build I found is a community port.
- OmniSQL 7B / 14B / 32B: the README states it supports SQLite only.
- PostgreSQL-specific (LiveSQLBench-Base-Lite, PG column): Qwen2.5-Coder 32B 22.96 and 7B 12.22, against Claude 3.7 Sonnet 39.26 ([mini_dev README](https://github.com/bird-bench/mini_dev)).

I found no primary PostgreSQL scores and no structured-output reliability data for newer models such as Qwen3.5, Qwen3-Coder-Next or gpt-oss. Pick by running your own harness.

**Apple Silicon latency** (llama.cpp benchmark, Llama 2 7B at Q4_0; [discussion #4167](https://github.com/ggml-org/llama.cpp/discussions/4167)). PP is prompt processing, TG is token generation, both in tokens per second:

| Chip | PP | TG |
|---|---|---|
| M1 Pro, 16-core GPU | 266 | 36 |
| M4 Pro, 20-core GPU | 440 | 51 |
| M4 Max, 40-core GPU | 886 | 83 |

On an M4 Pro, a 4k-token prompt plus 250 output tokens works out to about 9 s + 5 s for a 7B model. Larger dense models would be slower (unverified). This is why local mode needs a tight schema budget.

### 5. Evaluation

**Harness**
- Run gold and predicted SQL in read-only transactions with `statement_timeout`.
- Report Set-EX, which matches BIRD's evaluator (`set(pred)==set(gold)`, [evaluation_ex.py](https://raw.githubusercontent.com/bird-bench/mini_dev/main/evaluation/evaluation_ex.py)), and Multiset-EX. Set-EX hides duplicate-row errors, and the gap was 5.2 to 6.8 points in [arXiv 2609.29573](https://arxiv.org/abs/2609.29573).
- Compare in order only when the gold SQL has a top-level `ORDER BY`.
- Match columns regardless of column order, normalise numeric types and floats (about 1e-6), timestamps to UTC, and NULLs.
- Optionally run on a second copy of the database with perturbed data to cut false positives (Zhong et al. 2020 test suites).
- Score `kind=clarify` questions separately.

**Datasets**

| Dataset | Licence | Notes |
|---|---|---|
| [Pagila](https://github.com/devrimgunduz/pagila) | PostgreSQL License | |
| [Chinook](https://raw.githubusercontent.com/lerocha/chinook-database/master/LICENSE.md) | MIT-style text | GitHub reports "Other" |
| [northwind_psql](https://raw.githubusercontent.com/pthom/northwind_psql/master/LICENSE) | Ms-PL | Copyleft-ish attribution conditions |
| [BIRD Mini-Dev](https://huggingface.co/api/datasets/birdsql/bird_mini_dev) | CC BY-SA 4.0 | 500 questions over 11 databases, aligned across SQLite, MySQL and PostgreSQL (`mini_dev_pg` split); revised in a 2026 update |

For BIRD Mini-Dev, the dump size and the licence of the underlying database files are unverified. Download it at eval time rather than vendoring it into the repo, because of ShareAlike.

**Building 60 to 100 pairs per database**
- Stratify by feature: joins, aggregates, windows, date ranges, NULLs, DISTINCT/multiplicity traps.
- Make about 10 to 15% ambiguous (expected answer: clarify) and 5 to 10% unanswerable.
- LLM drafts are fine, but have a person execute and review every gold query, with two reviewers. Given the annotation error rates above, this review step is necessary.
- Record acceptable alternative answers, and pin the database dump by checksum.

**Messy schema variant**
- Apply a deterministic mapping to Pagila: drop FKs, use cryptic names (`t_cst.c3`), store dates as text, use coded status values, add decoy tables, and move meaning into a glossary.
- Rewrite the gold SQL through the same mapping, for example with pganalyze/pg_query_go (unverified fit).
- Add an injection suite (section 6).

### 6. Prompt-injection and data-leak risks in this flow

**Vectors**
- `COMMENT ON` text, table/column/enum/view names, and the glossary. These can steer the model toward sensitive columns or misleading summaries.
- Cell values in "rows" mode. This is the Supabase/General Analysis case, where instructions planted in data led an agent to copy private data out ([Supabase](https://supabase.com/blog/mcp)).
- Error messages. For example, `CAST(secret AS int)` puts the value into the message (`invalid input syntax…: "value"`). Feeding that to a repair call leaks data even in schema-only mode.
- Min/max of text columns in profile mode.
- Rendering of model output. OWASP LLM05 lists markdown-image exfiltration and XSS.

**Mitigations**
- Error redaction: send only SQLSTATE, the message with quoted literals replaced by `'<redacted>'`, and Position. Drop Detail, Where, Internal query, and literal values in Hint. Field meanings are in the [protocol error fields](https://www.postgresql.org/docs/current/protocol-error-fields.html) doc.
- Metadata hygiene: strip newlines and control characters from comments, truncate them (for example to 200 characters), and wrap them as quoted, labelled data. Flag imperative text ("ignore", "system") in the UI.
- Sensitive columns: keep a name denylist (password, token, secret, ssn) that is excluded from prompts and blocked by the guard unless the user opts in.
- Guard: deny network and file functions such as `dblink`, `http`, `pg_read_file`, `lo_*`. Use a low-privilege role.
- UI: render model text as plain text, with no auto-linked URLs or images.
- Profile mode: suppress small groups (count < k). This is my suggestion, not from a source.
- These follow OWASP [LLM01](https://genai.owasp.org/llmrisk/llm01-prompt-injection/) (constrain, validate format, least privilege, segregate untrusted content, adversarial tests), [LLM02](https://genai.owasp.org/llmrisk/llm022025-sensitive-information-disclosure/) (least privilege, error-message hygiene) and [LLM05](https://genai.owasp.org/llmrisk/llm052025-improper-output-handling/).

**What others do**
- Vanna sends no database contents by default (`allow_llm_to_see_data=False`, [FAQ](https://try.vanna.ai/data-security-faq.html)). Its CVE-2024-5565 was remote code execution from `exec` of LLM-generated Plotly code ([JFrog](https://research.jfrog.com/vulnerabilities/vanna-prompt-injection-rce-jfsa-2024-001034449/)). Never execute model-generated code.
- Supabase reports that wrapping results with warnings and using classifiers reduced the risk but did not remove it. They recommend non-production data and CaMeL-style separation.
- Outerbase: I found no primary guidance.

### 7. Clarification UX

**Evidence**
- PlanPool shows agents silently making assumptions. Forcing more questions raises accuracy but is inefficient, because ambiguities cluster early in the conversation ([arXiv 2610.02739](https://arxiv.org/abs/2610.02739)).
- PRACTIQ defines 4 ambiguous and 4 unanswerable categories, and state-of-the-art systems struggle with them ([arXiv 2410.11076](https://arxiv.org/abs/2410.11076)).
- AmbiSQL v1 reports 87.2% detection precision and +50% exact match using multiple-choice questions ([v1](https://arxiv.org/html/2508.15276v1)).
- An Expected Information Gain approach asks the question that best separates candidate SQL queries ([arXiv 2507.06467](https://arxiv.org/abs/2507.06467)).
- Tian et al. (EMNLP 2023) let users edit step-by-step explanations. In a 24-participant study, users solved more tasks, faster, with higher confidence ([arXiv 2305.07372](https://arxiv.org/abs/2305.07372)).
- DIY (IUI 2021, 12 users) uses question-to-query mappings, a small relevant data subset, and back-of-envelope checks ([MSR](https://www.microsoft.com/en-us/research/publication/diy-assessing-the-correctness-of-natural-language-to-sql-systems/)).
- I found no published ask-versus-answer threshold.

**Policy for dbscope**
- Default to answering with editable assumption chips: time range, timezone, ties, NULLs.
- Ask only when two interpretations would change the result and the glossary gives no default. Typical cases: an undefined metric (gross vs net revenue, "active user") or an entity name that matches several local values.
- Ask at most one question per turn, with 2 to 4 options plus "Other".
- Save each resolution to the per-database glossary so the same question is not asked again.
- Optional cheap detector: if 2 candidate queries return different results locally, ask (a simplified form of the information-gain approach).
- When a user edits a chip, regenerate under that constraint and show the SQL diff.

### Unverified
- Ollama's current JSON-schema backend and keyword support; whether it honours `strict`; how structured output interacts with thinking models.
- Exact field names in openai-go v3 for `response_format`, and the exact fields of pgx's `pgconn.PgError` (both from memory).
- RESDSQL/CodeS top-k values.
- The BIRD Mini-Dev PostgreSQL dump size and the licence of its database files; the licence of the Arcwise-Plat corrections.
- PostgreSQL scores and Apple Silicon latency for 2026 open models; latency for 30B-class models (extrapolated only).
- BM25 timing and all tokenisation heuristics (not measured).
- Whether a 2026 OWASP LLM edition supersedes the 2025 list (seen only in a secondary source).
- Outerbase's mitigations.

```json
{"status":"done","summary":"Research report on NL-to-SQL for dbscope covering seven areas with primary-source citations. Key findings: execution-guided self-correction is the most consistent cheap gain (+1.2 to +4.4 EX on BIRD for open models); schema linking has mixed evidence, so prune for recall and send the full schema to strong cloud models with prompt caching; self-consistency is too costly for interactive use. A single all-required JSON schema with no unions fits both Anthropic structured outputs and llama.cpp-style grammars. Anthropic IDs are claude-sonnet-5-5, claude-haiku-5-5, claude-opus-5-5 and claude-fable-5-1. Local SQL-specialist models are mostly SQLite-trained and Apple Silicon prefill speed makes tight local schema budgets necessary. Evaluation should report Set-EX and Multiset-EX on Pagila (PostgreSQL License), Chinook (MIT-style) and BIRD Mini-Dev PG (CC BY-SA 4.0, 500 questions). Security work should focus on error-message redaction, metadata sanitisation and plain-text rendering. No files were created and no tests were run.","artifacts":[],"tests":{"command":"","passed":null,"output_tail":""},"claims_to_verify":["arXiv 2606.29733 v2: Qwen2.5-Coder 42.6/52.0/54.9 BIRD dev; self-correction +1.2 to 4.4; lexical linking -4.2 for Llama-3.3-70B; self-consistency <=0.13","CHASE-SQL Table 1 and 7: 63.01 single, 68.84 self-consistency, 82.79 upper bound; w/o LSH -2.92; w/o fixer -3.78","CHESS v1 ablation on 147-question SDS: retrieval +4.76, table selection +6.12, revision +6.80","BIRD-Interact: GPT-5 8.67% c-Interact, 17.00% a-Interact","uiuc-kang-lab: BIRD Mini-Dev 52.8% and Spider2-Snow 62.8% annotation error rates","Anthropic structured outputs limits: 24 optional params, 16 union params, minItems 0/1 only, additionalProperties must be false, enum casing caveat, reasoning_extraction refusal note","Anthropic model IDs and prices from platform.claude.com models overview as of 2026-10-27","birdsql/bird_mini_dev HF licence cc-by-sa-4.0 with mini_dev_pg split","Pagila PostgreSQL License; Chinook LICENSE.md MIT-style; northwind_psql Ms-PL","llama.cpp #4167 Q4_0 Llama 2 7B numbers for M1 Pro, M4 Pro, M4 Max","ModularSQL Set-EX vs Multiset-EX gap 5.81-6.79 pp","DAIL-SQL zero-shot GPT-4 representation range 72.3-74.3 EX and +2.6 with foreign keys"],"open_questions":["Does dbscope's existing guard already block dblink/http/pg_read_file/lo_* and similar network or file functions?","Is the error message currently fed back to the model for repair, and does it include DETAIL?","What token budget should trigger full-schema mode for cloud providers?","Should value hints be offered as an opt-in privacy level separate from the rows mode?"]}
```