# dbscope architecture

Local-first PostgreSQL explorer: ERD canvas, natural-language query tracing,
migration risk analysis. One Go binary runs on the user's machine, serves a
localhost web UI, and doubles as a headless CLI for CI.

Stack choices below were researched on 2026-10-26 from primary sources
(research notes: `~/.kiro-jobs/2026-10-26-dbscope/r-*/result.md`).

## Fixed decisions

| # | Decision |
|---|---|
| D1 | Local agent. No hosted backend. The agent binds 127.0.0.1 only. |
| D2 | Three modes: ERD, NL querying, migration analysis. |
| D3 | LLM: bring-your-own-key (Anthropic, any OpenAI-compatible) and local (Ollama). |
| D4 | Result summary per connection: `off` / `profile` / `rows`. Default `profile`. |
| D5 | Postgres only. No driver abstraction. Superseded in October 2026 by per-engine support, see docs/ENGINES.md. |
| D6 | Migration analysis is deterministic (libpg_query AST), no LLM. |
| D7 | Windows, macOS, Linux (amd64 + arm64) for every feature. `CGO_ENABLED=0` always. |

## Backend stack

| Concern | Choice | Notes |
|---|---|---|
| Go | `go 1.26`, `toolchain go1.27.3` | anthropic-sdk-go v2 needs 1.25+ |
| HTTP | stdlib `net/http` ServeMux (1.22+ patterns) | no router dependency |
| Streaming to UI | SSE frames over `fetch()` + `ReadableStream` | EventSource/WebSocket can't send the token header |
| Postgres | `github.com/jackc/pgx/v5` | `CancelRequestContextWatcherHandler` so ctx cancel stops the server-side query; `QueryExecModeExec` when PgBouncer is detected |
| SQL parser | `github.com/wasilibs/go-pgquery` (WASM libpg_query, pg_query_go/v6 AST types) | ~600ms first-parse warmup, then ~0.1ms/parse |
| Local store | `modernc.org/sqlite` (pure Go) | `os.UserConfigDir()/dbscope/dbscope.db` |
| Secrets | `zalando/go-keyring` holds only an age X25519 identity; all secrets in `secrets.age` (`filippo.io/age`) | no keyring (headless Linux/WSL) → age scrypt passphrase; Windows 2560-byte blob limit avoided |
| LLM | `anthropic-sdk-go/v2` (structured output via `OutputConfig.Format`), `openai-go/v3` Chat Completions (works with Ollama `/v1`; not the Responses API) | |
| Logging | `log/slog`; `Secret` type implementing `LogValuer` + `ReplaceAttr` key filter | never log a DSN string |
| Tests | stdlib `testing`; `testcontainers-go` postgres module locally/Linux CI; `DBSCOPE_TEST_DSN` on macOS/Windows CI via `ikalnytskyi/action-setup-postgres@v8` | |
| Release | goreleaser OSS v2, quill notarize, jsign (Azure Artifact Signing), cosign keyless; homebrew_casks, scoop, winget, nfpm | |

Recovery conflict on hot standby: SQLSTATE `40001` + message containing
"conflict with recovery" (40001 alone is also a serialization failure).

## Frontend stack

| Concern | Choice |
|---|---|
| Build | Vite 8, React 19, TypeScript 7 (`tsc` = native compiler), pnpm (pinned via `packageManager`), Node >= 22.12 |
| UI kit | Tailwind CSS v4 + shadcn/ui on **Base UI** (`@base-ui/react`), shadcn Toast (Base UI), `lucide-react` icons |
| Lint/format | Biome (does not need the TS programmatic API, which TS 7 lacks) |
| Canvas | `@xyflow/react` 12; elkjs (classic worker via `elk-api` + `elk-worker.min.js?url`); d3-force + grid in a module worker |
| State | Zustand (UI + canvas + overlay stores), TanStack Query (server state) |
| SQL editor | CodeMirror 6 + `@codemirror/lang-sql` (`PostgreSQL` dialect, `schema` completion) |
| Grid | TanStack Table v9 + TanStack Virtual; own CSV serializer (prefix cells starting `= + - @ \t \r`) |
| Layout | `react-resizable-panels`, `cmdk` command palette |
| Fonts | Geist + Geist Mono via `@fontsource-variable/*` (self-hosted, works offline) |
| Tests | Vitest + Testing Library + happy-dom; Playwright e2e on all OSes; `toHaveScreenshot` baselines from one pinned Linux env |

## Repository layout

```
cmd/dbscope/            # main: `ui`, `migrate check`, `stats export`, `eval`, `version`
internal/
  agent/                # HTTP server, guard (token/Host/Origin), handlers, SSE
  store/                # SQLite + embedded migrations
  secrets/              # keyring + age vault
  conn/                 # profiles, validation, pgx pools
  schema/               # shared in-memory model (structure only, never rows)
  introspect/ infer/    # Phase 1
  query/{guard,exec,plan}/   # Phase 2
  nl/{retrieve,llm,generate,redact,summarize}/  # Phase 3
  migrate/{ingest,apply,rules,impact,report}/   # Phase 4
web/                    # Vite app; `web/dist` embedded into the binary
eval/                   # NL + migration ground-truth suites
docs/
```

### Frontend: trimmed Feature-Sliced Design

Layers `app → pages → widgets → features → entities → shared`. A slice imports
only from layers strictly below it. Cross-widget coordination (e.g. a migration
finding highlighting a canvas node) goes through stores in `entities/`.

```
web/src/
  app/        providers, theme, tokens.css, hotkeys, error boundary, query client
  pages/      onboarding/, workbench/
  widgets/    app-shell/, schema-explorer/, erd-canvas/, sql-editor/, results-dock/,
              assistant-panel/, migration-review/, command-palette/
  features/   run-query/, cancel-query/, ask-assistant/, analyze-migration/,
              add-connection/, switch-connection/, toggle-theme/, ...
  entities/   connection/, schema/, query/, assistant-turn/, migration-finding/
  shared/     api/ (typed client + SSE reader), ui/ (shadcn components), lib/, config/
```

## Canvas performance

Measured by `web/e2e/perf.spec.ts` (the `perf` Playwright project: vsync and the frame cap off, so
a frame interval is its real cost; headless Chromium rasterises on the CPU) on an Apple M2 with
8 GB, 2026-10-28. Medians of a scripted pan with the zoom swinging:

| Schema | Initial render | Overview zoom | Header-only zoom | Detailed zoom |
|---|---|---|---|---|
| 300 tables, 600 FKs | 1.5 s (layered layout ~0.9 s in a worker) | – | 4 ms | 3 ms (7 ms densest) |
| 1,500 tables, 3,000 FKs | 1.1 s | 6 ms (24 ms before) | 21 ms | – |
| 3,000 tables (canvas overview; a one-off run of the same script) | 1.7 s | 13–14 ms (zoom p95 34 ms; 114 ms before) | – | – |
| 5,000 tables, 10,000 FKs (canvas overview) | 2.5 s | 24–28 ms (zoom p95 36–75 ms; 164 ms before) | – | – |

What keeps it there:

- Level of detail by zoom (`entities/schema/ui/lod.ts`): column rows only at zoom ≥ 0.5; header-only
  cards below; on schemas over 200 tables, name-only cards below 0.25 with every foreign key drawn as
  one SVG path instead of 3,000 edge components.
- Over 2,000 tables, the overview zoom is painted on one canvas (`widgets/erd-canvas/ui/overview-canvas.tsx`):
  React Flow keeps the tables as hidden nodes (fit view and the minimap still use them), keys are one
  `Path2D`, and tables are a few `Path2D`s grouped by look, so a frame is a handful of draw calls (the
  rasterising still grows with the table count). Marks differ in line width and dash, not only colour:
  selection, search matches, focus and impact, migration changes, stubs; dimmed tables are faint. The
  canvas takes no pointer events: panning and zooming stay React Flow's, a click is hit-tested to select
  the table under it, and a double-click opens its details. Tables cannot be dragged at this zoom, and
  overlay badges (query plans, workload) appear from the header-only zoom up, as on any schema with an
  overview; zooming in past the overview brings the cards back, culled to the screen.
- React Flow culls off-screen nodes and edges except in the overview, where everything is on screen.
- The dotted background is not drawn at overview zoom, where its dots are a few pixels apart:
  ~5 ms a frame of rasterising noise.
- The minimap is two canvases (`widgets/erd-canvas/ui/canvas-minimap.tsx`): tables are redrawn when
  the nodes change (a layout, a drag), and a pan or zoom redraws only the viewport rectangle. React Flow's SVG minimap
  re-rendered a rectangle per table on every frame: 24 ms a frame at 1,500 tables.
- Layout runs in a worker; positions are saved per schema selection, so a reopen skips it.

Removing the cards' text below a readable size did not help (re-rendering every card when the zoom
crossed that threshold cost more than it saved), so it is not done. Header-only zoom on 1,500 tables
stays near 50 fps while the zoom changes every frame; pans alone are at 60.

## Agent security model

- Bind `127.0.0.1` only; random 256-bit token per launch.
- CLI opens `http://127.0.0.1:<port>/#t=<token>`. The fragment never reaches the
  server; the UI keeps it in `sessionStorage` and sends `X-Dbscope-Token`.
- Every request: `Host` must be loopback:<port>; `Origin`, if present, must be
  `http://` + loopback:<port> (plus an explicit `--dev-origin` for Vite dev).
  `/api/*` additionally requires the token (constant-time compare).
- CSP `default-src 'self'`, `frame-ancestors 'none'`, nosniff, no-referrer.
- Credentials never leave the agent: the API returns `hasPassword`, never the value.

## Design system

### Layout (workbench)

```
+------------------------------------------------------------------------------+
| PROD-REPLICA  READ-ONLY  db @ host  user                       (prod only)   |  32px banner
+------------------------------------------------------------------------------+
| dbscope | <connection> [ENV] |  Search or run a command (Cmd/Ctrl+K) | theme |  40px top bar
+----+-------------+-----------------------------------------+------------------+
|rail| Schema      | tabs: [ERD] [query-1.sql] [0042.sql]    | Assistant        |
|48px| explorer    |                                         | (Cmd/Ctrl+I)     |
|    | 240px       |   centre: ERD canvas                    | 360px            |
|    | (Cmd/Ctrl+B)+-----------------------------------------+                  |
|    |             | dock: Results | Messages | Plan | Risk  |                  |
|    |             | (Cmd/Ctrl+J) 280px, collapsed until 1st run                |
+----+-------------+-----------------------------------------+------------------+
| [ENV] connected | tx | last: 120 ms, 42 rows | schema snapshot 2 min ago      |  24px status bar
+------------------------------------------------------------------------------+
```

Panels resize by drag or keyboard (focused splitter + arrows), reset on
double-click, sizes persist. F6 cycles regions. Below 1280px the assistant
becomes an overlay drawer.

### Tokens (CSS variables in `web/src/app/tokens.css`)

Surfaces `--surface-canvas|subtle|raised|overlay|hover|selected`; borders
`--border-default|control|focus`; text `--text-primary|muted|on-emphasis|link`;
`--accent-fg|emphasis`; `--status-{success,warn,danger,info}-{fg,emphasis,subtle}`;
`--env-{local,staging,prod}-fg`, `--env-prod-banner-bg|fg`. Spacing 4px base
(0,4,8,12,16,24,32,48). Radius 4/6/8/full. Type: sans 11/12/13(default)/14/16/20,
mono for SQL, identifiers and numbers. Row density 24/28/32.

Verified WCAG contrast (worst case vs `surface.subtle`; light `#F6F8FA`, dark `#161B22`):

| Token | Light | Dark |
|---|---|---|
| surface.canvas / subtle | #FFFFFF / #F6F8FA | #0D1117 / #161B22 |
| text.primary | #1F2328 | #E6EDF3 |
| text.muted | #59636E (5.74) | #9198A1 (5.94) |
| border.control | #818B98 (3.24) | #656C76 (3.26) |
| accent.fg / focus ring / info | #0969DA (4.88) | #58A6FF (6.85) |
| status.success.fg | #1A7F37 (4.77) | #3FB950 (6.81) |
| status.warn.fg | #9A6700 (4.57) | #D29922 (6.85) |
| status.danger.fg | #CF222E (5.03) | #F85149 (5.16) |
| env.local.fg | #0F766E (5.14) | #2DD4BF (9.29) |
| env.staging.fg | #BC4C00 (4.73) | #DB6D28 (5.13) |
| env.prod.fg | #A40E26 (7.39) | #FF7B72 (6.86) |
| env.prod banner (white text) | #A40E26 (7.87) | #B62324 (6.45) |

Status badges use outline style (fg text + border on a surface). Risk levels:
Low=info (circle), Medium=warn (triangle), High=danger (octagon),
Critical=filled danger (octagon with bar).

### UX rules

1. First run: one connection form (paste URL or fields) + "Test connection". Env tag required; suggest `local` only for loopback hosts. "Open sample schema" works with no database.
2. Prod-replica is loud and read-only: banner, `[PROD]` tab-title prefix, status bar with icon + text, enforced server-side.
3. Statement classification uses the SQL parser, never regex.
4. Generated SQL runs only through the guarded read-only executor (docs/QUERY-SAFETY.md) and always shows the exact `executedSql` beside its result; a query over the profile's cost limit (always the case for expensive queries on prod-replica) stops and asks before running, and sample rows never leave the machine on prod-replica without an explicit confirmation. Any rewrite dbscope applies (timeouts, deparsing) is shown.
5. Assistant answer card order: question restated → SQL (highlight, copy, open in editor) → step explanation → editable assumption chips → clarifying-question buttons → fact badges.
6. Badges show facts (reads only, N tables (click → highlight), plan estimate), never model confidence.
7. Status never by colour alone: icon + text label always. NULL renders as italic "NULL", distinct from empty string.
8. Empty states say what belongs there, one primary action, its shortcut.
9. Loading: counted progress for introspection, elapsed timer + Cancel (Cmd/Ctrl+.) for queries, Stop for streams; nothing blocks the rest of the UI.
10. Errors show SQLSTATE, message, detail, hint; map position into the editor; connection errors name the cause (auth/network/TLS/timeout). Never only a vanishing toast.
11. Keyboard first: Cmd/Ctrl+K palette, `?` shortcut sheet, shortcuts shown in tooltips/menus; single-key shortcuts never fire in text fields; avoid browser-reserved combos.
12. Visible 2px focus ring everywhere; roving tabindex in grid and tree; schema explorer is the non-visual equivalent of the canvas.
13. Streaming answers don't announce per token; a polite live region announces completion. Respect `prefers-reduced-motion`. Theme follows system with manual override.
14. Show what leaves the machine: the assistant names the model/endpoint and what context was sent (schema only / profile / rows).
