/**
 * Phase 2 fixtures shaped like internal/query/exec.Result and plan.Node
 * (docs/API.md "Phase 2"); kept local so e2e does not import app code.
 */

export interface StubColumn {
  name: string;
  type: string;
  typeOid: number;
  relation?: string;
  column?: string;
}

export type StubCell = string | number | boolean | null | Record<string, unknown> | unknown[];

export interface StubPlanNode {
  nodeType: string;
  relation?: string;
  alias?: string;
  index?: string;
  joinType?: string;
  condition?: string;
  startupCost: number;
  totalCost: number;
  planRows: number;
  actualRows?: number;
  actualLoops?: number;
  actualTotalMs?: number;
  rowsRemovedByFilter?: number;
  costShare: number;
  timeShare?: number;
  flags: { level: "info" | "warn" | "danger"; code: string; message: string }[];
  children: StubPlanNode[];
}

export const ORDERS_COLUMNS: StubColumn[] = [
  { name: "id", type: "bigint", typeOid: 20, relation: "shop.orders", column: "id" },
  { name: "customer", type: "text", typeOid: 25, relation: "shop.customers", column: "name" },
  { name: "status", type: "shop.order_status", typeOid: 16_390, relation: "shop.orders", column: "status" },
  { name: "total", type: "numeric", typeOid: 1700 },
  { name: "note", type: "text", typeOid: 25 },
  { name: "placed_at", type: "timestamp with time zone", typeOid: 1184, relation: "shop.orders", column: "placed_at" },
];

const STATUSES = ["pending", "paid", "shipped", "refunded"];
const NOTES: StubCell[] = [
  null,
  "",
  "=SUM(A1:A9)",
  'gift, "fragile"',
  "-5 off",
  "line one\nline two",
  "@home",
  "plain",
];

/** Deterministic rows for ORDERS_COLUMNS: NULL, empty strings and formula-looking text included. */
export function orderRows(count: number): StubCell[][] {
  return Array.from({ length: count }, (_, i) => [
    i + 1,
    `Customer ${String((i % 97) + 1).padStart(2, "0")}`,
    STATUSES[i % STATUSES.length] as string,
    (((i * 37) % 10_000) / 100 + 10).toFixed(2),
    NOTES[i % NOTES.length] ?? null,
    `2026-10-${String((i % 27) + 1).padStart(2, "0")} 09:${String(i % 60).padStart(2, "0")}:00+00`,
  ]);
}

export const JOIN_PLAN: StubPlanNode = {
  nodeType: "Hash Join",
  joinType: "Inner",
  condition: "(o.customer_id = c.id)",
  startupCost: 38.25,
  totalCost: 48_210.5,
  planRows: 2000,
  costShare: 0.12,
  flags: [],
  children: [
    {
      nodeType: "Seq Scan",
      relation: "shop.orders",
      alias: "o",
      condition: "(o.status = 'paid'::shop.order_status)",
      startupCost: 0,
      totalCost: 42_100,
      planRows: 2000,
      costShare: 0.84,
      flags: [
        { level: "danger", code: "seq_scan_filter", message: "Seq Scan reads about 2.1M rows to keep an estimated 2K" },
      ],
      children: [],
    },
    {
      nodeType: "Hash",
      startupCost: 25,
      totalCost: 25,
      planRows: 1000,
      costShare: 0.01,
      flags: [],
      children: [
        {
          nodeType: "Index Scan",
          relation: "shop.customers",
          alias: "c",
          index: "customers_pkey",
          startupCost: 0.29,
          totalCost: 24.5,
          planRows: 1000,
          costShare: 0.03,
          flags: [],
          children: [],
        },
      ],
    },
  ],
};

export const JOIN_OVERLAY = {
  relations: {
    "shop.orders": {
      level: "danger",
      scans: [{ nodeType: "Seq Scan", planRows: 2000 }],
      reasons: ["Seq Scan reads about 2.1M rows to keep an estimated 2K"],
    },
    "shop.customers": {
      level: "good",
      scans: [{ nodeType: "Index Scan", index: "customers_pkey", planRows: 1000 }],
      reasons: [],
    },
  },
  joins: [
    {
      from: "shop.orders",
      to: "shop.customers",
      method: "Hash Join",
      joinType: "Inner",
      condition: "(o.customer_id = c.id)",
    },
  ],
};

export const EXECUTED_SQL =
  "SELECT o.id, c.name AS customer, o.status, o.total, o.note, o.placed_at FROM shop.orders o JOIN shop.customers c ON o.customer_id = c.id WHERE o.status = 'paid' LIMIT 501";

export function queryResult(rows: number, options: { truncated?: boolean; executedSql?: string } = {}) {
  return {
    columns: ORDERS_COLUMNS,
    rows: orderRows(rows),
    truncated: options.truncated ?? false,
    rowCount: rows,
    durationMs: 42.5,
    executedSql: options.executedSql ?? EXECUTED_SQL,
    plan: JOIN_PLAN,
    overlay: JOIN_OVERLAY,
    warnings: [] as string[],
  };
}

export interface StubHistoryEntry {
  id: string;
  sql: string;
  startedAt: string;
  durationMs: number;
  rowCount: number;
  status: "ok" | "error" | "rejected" | "cancelled";
  errorCode?: string;
  source: "query" | "ask";
  question?: string;
}
