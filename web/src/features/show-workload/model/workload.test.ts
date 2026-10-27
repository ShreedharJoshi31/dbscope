import { describe, expect, it } from "vitest";
import { formatMs, workloadEntries, workloadSummary } from "@/features/show-workload/model/workload";
import type { WorkloadSnapshot, WorkloadTable } from "@/shared/api";

const table = (over: Partial<WorkloadTable>): WorkloadTable => ({
  seqScans: 0,
  idxScans: 0,
  rowsWritten: 0,
  liveRows: 0,
  queryCalls: 0,
  queryTimeMs: 0,
  ...over,
});

function snapshot(over: Partial<WorkloadSnapshot["statements"]> = {}): WorkloadSnapshot {
  return {
    generatedAt: "2026-10-28T10:00:00Z",
    inRecovery: false,
    statements: {
      status: "ok",
      since: "2026-10-01T00:00:00Z",
      dealloc: 0,
      totalCalls: 1000,
      totalTimeMs: 10_000,
      hiddenCalls: 0,
      unattributedCalls: 100,
      unreadCalls: 0,
      read: 50,
      ...over,
    },
    tables: {
      "public.orders": table({
        queryCalls: 900,
        queryTimeMs: 8000,
        seqScans: 3,
        idxScans: 400,
        columns: { total: { queries: 2, calls: 600 }, id: { queries: 5, calls: 900 } },
        topQueries: [
          { queryId: "1", text: "SELECT total\n  FROM orders WHERE id = $1", calls: 900, totalMs: 8000, meanMs: 8.9 },
        ],
      }),
      "public.customers": table({ queryCalls: 9, queryTimeMs: 100, seqScans: 1 }),
      "public.idle": table({}),
    },
  };
}

describe("workloadEntries", () => {
  it("labels each table by the chosen metric on a log scale and skips idle tables", () => {
    const e = workloadEntries(snapshot(), "time");
    expect(Object.keys(e).sort()).toEqual(["public.customers", "public.orders"]);
    expect(e["public.orders"]).toMatchObject({ tone: "info", label: "80% of query time", intensity: 1 });
    expect(e["public.customers"]?.label).toBe("1% of query time");
    const tiny = workloadEntries(
      { ...snapshot(), tables: { ...snapshot().tables, "public.t": table({ queryTimeMs: 4 }) } },
      "time",
    );
    expect(tiny["public.t"]?.label).toBe("<1% of query time");
    // log1p(100) / log1p(8000): about half, not 1/80.
    expect(e["public.customers"]?.intensity).toBeCloseTo(0.51, 2);
    expect(e["public.orders"]?.details).toContain("Most used columns: id, total");
    expect(e["public.orders"]?.details?.at(-1)).toBe(
      "Slowest in total: SELECT total FROM orders WHERE id = $1 (mean 9 ms)",
    );

    expect(workloadEntries(snapshot(), "reads")["public.orders"]?.label).toBe("403 scans");
    expect(workloadEntries(snapshot(), "writes")).toEqual({});
  });
});

describe("workloadSummary", () => {
  const now = Date.parse("2026-10-28T10:00:00Z");
  it("says what the totals under each metric cover and what is missing", () => {
    expect(workloadSummary(snapshot(), "time", now)).toMatch(/^Totals since .*· 10% of calls not tied to a table\.$/);
    expect(workloadSummary(snapshot({ since: "2026-10-28T02:00:00Z" }), "calls", now)).toContain(
      "less than a day of data",
    );
    // Scans and writes come from the table counters: their own reset time, and statement gaps do not apply.
    const tables = workloadSummary(
      { ...snapshot({ since: "2026-10-28T02:00:00Z" }), tableStatsReset: undefined },
      "reads",
      now,
    );
    expect(tables).toBe("Totals since the statistics were last reset (no time recorded).");
    expect(workloadSummary(snapshot({ unreadCalls: 200 }), "time", now)).toContain(
      "30% of calls not tied to a table · only the 50 statements with the most time were read",
    );
    expect(workloadSummary(snapshot({ status: "not_loaded" }), "reads", now)).toContain(
      "not in shared_preload_libraries",
    );
    expect(workloadSummary({ ...snapshot(), inRecovery: true }, "reads", now)).toContain("a replica");
  });

  it("formats durations", () => {
    expect([formatMs(0.5), formatMs(12.4), formatMs(2500), formatMs(90_000)]).toEqual([
      "0.50 ms",
      "12 ms",
      "2.5 s",
      "1.5 min",
    ]);
  });
});
