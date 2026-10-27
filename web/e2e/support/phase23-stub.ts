import type { Route } from "@playwright/test";
import { EXECUTED_SQL, JOIN_OVERLAY, JOIN_PLAN, queryResult, type StubHistoryEntry } from "./query-fixtures";
import { json } from "./schema-stub";
import { type AskScript, registerAskScript, type SseFrame } from "./sse-server";

/** Mirrors docs/API.md Phase 2 and 3 for the e2e stub. */
export interface QueryBody {
  sql: string;
  maxRows?: number;
  analyze?: boolean;
  confirmCost?: boolean;
}

export interface StubLlmSettings {
  provider: "anthropic" | "openai_compat" | "none";
  model: string;
  summaryModel: string;
  baseUrl?: string;
  effort?: "low" | "medium" | "high";
  hasApiKey: boolean;
}

export interface GlossaryEntry {
  term: string;
  definition: string;
  sql: string;
}

export interface Phase23Options {
  llm?: StubLlmSettings;
  glossary?: GlossaryEntry[];
  /** SSE frames for POST /ask; defaults to standardAsk. */
  ask?: AskScript;
  /** Delay before answering POST /query, ms. */
  queryDelay?: number;
}

export interface Phase23State {
  queries: QueryBody[];
  explains: QueryBody[];
  history: StubHistoryEntry[];
  llm: StubLlmSettings;
  llmPuts: Record<string, unknown>[];
  glossary: GlossaryEntry[];
  glossaryPuts: GlossaryEntry[][];
  asks: { url: string; calls: import("./sse-server").AskBody[] } | null;
  aborted: number;
}

const ANTHROPIC_MODELS = ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"];

export function newPhase23State(options: Phase23Options): Phase23State {
  return {
    queries: [],
    explains: [],
    history: [],
    llm: options.llm ?? { provider: "none", model: "", summaryModel: "", hasApiKey: false },
    llmPuts: [],
    glossary: options.glossary ?? [],
    glossaryPuts: [],
    asks: null,
    aborted: 0,
  };
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

function envelope(status: number, error: Record<string, unknown>) {
  return { status, body: { error } };
}

/** The guard and executor, as far as the specs need them; markers in the SQL pick the outcome. */
function queryOutcome(body: QueryBody): { status: number; body: unknown } {
  const sql = body.sql;
  const typo = sql.search(/\bWHER\b/);
  if (typo >= 0) {
    return envelope(422, {
      code: "rejected",
      message: 'syntax error at or near "WHER"',
      detail: "syntax",
      position: typo + 1,
    });
  }
  if (/for\s+update/i.test(sql)) {
    return envelope(422, {
      code: "rejected",
      message: "SELECT ... FOR UPDATE locks rows; dbscope only reads",
      detail: "locking_clause",
    });
  }
  if (/missing_table/.test(sql)) {
    return envelope(400, {
      code: "database",
      message: 'relation "missing_table" does not exist',
      sqlstate: "42P01",
      position: 15,
    });
  }
  if (/expensive/i.test(sql) && !body.confirmCost) {
    return envelope(409, {
      code: "cost_confirmation_required",
      message:
        "estimated cost 2450000 is over this profile's limit of 1000000; re-send with confirmCost to run it anyway",
      estimatedCost: 2_450_000,
      estimatedRows: 1_800_000,
    });
  }
  const limit = Number(sql.match(/limit\s+(\d+)/i)?.[1] ?? 3);
  const maxRows = body.maxRows ?? 500;
  const rows = Math.min(limit, maxRows);
  const result = queryResult(rows, { truncated: limit > maxRows });
  if (body.confirmCost) {
    result.warnings = [
      "Ran with an estimated cost of 2450000, over this profile's limit of 1000000, because you confirmed it.",
    ];
  }
  return { status: 200, body: result };
}

function record(
  state: Phase23State,
  sql: string,
  status: number,
  rows: number,
  source: "query" | "ask",
  question?: string,
) {
  state.history.unshift({
    id: `h${state.history.length + 1}`,
    sql,
    startedAt: "2026-10-26T09:59:30Z",
    durationMs: 42.5,
    rowCount: status === 200 ? rows : 0,
    status: status === 200 ? "ok" : status === 422 || status === 409 ? "rejected" : "error",
    ...(status === 409 ? { errorCode: "cost_confirmation_required" } : {}),
    source,
    ...(question ? { question } : {}),
  });
}

function saveSettings(state: Phase23State, input: Record<string, unknown>): StubLlmSettings {
  const provider = input.provider as StubLlmSettings["provider"];
  const changedProvider = provider !== state.llm.provider;
  let hasApiKey = changedProvider ? false : state.llm.hasApiKey;
  if (typeof input.apiKey === "string") hasApiKey = input.apiKey !== "";
  if (provider === "none") return { provider: "none", model: "", summaryModel: "", hasApiKey: false };
  if (provider === "anthropic") {
    return {
      provider,
      model: (input.model as string) || "claude-opus-5-5",
      summaryModel: (input.summaryModel as string) || "claude-haiku-5-5",
      effort: (input.effort as StubLlmSettings["effort"]) ?? "medium",
      hasApiKey,
    };
  }
  const baseUrl = String(input.baseUrl ?? "");
  return {
    provider,
    model: String(input.model),
    summaryModel: String(input.summaryModel || input.model),
    baseUrl: baseUrl.endsWith("/") ? baseUrl : `${baseUrl}/`,
    hasApiKey,
  };
}

async function handleLlm(route: Route, pathname: string, method: string, state: Phase23State): Promise<boolean> {
  if (pathname === "/api/llm/settings" && method === "GET") {
    await json(route, 200, state.llm);
    return true;
  }
  if (pathname === "/api/llm/settings" && method === "PUT") {
    const input = route.request().postDataJSON() as Record<string, unknown>;
    state.llmPuts.push(input);
    state.llm = saveSettings(state, input);
    await json(route, 200, state.llm);
    return true;
  }
  if (pathname === "/api/llm/test" && method === "POST") {
    const ok = state.llm.provider === "openai_compat" || state.llm.hasApiKey;
    await json(route, 200, {
      ok,
      provider: state.llm.provider,
      model: state.llm.model,
      latencyMs: 412,
      ...(ok ? {} : { error: { kind: "auth", message: "invalid x-api-key" } }),
    });
    return true;
  }
  if (pathname === "/api/llm/models" && method === "GET") {
    await json(route, 200, state.llm.provider === "anthropic" ? ANTHROPIC_MODELS : ["llama3.1:8b", "qwen2.5-coder:7b"]);
    return true;
  }
  return false;
}

/** Phase 2 and 3 routes. Returns true when it answered. */
export async function handlePhase23Route(
  route: Route,
  pathname: string,
  method: string,
  options: Phase23Options,
  state: Phase23State,
): Promise<boolean> {
  if (await handleLlm(route, pathname, method, state)) return true;
  const m = pathname.match(/^\/api\/profiles\/([^/]+)\/(query|explain|history|glossary|ask)$/);
  if (!m) return false;
  const what = m[2];
  const request = route.request();

  if (what === "query" && method === "POST") {
    const body = request.postDataJSON() as QueryBody;
    state.queries.push(body);
    const delay = /pg_sleep/.test(body.sql) ? 10_000 : (options.queryDelay ?? 0);
    if (delay) await sleep(delay);
    const outcome = queryOutcome(body);
    const rows = (outcome.body as { rowCount?: number }).rowCount ?? 0;
    record(state, body.sql, outcome.status, rows, "query");
    // A cancelled fetch makes fulfill throw; the app has already moved on.
    await json(route, outcome.status, outcome.body).catch(() => {
      state.aborted++;
    });
    return true;
  }
  if (what === "explain" && method === "POST") {
    const body = request.postDataJSON() as QueryBody;
    state.explains.push(body);
    const outcome = queryOutcome({ ...body, confirmCost: true });
    if (outcome.status !== 200) {
      await json(route, outcome.status, outcome.body);
      return true;
    }
    await json(route, 200, { executedSql: EXECUTED_SQL, plan: JOIN_PLAN, overlay: JOIN_OVERLAY, warnings: [] });
    return true;
  }
  if (what === "history" && method === "GET") {
    await json(route, 200, state.history);
    return true;
  }
  if (what === "glossary" && method === "GET") {
    await json(route, 200, state.glossary);
    return true;
  }
  if (what === "glossary" && method === "PUT") {
    state.glossary = request.postDataJSON() as GlossaryEntry[];
    state.glossaryPuts.push(state.glossary);
    await json(route, 204);
    return true;
  }
  if (what === "ask" && method === "POST") {
    if (state.llm.provider === "none") {
      await json(route, 409, {
        error: { code: "llm_not_configured", message: "choose an LLM provider in settings first" },
      });
      return true;
    }
    state.asks ??= await registerAskScript((body, call) => {
      const frames = (options.ask ?? standardAsk)(body, call);
      const answer = frames.find((f) => f.event === "answer")?.data as { sql?: string } | undefined;
      if (answer?.sql) record(state, answer.sql, 200, 3, "ask", body.question);
      return frames;
    });
    await route.continue({ url: state.asks.url });
    return true;
  }
  return false;
}

export const ANSWER_SQL =
  "SELECT c.name, count(*) AS orders\nFROM shop.orders o\nJOIN shop.customers c ON c.id = o.customer_id\nWHERE o.placed_at >= now() - interval '30 days'\nGROUP BY c.name\nORDER BY orders DESC\nLIMIT 10";

export const CONTEXT = {
  provider: "anthropic",
  model: "claude-opus-5-5",
  schemaMode: "full",
  tables: ["shop.orders", "shop.customers", "shop.order_items"],
  omittedColumns: ["app.api_keys.token_hash"],
  sends: "schema",
};

function answer(
  sql: string,
  assumptions = [{ label: "Last 30 days", sqlFragment: "o.placed_at >= now() - interval '30 days'" }],
) {
  return {
    kind: "sql",
    sql,
    explanationSteps: [
      "Join orders to customers on customer_id.",
      "Keep orders placed in the time window.",
      "Count orders per customer and keep the top 10.",
    ],
    assumptions: [...assumptions, { label: "Ties broken arbitrarily", sqlFragment: "ORDER BY orders DESC" }],
    tables: ["shop.orders", "shop.customers"],
    repaired: false,
  };
}

const RESULT_ROWS = [
  ["Customer 07", 41],
  ["Customer 12", 37],
  ["Customer 03", 29],
];

function sqlResult(sql: string) {
  return {
    columns: [
      { name: "name", type: "text", typeOid: 25, relation: "shop.customers", column: "name" },
      { name: "orders", type: "bigint", typeOid: 20 },
    ],
    rows: RESULT_ROWS,
    truncated: false,
    rowCount: RESULT_ROWS.length,
    durationMs: 18.2,
    executedSql: sql.replace(/\n/g, " "),
    plan: JOIN_PLAN,
    overlay: JOIN_OVERLAY,
    warnings: [],
  };
}

const DONE = {
  conversationId: "conv-1",
  durationMs: 2140.5,
  usage: { inputTokens: 5120, outputTokens: 310, cacheReadTokens: 4800 },
};

/** retrieving → context → generating → answer → checking → running → result → summarizing → summary → done. */
export const standardAsk: AskScript = (body) => {
  const override = body.assumptionOverrides?.find((o) => o.label !== "Ties broken arbitrarily");
  const sql = override
    ? ANSWER_SQL.replace("now() - interval '30 days'", override.sqlFragment.replace(/^o\.placed_at >= /, ""))
    : ANSWER_SQL;
  const assumptions = override ? [override] : undefined;
  const frames: SseFrame[] = [
    { event: "status", data: { stage: "retrieving" } },
    { event: "context", data: CONTEXT, delay: 60 },
    { event: "status", data: { stage: "generating" } },
    { event: "answer", data: answer(sql, assumptions), delay: 250 },
    { event: "status", data: { stage: "checking" } },
    { event: "status", data: { stage: "running" }, delay: 40 },
    { event: "result", data: sqlResult(sql), delay: 80 },
    // Profile mode: the agent counts the summary itself, with no summarizing stage.
    {
      event: "summary",
      data: {
        text: "3 rows. name has 3 distinct values. orders ranges from 29 to 41.",
        mode: "profile",
      },
      delay: 120,
    },
    { event: "done", data: DONE },
  ];
  return frames;
};

/** "active customers" asks first; the answer then runs like standardAsk. */
export const clarifyAsk: AskScript = (body, call) => {
  if (!body.clarification) {
    return [
      { event: "status", data: { stage: "retrieving" } },
      { event: "context", data: CONTEXT },
      { event: "status", data: { stage: "generating" } },
      {
        event: "answer",
        delay: 150,
        data: {
          kind: "clarify",
          sql: "",
          explanationSteps: [],
          assumptions: [],
          tables: ["shop.customers"],
          clarifyingQuestion: {
            question: "What makes a customer active?",
            options: ["Ordered in the last 30 days", "Signed in during the last 30 days"],
          },
          repaired: false,
        },
      },
      { event: "done", data: DONE },
    ];
  }
  return standardAsk(body, call);
};

/** Prod-replica in rows mode: the first ask stops for confirmation; the confirmed re-send runs. */
export const rowsConfirmAsk: AskScript = (body, call) => {
  if (!body.confirmRows) {
    return [
      { event: "status", data: { stage: "retrieving" } },
      { event: "context", data: { ...CONTEXT, sends: "schema+rows" } },
      { event: "status", data: { stage: "generating" } },
      { event: "answer", data: answer(ANSWER_SQL), delay: 120 },
      { event: "status", data: { stage: "checking" } },
      { event: "needs_confirmation", data: { reason: "rows", conversationId: "conv-rows" } },
    ];
  }
  return standardAsk(body, call).map((f) =>
    f.event === "context"
      ? { ...f, data: { ...CONTEXT, sends: "schema+rows" } }
      : f.event === "summary"
        ? { ...f, data: { text: "Customer 07 leads with 41 orders.", mode: "rows" } }
        : f,
  );
};

/** Long pauses so a spec can press Stop mid-stream. */
export const slowAsk: AskScript = () => [
  { event: "status", data: { stage: "retrieving" } },
  { event: "context", data: CONTEXT },
  { event: "status", data: { stage: "generating" } },
  { event: "answer", data: answer(ANSWER_SQL), delay: 15_000 },
  { event: "done", data: DONE },
];
