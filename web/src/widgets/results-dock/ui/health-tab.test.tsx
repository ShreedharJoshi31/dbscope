import { screen, within } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { describe, expect, it } from "vitest";
import { makeProfile, renderWithProviders } from "@/test/render";
import { server } from "@/test/server";
import { HealthTab } from "@/widgets/results-dock/ui/health-tab";

const capabilities = { query: true, ask: true, migrations: true, workload: true, health: true };

describe("Health tab", () => {
  it("lists checks worst first with their values, and the fix only for checks that are not fine", async () => {
    server.use(
      http.get("/api/profiles", () => HttpResponse.json([makeProfile({ id: "p1", capabilities })])),
      http.get("/api/profiles/p1/health", () =>
        HttpResponse.json({
          engine: "postgres",
          server: "PostgreSQL 17.2",
          tookMs: 12,
          checks: [
            { id: "pg.connections", title: "Connections", status: "ok", value: "3 of 97", fix: "Use a pooler" },
            {
              id: "pg.inactive_slots",
              title: "Inactive replication slots",
              status: "danger",
              value: "1, holding up to 12.0 GiB",
              fix: "Drop it",
            },
            {
              id: "pg.replication_lag",
              title: "Replication lag",
              status: "unknown",
              value: "not readable",
              detail: "GRANT pg_read_all_stats",
            },
            { id: "pg.invalid_indexes", title: "Invalid indexes", status: "warn", value: "1" },
          ],
        }),
      ),
    );
    renderWithProviders(<HealthTab />);
    const rows = await screen.findAllByTestId("health-check");
    expect(rows.map((r) => r.dataset.status)).toEqual(["danger", "warn", "unknown", "ok"]);
    const [worst, , , fine] = rows as [HTMLElement, HTMLElement, HTMLElement, HTMLElement];
    expect(within(worst).getByText("What to do: Drop it")).toBeInTheDocument();
    expect(within(fine).queryByText(/What to do/)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Health summary")).toHaveTextContent(
      "PostgreSQL 17.2 · 1 to act on, 1 to watch, 1 not readable, 1 fine",
    );
  });

  it("explains when the engine has no health checks", async () => {
    server.use(
      http.get("/api/profiles", () =>
        HttpResponse.json([makeProfile({ id: "p1", capabilities: { ...capabilities, health: false } })]),
      ),
    );
    renderWithProviders(<HealthTab />);
    expect(await screen.findByText("No health checks")).toBeInTheDocument();
  });
});
