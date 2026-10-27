import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import type { Route } from "@playwright/test";

/**
 * A minimal mirror of internal/schema.Model for the stubs; e2e does not import
 * app code. The fixture is hand-written to the Go JSON shape (see
 * src/test/fixtures/sample-model.json).
 */
export interface StubForeignKey {
  name: string;
  columns: string[];
  ref: { schema: string; name: string };
  refColumns: string[];
  validated: boolean;
  inferred?: boolean;
  confidence?: number;
  onDelete?: string;
  onUpdate?: string;
}

export interface StubRelation {
  schema: string;
  name: string;
  kind: string;
  rowEstimate: number | null;
  pages: number;
  columns: { name: string; type: string; nullable: boolean }[] | null;
  primaryKey?: string[];
  foreignKeys?: StubForeignKey[];
  stub?: boolean;
  [key: string]: unknown;
}

export interface StubModel {
  serverVersion: number;
  schemas: string[] | null;
  relations: StubRelation[] | null;
  enums: unknown[] | null;
  introspectedAt: string;
  durationMs: number;
}

const FIXTURE_PATH = fileURLToPath(new URL("../../src/test/fixtures/sample-model.json", import.meta.url));

/** A fresh copy of the sample fixture (42 relations, 55 foreign keys, 2 inferred). */
export function sampleModel(): StubModel {
  return JSON.parse(readFileSync(FIXTURE_PATH, "utf8")) as StubModel;
}

/** What the agent does with stored decisions: rejected keys are omitted, accepted ones get confidence 1. */
export function applyDecisions(model: StubModel, decisions: ReadonlyMap<string, string>): StubModel {
  const copy = structuredClone(model);
  for (const r of copy.relations ?? []) {
    if (!r.foreignKeys) continue;
    r.foreignKeys = r.foreignKeys
      .filter((fk) => !(fk.inferred && decisions.get(fk.name) === "rejected"))
      .map((fk) => (fk.inferred && decisions.get(fk.name) === "accepted" ? { ...fk, confidence: 1 } : fk));
  }
  return copy;
}

/** Synthetic schema for the performance tests: `tables` tables with `fks` foreign keys, deterministic. */
export function syntheticModel(tables: number, fks: number, seed = 42): StubModel {
  let state = seed;
  const random = () => {
    state = (state * 16807) % 2147483647;
    return state / 2147483647;
  };
  const schemas = ["core", "sales", "ops", "audit"];
  const relations: StubRelation[] = [];
  for (let i = 0; i < tables; i++) {
    const columnCount = 4 + Math.floor(random() * 10);
    const columns = [{ name: "id", type: "bigint", nullable: false }];
    for (let c = 1; c < columnCount; c++) {
      columns.push({ name: `ref_${c}_id`, type: "bigint", nullable: random() < 0.3 });
    }
    relations.push({
      schema: schemas[i % schemas.length] as string,
      name: `t${String(i).padStart(4, "0")}`,
      kind: "table",
      rowEstimate: Math.floor(random() * 5_000_000),
      pages: 100,
      columns,
      primaryKey: ["id"],
      foreignKeys: [],
    });
  }
  // Each table references earlier tables (a DAG with some hubs), like real schemas.
  let made = 0;
  for (let round = 0; made < fks && round < 64; round++) {
    for (let i = 1; i < tables && made < fks; i++) {
      const child = relations[i] as StubRelation;
      const slot = (child.foreignKeys?.length ?? 0) + 1;
      if (slot >= (child.columns?.length ?? 0)) continue;
      const parentIndex = random() < 0.2 ? Math.floor(random() * Math.min(10, i)) : Math.floor(random() * i);
      const parent = relations[parentIndex] as StubRelation;
      child.foreignKeys?.push({
        name: `${child.name}_ref_${slot}_fkey`,
        columns: [`ref_${slot}_id`],
        ref: { schema: parent.schema, name: parent.name },
        refColumns: ["id"],
        validated: true,
      });
      made++;
    }
  }
  relations.sort((a, b) => a.schema.localeCompare(b.schema) || a.name.localeCompare(b.name));
  return {
    serverVersion: 170002,
    schemas,
    relations,
    enums: [],
    introspectedAt: "2026-10-26T09:58:00Z",
    durationMs: 812,
  };
}

export interface SavedLayout {
  key: string;
  positions: Record<string, { x: number; y: number }>;
  collapsed: string[];
  updatedAt?: string;
}

export interface SchemaStubOptions {
  /** Model for GET /api/profiles/{id}/schema; defaults to the sample fixture. */
  schema?: StubModel;
  /** Model for GET /api/sample/schema; defaults to the sample fixture. */
  sample?: StubModel;
  /** Respond to GET schema with this error envelope instead. */
  schemaError?: { status: number; code: string; message: string; detail?: string };
  namespaces?: { name: string; relationCount: number }[];
}

/** In-memory Phase 1 state that survives page reloads within one test. */
export interface SchemaStubState {
  decisions: Map<string, string>;
  decisionRequests: { key: string; status: string }[];
  layouts: Map<string, SavedLayout>;
  layoutPuts: SavedLayout[];
  schemaRequests: number;
}

export function json(route: Route, status: number, body?: unknown) {
  return route.fulfill({
    status,
    contentType: "application/json",
    body: body === undefined ? "" : JSON.stringify(body),
  });
}

/**
 * Handles the Phase 1 routes from docs/API.md. Returns true when it answered.
 * Layout keys are stored per profile, as the agent does.
 */
export async function handleSchemaRoute(
  route: Route,
  pathname: string,
  method: string,
  options: SchemaStubOptions,
  state: SchemaStubState,
): Promise<boolean> {
  const request = route.request();
  if (pathname === "/api/sample/schema" && method === "GET") {
    await json(route, 200, options.sample ?? sampleModel());
    return true;
  }
  const m = pathname.match(/^\/api\/profiles\/([^/]+)\/(schema|namespaces|inferred-fks|layout)$/);
  if (!m) return false;
  const [, profileId, what] = m;

  if (what === "schema" && method === "GET") {
    state.schemaRequests++;
    if (options.schemaError) {
      const { status, ...error } = options.schemaError;
      await json(route, status, { error });
      return true;
    }
    await json(route, 200, applyDecisions(options.schema ?? sampleModel(), state.decisions));
    return true;
  }
  if (what === "namespaces" && method === "GET") {
    await json(
      route,
      200,
      options.namespaces ?? [
        { name: "app", relationCount: 15 },
        { name: "billing", relationCount: 11 },
        { name: "geo", relationCount: 1 },
        { name: "shop", relationCount: 15 },
      ],
    );
    return true;
  }
  if (what === "inferred-fks" && method === "PUT") {
    const body = request.postDataJSON() as { key: string; status: string };
    state.decisionRequests.push(body);
    if (body.status === "unset") state.decisions.delete(body.key);
    else state.decisions.set(body.key, body.status);
    await json(route, 204);
    return true;
  }
  if (what === "layout" && method === "GET") {
    const key = new URL(request.url()).searchParams.get("key") ?? "";
    const saved = state.layouts.get(`${profileId}\n${key}`);
    if (!saved) await json(route, 404, { error: { code: "not_found", message: "no layout saved under this key" } });
    else await json(route, 200, saved);
    return true;
  }
  if (what === "layout" && method === "PUT") {
    const body = request.postDataJSON() as SavedLayout;
    const saved = { ...body, updatedAt: "2026-10-26T10:00:00Z" };
    state.layoutPuts.push(saved);
    state.layouts.set(`${profileId}\n${body.key}`, saved);
    await json(route, 204);
    return true;
  }
  return false;
}
