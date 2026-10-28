import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { Providers } from "@/app/providers";
import { createQueryClient } from "@/app/query-client";
import type { Profile } from "@/shared/api";
import { bootstrapToken } from "@/shared/api";
import { safeSessionStorage } from "@/shared/lib/storage";
import { LiveRegion } from "@/shared/ui/live-region";

export const TEST_TOKEN = "test-token-0123456789abcdef";

/** Simulates opening the UI from the `dbscope ui` link. */
export function signIn(token = TEST_TOKEN): void {
  bootstrapToken({
    location: { hash: `#t=${token}`, pathname: "/", search: "" },
    history: { state: null, replaceState: () => {} },
    storage: safeSessionStorage,
  });
}

export function renderWithProviders(ui: ReactElement) {
  signIn();
  const user = userEvent.setup();
  const queryClient = createQueryClient();
  const result = render(
    <Providers queryClient={queryClient}>
      {ui}
      <LiveRegion />
    </Providers>,
  );
  return { user, queryClient, ...result };
}

export function makeProfile(overrides: Partial<Profile> = {}): Profile {
  return {
    id: "01JB0000000000000000000001",
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
    statementTimeoutMs: 30_000,
    hasPassword: true,
    createdAt: "2026-10-26T10:00:00Z",
    updatedAt: "2026-10-26T10:00:00Z",
    ...overrides,
  };
}
