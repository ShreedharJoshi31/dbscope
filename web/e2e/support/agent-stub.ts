import type { Page } from "@playwright/test";
import { handleMigrationRoute, type MigrationStubOptions, type MigrationStubState } from "./migration-stub";
import { handlePhase23Route, newPhase23State, type Phase23Options } from "./phase23-stub";
import { handleSchemaRoute, json, type SchemaStubOptions, type SchemaStubState } from "./schema-stub";

/** Mirrors the Phase 0 contract in docs/API.md, kept local so e2e does not import app code. */
export type Env = "local" | "staging" | "prod-replica";

export interface StubProfile {
  id: string;
  name: string;
  engine: "postgres" | "mysql" | "sqlite";
  path?: string;
  capabilities: { query: boolean; ask: boolean; migrations: boolean; workload: boolean };
  host: string;
  port: number;
  database: string;
  user: string;
  sslMode: "disable" | "prefer" | "require" | "verify-ca" | "verify-full";
  env: Env;
  schemas: string[];
  summaryMode: "off" | "profile" | "rows";
  maxRowsToLLM: number;
  statementTimeoutMs: number;
  maxCost?: number;
  hasPassword: boolean;
  createdAt: string;
  updatedAt: string;
}

export const E2E_TOKEN = "e2e-session-token";

const NOW = "2026-10-26T10:00:00Z";

function capabilitiesOf(engine: StubProfile["engine"] | undefined): StubProfile["capabilities"] {
  if (engine === "sqlite") return { query: true, ask: false, migrations: true, workload: false };
  if (engine === "mysql") return { query: true, ask: false, migrations: false, workload: false };
  return { query: true, ask: true, migrations: true, workload: true };
}

export function stubProfile(overrides: Partial<StubProfile> = {}): StubProfile {
  return {
    id: "01JB00000000000000000000AA",
    name: "orders-dev",
    engine: "postgres",
    capabilities: { query: true, ask: true, migrations: true, workload: true },
    host: "localhost",
    port: 5432,
    database: "orders",
    user: "app",
    sslMode: "prefer",
    env: "local",
    schemas: [],
    summaryMode: "profile",
    maxRowsToLLM: 50,
    statementTimeoutMs: 30000,
    hasPassword: true,
    createdAt: NOW,
    updatedAt: NOW,
    ...overrides,
  };
}

interface StubOptions extends SchemaStubOptions, Phase23Options, MigrationStubOptions {
  profiles?: StubProfile[];
}

const HEALTHY_TEST = {
  ok: true,
  latencyMs: 3,
  serverVersion: "17.2",
  serverVersionNum: 170002,
  isReplica: false,
  defaultReadOnly: false,
  writePrivileges: [],
  pgStatStatements: true,
  warnings: [],
};

/** An in-memory dbscope agent behind page.route, so e2e runs without the Go backend. */
export async function stubAgent(page: Page, options: StubOptions = {}) {
  const profiles = [...(options.profiles ?? [])];
  let nextId = 1;
  const schema: SchemaStubState = {
    decisions: new Map(),
    decisionRequests: [],
    layouts: new Map(),
    layoutPuts: [],
    schemaRequests: 0,
  };
  const phase23 = newPhase23State(options);
  const migrations: MigrationStubState = { requests: [] };

  await page.route("**/api/**", async (route) => {
    const request = route.request();
    if (request.headers()["x-dbscope-token"] !== E2E_TOKEN) {
      return json(route, 401, { error: { code: "unauthorized", message: "missing or invalid token" } });
    }
    const { pathname } = new URL(request.url());
    const method = request.method();
    const byId = pathname.match(/^\/api\/profiles\/([^/]+)(\/test)?$/);

    if (await handleSchemaRoute(route, pathname, method, options, schema)) return;
    if (await handlePhase23Route(route, pathname, method, options, phase23)) return;
    if (await handleMigrationRoute(route, pathname, method, options, migrations)) return;

    if (pathname === "/api/health") return json(route, 200, { version: "dev", os: "darwin", arch: "arm64" });
    if (pathname === "/api/secrets/status")
      return json(route, 200, { backend: "keyring", locked: false, initialized: true });
    if (pathname === "/api/connection-url/parse" && method === "POST") {
      const { url } = request.postDataJSON() as { url: string };
      const parsed = new URL(url);
      return json(route, 200, {
        host: parsed.hostname,
        port: Number(parsed.port || 5432),
        database: parsed.pathname.slice(1),
        user: decodeURIComponent(parsed.username),
        ...(parsed.password ? { password: decodeURIComponent(parsed.password) } : {}),
        sslMode: parsed.searchParams.get("sslmode") ?? "prefer",
      });
    }
    if (pathname === "/api/profiles/test" && method === "POST") return json(route, 200, HEALTHY_TEST);
    if (pathname === "/api/profiles" && method === "GET") {
      return json(
        route,
        200,
        [...profiles].sort((a, b) => a.name.localeCompare(b.name)),
      );
    }
    if (pathname === "/api/profiles" && method === "POST") {
      const { password, ...input } = request.postDataJSON() as Omit<StubProfile, "id"> & { password?: string };
      const created = stubProfile({
        ...input,
        id: `01JB0000000000000000000N${nextId++}`,
        hasPassword: Boolean(password),
        capabilities: capabilitiesOf(input.engine),
      });
      profiles.push(created);
      return json(route, 201, created);
    }
    if (byId?.[2] && method === "POST") return json(route, 200, HEALTHY_TEST);
    if (byId && !byId[2] && method === "PUT") {
      const body: Record<string, unknown> = request.postDataJSON();
      // The agent rejects unknown fields; capabilities are derived, never sent.
      if ("capabilities" in body) {
        return json(route, 400, { error: { code: "validation", message: "unknown field capabilities" } });
      }
      const { password, ...input } = body as Omit<StubProfile, "id"> & { password?: string };
      const index = profiles.findIndex((p) => p.id === byId[1]);
      const current = profiles[index];
      if (!current) return json(route, 404, { error: { code: "not_found", message: "no such profile" } });
      const updated = {
        ...current,
        ...input,
        hasPassword: password === undefined ? current.hasPassword : Boolean(password),
      };
      profiles[index] = updated;
      return json(route, 200, updated);
    }
    if (byId && method === "DELETE") {
      const index = profiles.findIndex((p) => p.id === byId[1]);
      if (index >= 0) profiles.splice(index, 1);
      return json(route, 204);
    }
    return json(route, 404, { error: { code: "not_found", message: `no stub for ${method} ${pathname}` } });
  });

  return { profiles, schema, phase23, migrations };
}

/** Opens the UI the way `dbscope ui` does: with the session token in the fragment. */
export async function openApp(page: Page) {
  await page.goto(`/#t=${E2E_TOKEN}`);
}
