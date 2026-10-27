import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import type { Route } from "@playwright/test";
import { json } from "./schema-stub";

/** A loose mirror of docs/API.md Phase 4 for the stub; e2e does not import app code. */
export interface StubAnalyzeBody {
  files: { name: string; sql: string }[];
  txMode?: string;
  assumeLockTimeout?: boolean;
}

interface StubStatement {
  file: string;
  line: number;
  endLine: number;
  rating: string;
  lockTimeout: boolean;
  findings: { ruleId: string; rating: string; message: string; safeAlternative?: string }[];
  [key: string]: unknown;
}

export interface StubReport {
  pgVersion: number;
  rating: string;
  statements: StubStatement[];
  diff: { added: string[]; dropped: string[]; altered: string[]; columns: Record<string, unknown>[] };
  impact: Record<string, { kind: string; name: string; certainty: string }[]>;
  unanalyzable: { file: string; line: number; message: string }[];
  warnings: string[] | null;
  base: string;
}

export interface MigrationStubOptions {
  /** Replaces the default answer to POST …/migrations/analyze. */
  analyze?: (body: StubAnalyzeBody, call: number) => { status: number; body: unknown };
  /** Delay before answering, ms. */
  analyzeDelay?: number;
}

export interface MigrationStubState {
  requests: { path: string; body: StubAnalyzeBody }[];
}

const dir = (name: string) => fileURLToPath(new URL(`../../src/test/fixtures/${name}`, import.meta.url));

/** The SQL files the recorded report was made from. */
export const MIGRATION_FILES = ["0042_orders.sql", "0043_cleanup.sql"].map((name) => ({
  name,
  sql: readFileSync(dir(`migrations/${name}`), "utf8"),
}));

/**
 * A real POST /api/sample/migrations/analyze response for MIGRATION_FILES
 * (recorded 2026-10-27): DROP TABLE shop.shipments is Critical with
 * "fails without CASCADE", a NOT ENFORCED CHECK is a DBS-PARSE statement,
 * and column impact is heuristic. Against a live database the catalog
 * queries also report triggers and functions; two such items are added so the
 * stub covers those name formats (and a heuristic table dependent) too.
 */
export function migrationReport(): StubReport {
  const report = JSON.parse(readFileSync(dir("migration-report.json"), "utf8")) as StubReport;
  report.base = "live";
  report.pgVersion = 170002;
  report.impact["shop.orders"]?.push(
    { kind: "function", name: "shop.order_total(bigint)", certainty: "heuristic" },
    { kind: "trigger", name: "refund_audit ON shop.refunds", certainty: "heuristic" },
  );
  return report;
}

/** The default answer: the recorded report, with statements moved to the names sent when they differ. */
function defaultAnswer(body: StubAnalyzeBody): { status: number; body: unknown } {
  const report = migrationReport();
  const names = body.files.map((f) => f.name);
  if (!MIGRATION_FILES.every((f) => names.includes(f.name))) {
    const first = names[0] ?? "pasted.sql";
    report.statements = report.statements
      .filter((s) => s.file === "0043_cleanup.sql")
      .map((s) => ({ ...s, file: first }));
    report.unanalyzable = report.unanalyzable.map((u) => ({ ...u, file: first }));
  }
  if (body.assumeLockTimeout) report.statements = report.statements.map((s) => ({ ...s, lockTimeout: true }));
  return { status: 200, body: report };
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/** Handles POST /api/sample/migrations/analyze and /api/profiles/{id}/migrations/analyze. */
export async function handleMigrationRoute(
  route: Route,
  pathname: string,
  method: string,
  options: MigrationStubOptions,
  state: MigrationStubState,
): Promise<boolean> {
  const sample = pathname === "/api/sample/migrations/analyze";
  if (method !== "POST" || !(sample || /^\/api\/profiles\/[^/]+\/migrations\/analyze$/.test(pathname))) return false;
  const body = route.request().postDataJSON() as StubAnalyzeBody;
  state.requests.push({ path: pathname, body });
  if (options.analyzeDelay) await sleep(options.analyzeDelay);
  const answer = options.analyze ? options.analyze(body, state.requests.length) : defaultAnswer(body);
  await json(route, answer.status, answer.body);
  return true;
}
