/**
 * Wire types for the Phase 2 query API (docs/API.md "Phase 2"). They mirror
 * internal/query/exec.Result, internal/query/plan.Node and plan.Overlay.
 */

export interface QueryRequest {
  /** Exactly one SELECT (incl. VALUES / TABLE / WITH ... SELECT). */
  sql: string;
  /** Default 500, 1..5000. */
  maxRows?: number;
  /** EXPLAIN ANALYZE as well; this executes the query a second time. */
  analyze?: boolean;
  /** Re-send true after a 409 cost_confirmation_required. */
  confirmCost?: boolean;
}

export type ExplainRequest = Omit<QueryRequest, "maxRows">;

export interface ResultColumn {
  name: string;
  /** format_type() output, e.g. "bigint" or "character varying(80)". */
  type: string;
  typeOid: number;
  /** "schema.name" when the column comes straight from a table (partitions mapped to their root). */
  relation?: string;
  column?: string;
}

/** Text-format values mapped per docs/QUERY-SAFETY.md "Value mapping": json/jsonb arrive as raw JSON. */
export type CellValue = string | number | boolean | null | { [key: string]: unknown } | unknown[];

export interface PlanFlag {
  level: "info" | "warn" | "danger";
  code: string;
  message: string;
}

export interface PlanNode {
  nodeType: string;
  /** On the root: the engine names steps without estimates (SQLite, MySQL), and dbscope checks none of them. */
  stepsOnly?: boolean;
  relation?: string;
  alias?: string;
  index?: string;
  joinType?: string;
  condition?: string;
  startupCost: number;
  totalCost: number;
  planRows: number;
  /** Per loop, as EXPLAIN prints it (analyze only). */
  actualRows?: number;
  actualLoops?: number;
  /** Per loop (analyze only). */
  actualTotalMs?: number;
  rowsRemovedByFilter?: number;
  /** Exclusive cost / total cost, 0..1. */
  costShare: number;
  /** Exclusive time / total time, 0..1 (analyze only). */
  timeShare?: number;
  flags: PlanFlag[];
  children: PlanNode[];
}

export type OverlayLevel = "good" | "warn" | "danger";

export interface OverlayScan {
  nodeType: string;
  index?: string;
  planRows: number;
  actualRows?: number;
}

export interface OverlayRelation {
  level: OverlayLevel;
  scans: OverlayScan[];
  reasons: string[];
}

export interface OverlayJoin {
  from: string;
  to: string;
  method: "Hash Join" | "Merge Join" | "Nested Loop";
  joinType: string;
  condition?: string;
}

export interface Overlay {
  /** Keyed "schema.name". */
  relations: Record<string, OverlayRelation>;
  joins: OverlayJoin[];
}

export interface QueryResult {
  columns: ResultColumn[];
  rows: CellValue[][];
  truncated: boolean;
  rowCount: number;
  durationMs: number;
  /** The deparsed statement actually run. */
  executedSql: string;
  plan: PlanNode;
  overlay: Overlay;
  warnings: string[];
}

export interface ExplainResult {
  executedSql: string;
  plan: PlanNode;
  overlay: Overlay;
  warnings: string[];
}

export type HistoryStatus = "ok" | "error" | "rejected" | "cancelled";

export interface HistoryEntry {
  id: string;
  sql: string;
  startedAt: string;
  durationMs: number;
  rowCount: number;
  status: HistoryStatus;
  errorCode?: string;
  source: "query" | "ask";
  /** The assistant question, for source "ask". */
  question?: string;
}

/** Guard rejection reasons sent in the 422 `detail`. */
export type GuardReason =
  | "multiple_statements"
  | "not_select"
  | "writes_data"
  | "locking_clause"
  | "select_into"
  | "function_not_allowed"
  | "syntax";
