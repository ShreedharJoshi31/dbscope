import { describe, expect, it } from "vitest";
import {
  clamp,
  formatAsOf,
  formatSpan,
  fromAsOf,
  MAX_PICKED_TABLES,
  oldestLabel,
  outOfRange,
  pickTables,
  toAsOf,
} from "@/features/time-travel/model/as-of";
import type { Model, Relation, TimeTravel, TimeTravelBounds } from "@/shared/api";

function relation(schema: string, name: string, timeTravel?: TimeTravel): Relation {
  return { schema, name, kind: "table", rowEstimate: 1, pages: 1, columns: [], ...(timeTravel ? { timeTravel } : {}) };
}

function model(relations: Relation[]): Model {
  return {
    serverVersion: 1,
    schemas: ["TT"],
    relations,
    enums: [],
    introspectedAt: "2026-11-15T00:00:00Z",
    durationMs: 1,
  };
}

const FLASHBACK: TimeTravel = { kind: "flashback", available: true };

describe("toAsOf and fromAsOf", () => {
  it("reads a datetime-local value as wall time in the zone and gives UTC with Z", () => {
    expect(toAsOf("2026-11-15T10:00:30", "Asia/Kolkata")).toBe("2026-11-15T04:30:30Z");
    expect(toAsOf("2026-11-15T10:00", "Asia/Kolkata")).toBe("2026-11-15T04:30:00Z");
    expect(toAsOf("2026-11-15T10:00:00", "UTC")).toBe("2026-11-15T10:00:00Z");
    // Across midnight and a date line.
    expect(toAsOf("2026-01-01T03:00:00", "America/Los_Angeles")).toBe("2026-01-01T11:00:00Z");
    expect(toAsOf("2026-01-01T03:00:00", "Pacific/Auckland")).toBe("2025-12-31T14:00:00Z");
  });

  it("uses the offset in force on that date, not today's", () => {
    // New York: EDT (UTC-4) in July, EST (UTC-5) in January.
    expect(toAsOf("2026-07-01T12:00:00", "America/New_York")).toBe("2026-07-01T16:00:00Z");
    expect(toAsOf("2026-01-15T12:00:00", "America/New_York")).toBe("2026-01-15T17:00:00Z");
    // Just after the autumn change (2026-11-01 02:00 EDT becomes 01:00 EST).
    expect(toAsOf("2026-11-01T03:00:00", "America/New_York")).toBe("2026-11-01T08:00:00Z");
  });

  it("rejects values that are not a real date and time, and unknown zones", () => {
    expect(toAsOf("", "UTC")).toBeNull();
    expect(toAsOf("2026-02-30T10:00:00", "UTC")).toBeNull();
    expect(toAsOf("2026-11-15T24:00:00", "UTC")).toBeNull();
    expect(toAsOf("2026-11-15 10:00:00", "UTC")).toBeNull();
    expect(toAsOf("2026-11-15T10:00:00", "Not/AZone")).toBeNull();
  });

  it("goes back to a datetime-local value in the zone, rounding fractions as asked", () => {
    expect(fromAsOf("2026-11-15T04:30:30Z", "Asia/Kolkata")).toBe("2026-11-15T10:00:30");
    expect(fromAsOf("2026-11-15T04:30:30.250+00:00", "Asia/Kolkata")).toBe("2026-11-15T10:00:30");
    expect(fromAsOf("2026-11-15T04:30:30.250Z", "Asia/Kolkata", "ceil")).toBe("2026-11-15T10:00:31");
    expect(fromAsOf("2026-11-15T10:00:00+05:30", "UTC")).toBe("2026-11-15T04:30:00");
    expect(fromAsOf("not a time", "UTC")).toBe("");
    expect(toAsOf(fromAsOf("2026-07-01T16:00:00Z", "America/New_York"), "America/New_York")).toBe(
      "2026-07-01T16:00:00Z",
    );
  });

  it("formats an instant for display with the zone's offset", () => {
    expect(formatAsOf("2026-11-15T04:30:00Z", "Asia/Kolkata")).toBe("2026-11-15 10:00:00 GMT+5:30");
    expect(formatAsOf("2026-11-15T04:30:00Z", "UTC")).toBe("2026-11-15 04:30:00 GMT");
  });
});

describe("pickTables", () => {
  const m = model([
    relation("TT", "ACCT", FLASHBACK),
    relation("TT", "ACCT_LOG", FLASHBACK),
    relation("TT", "PLAIN"),
    relation("APP", "ORDERS", { kind: "flashback", available: false, note: "Needs FLASHBACK" }),
  ]);

  it("matches versioned names as whole identifiers, case-insensitively, bare or qualified", () => {
    expect(pickTables("select * from acct", m)).toEqual({ tables: ["TT.ACCT"], matched: true });
    expect(pickTables('SELECT * FROM "TT"."ACCT" a JOIN app.orders o ON 1=1', m)).toEqual({
      tables: ["TT.ACCT", "APP.ORDERS"],
      matched: true,
    });
    expect(pickTables("SELECT * FROM tt.acct_log", m).tables).toEqual(["TT.ACCT_LOG"]);
    // A plain table is never picked, even when named.
    expect(pickTables("SELECT * FROM plain JOIN acct ON 1=1", m).tables).toEqual(["TT.ACCT"]);
  });

  it("falls back to every versioned relation when the SQL names none, capped", () => {
    expect(pickTables("SELECT * FROM plain", m)).toEqual({
      tables: ["TT.ACCT", "TT.ACCT_LOG", "APP.ORDERS"],
      matched: false,
    });
    expect(pickTables("SELECT 1 FROM accts", m).matched).toBe(false);
    const many = model(Array.from({ length: 80 }, (_, i) => relation("S", `T${i}`, FLASHBACK)));
    expect(pickTables("", many).tables).toHaveLength(MAX_PICKED_TABLES);
    expect(pickTables("SELECT 1", undefined)).toEqual({ tables: [], matched: false });
  });
});

describe("clamp", () => {
  const bounds: TimeTravelBounds = {
    now: "2026-11-15T10:00:00Z",
    tables: [
      {
        table: "TT.ACCT",
        kind: "flashback",
        available: true,
        oldest: "2026-11-15T09:45:00Z",
        source: "assumed_default",
      },
      {
        table: "TT.LOG",
        kind: "flashback",
        available: true,
        oldest: "2026-11-15T09:50:00.5Z",
        source: "undo_retention",
      },
      {
        table: "APP.ORDERS",
        kind: "flashback",
        available: false,
        oldest: null,
        source: "none",
        note: "Needs FLASHBACK",
      },
      { table: "APP.LATER", kind: "flashback", available: false, oldest: "2026-11-15T09:59:00Z", source: "none" },
      { table: "DBO.HIST", kind: "temporal", available: true, oldest: null, source: "none" },
    ],
  };

  it("takes the latest oldest time of the available tables as min and the server clock as max", () => {
    const range = clamp(bounds, ["TT.ACCT", "TT.LOG", "APP.ORDERS", "APP.LATER", "DBO.HIST"]);
    expect(range.min).toBe("2026-11-15T09:50:00.5Z");
    expect(range.max).toBe("2026-11-15T10:00:00Z");
    expect(range.rows.map((r) => r.table)).toEqual(["TT.ACCT", "TT.LOG", "APP.ORDERS", "APP.LATER", "DBO.HIST"]);
    expect(range.rows[2]).toEqual({
      table: "APP.ORDERS",
      kind: "flashback",
      available: false,
      oldest: null,
      source: "none",
      note: "Needs FLASHBACK",
    });
  });

  it("only uses the picked tables, and has no min when none sets a lower limit", () => {
    expect(clamp(bounds, ["TT.ACCT"]).min).toBe("2026-11-15T09:45:00Z");
    const open = clamp(bounds, ["DBO.HIST", "APP.ORDERS", "MISSING"]);
    expect(open.min).toBeNull();
    expect(open.rows.map((r) => r.table)).toEqual(["DBO.HIST", "APP.ORDERS"]);
  });

  it("says which side of the range an instant falls", () => {
    const range = clamp(bounds, ["TT.ACCT", "TT.LOG"]);
    expect(outOfRange("2026-11-15T09:50:00Z", range)).toBe("before");
    expect(outOfRange("2026-11-15T09:50:01Z", range)).toBeNull();
    expect(outOfRange("2026-11-15T10:00:00Z", range)).toBeNull();
    expect(outOfRange("2026-11-15T10:00:01Z", range)).toBe("after");
    expect(outOfRange("2000-01-01T00:00:00Z", null)).toBeNull();
  });

  it("shows an assumed undo retention as an estimate", () => {
    const [assumed, measured, missing, , open] = clamp(bounds, [
      "TT.ACCT",
      "TT.LOG",
      "APP.ORDERS",
      "APP.LATER",
      "DBO.HIST",
    ]).rows;
    if (!assumed || !measured || !missing || !open) throw new Error("rows missing");
    expect(oldestLabel(assumed, bounds.now, "UTC")).toBe("about 15 minutes, assumed: undo retention could not be read");
    expect(oldestLabel(measured, bounds.now, "Asia/Kolkata")).toBe("2026-11-15 15:20:00 GMT+5:30");
    expect(oldestLabel(missing, bounds.now, "UTC")).toBe("Not readable");
    expect(oldestLabel(open, bounds.now, "UTC")).toBe("No lower limit");
    expect(formatSpan(60_000)).toBe("1 minute");
    expect(formatSpan(3 * 3_600_000)).toBe("3 hours");
    expect(formatSpan(3 * 86_400_000)).toBe("3 days");
  });
});
