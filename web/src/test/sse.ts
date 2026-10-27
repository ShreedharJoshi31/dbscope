import { HttpResponse } from "msw/http";
import type { AskAnswer, AskContext, QueryResult } from "@/shared/api";

export type Frame = [event: string, data: unknown];

const encoder = new TextEncoder();

/**
 * An SSE response that writes each frame split across two chunks, like a
 * real flushed stream. `hold` keeps it open after the last frame.
 */
export function sseResponse(frames: Frame[], options: { hold?: boolean } = {}) {
  let cancelled = false;
  const stream = new ReadableStream<Uint8Array>({
    async start(controller) {
      for (const [event, data] of frames) {
        const text = `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
        const cut = Math.floor(text.length / 2);
        controller.enqueue(encoder.encode(text.slice(0, cut)));
        await Promise.resolve();
        controller.enqueue(encoder.encode(text.slice(cut)));
      }
      if (!options.hold) controller.close();
    },
    cancel() {
      cancelled = true;
    },
  });
  return {
    response: new HttpResponse(stream, { headers: { "Content-Type": "text/event-stream" } }),
    cancelled: () => cancelled,
  };
}

export const CONTEXT: AskContext = {
  provider: "anthropic",
  model: "claude-opus-5-5",
  schemaMode: "retrieved",
  tables: ["shop.orders", "shop.customers"],
  omittedColumns: ["app.users.password_hash"],
  sends: "schema",
};

export const ANSWER: AskAnswer = {
  kind: "sql",
  sql: "SELECT c.name, count(*) FROM shop.orders o JOIN shop.customers c ON c.id = o.customer_id WHERE o.placed_at >= now() - interval '30 days' GROUP BY 1",
  explanationSteps: ["Join orders to customers.", "Count orders in the last 30 days."],
  assumptions: [
    { label: "Last 30 days", sqlFragment: "o.placed_at >= now() - interval '30 days'" },
    { label: "Every order counts", sqlFragment: "" },
  ],
  tables: ["shop.orders", "shop.customers"],
  repaired: false,
};

export const RESULT: QueryResult = {
  columns: [
    { name: "name", type: "text", typeOid: 25, relation: "shop.customers", column: "name" },
    { name: "count", type: "bigint", typeOid: 20 },
  ],
  rows: [
    ["Ada", 4],
    ["<img src=x onerror=alert(1)>", 2],
  ],
  truncated: false,
  rowCount: 2,
  durationMs: 9,
  executedSql: "SELECT c.name, count(*) FROM shop.orders o JOIN shop.customers c ON c.id = o.customer_id GROUP BY 1",
  plan: {
    nodeType: "HashAggregate",
    startupCost: 1,
    totalCost: 120,
    planRows: 2,
    costShare: 1,
    flags: [],
    children: [],
  },
  overlay: {
    relations: { "shop.orders": { level: "good", scans: [{ nodeType: "Seq Scan", planRows: 10 }], reasons: [] } },
    joins: [{ from: "shop.orders", to: "shop.customers", method: "Hash Join", joinType: "Inner" }],
  },
  warnings: [],
};

export const DONE = {
  conversationId: "conv-1",
  durationMs: 1200,
  usage: { inputTokens: 1, outputTokens: 1, cacheReadTokens: 0 },
};

/** status → context → answer → result → summary → done, with the stages in between. */
export function fullAnswer(answer: AskAnswer = ANSWER): Frame[] {
  return [
    ["status", { stage: "retrieving" }],
    ["context", CONTEXT],
    ["status", { stage: "generating" }],
    ["answer", answer],
    ["status", { stage: "checking" }],
    ["status", { stage: "running" }],
    ["result", RESULT],
    ["summary", { text: "**Ada** leads with 4 orders. [click](https://evil.example)", mode: "profile" }],
    ["done", DONE],
  ];
}
