import { describe, expect, it } from "vitest";
import { BEFORE_FIRST_OBSERVATION, buildTimeline, compareStep, NO_OBSERVATION } from "@/entities/schema/model/timeline";
import type { Model, SnapshotInfo } from "@/shared/api";

const NOW = Date.parse("2026-11-15T12:00:00.000Z");

const snapshot = (id: number, takenAt: string, lastSeenAt: string, schemas = ["app"]): SnapshotInfo => ({
  id,
  takenAt,
  lastSeenAt,
  relations: 1,
  schemas,
});

// As the API returns them: newest first.
const LIST: SnapshotInfo[] = [
  snapshot(7, "2026-11-14T09:00:00.000Z", "2026-11-15T11:59:00.000Z"),
  snapshot(4, "2026-11-10T08:00:00.000Z", "2026-11-12T17:30:00.000Z"),
  snapshot(2, "2026-11-01T10:00:00.123Z", "2026-11-03T10:00:00.456Z"),
];

const model = (schemas: string[], relations: unknown[]) =>
  ({ serverVersion: 0, schemas, enums: [], relations }) as unknown as Model;
const table = (schema: string, name: string, columns = [{ name: "id", type: "int", nullable: false }]) => ({
  schema,
  name,
  kind: "table",
  rowEstimate: 1,
  pages: 0,
  columns,
});

describe("buildTimeline", () => {
  it("orders steps oldest to newest", () => {
    expect(buildTimeline(LIST, NOW).steps.map((s) => s.id)).toEqual([2, 4, 7]);
    const shuffled = [LIST[1], LIST[2], LIST[0]] as SnapshotInfo[];
    expect(buildTimeline(shuffled, NOW).steps.map((s) => s.id)).toEqual([2, 4, 7]);
    expect(buildTimeline(LIST, NOW).now).toBe(NOW);
  });

  it("uses takenAt and lastSeenAt exactly for spans and gaps", () => {
    const [first, second, third] = buildTimeline(LIST, NOW).steps;
    expect([first?.takenAt, first?.lastSeenAt]).toEqual(["2026-11-01T10:00:00.123Z", "2026-11-03T10:00:00.456Z"]);
    expect(second?.gap).toEqual({
      kind: "unobserved",
      label: NO_OBSERVATION,
      from: "2026-11-03T10:00:00.456Z",
      to: "2026-11-10T08:00:00.000Z",
    });
    expect(third?.gap).toMatchObject({ from: "2026-11-12T17:30:00.000Z", to: "2026-11-14T09:00:00.000Z" });
    expect([third?.takenAt, third?.lastSeenAt]).toEqual(["2026-11-14T09:00:00.000Z", "2026-11-15T11:59:00.000Z"]);
  });

  it("labels every gap as no observation, never as no change", () => {
    const steps = buildTimeline(LIST, NOW).steps;
    expect(steps[0]?.gap).toEqual({
      kind: "before-first",
      label: BEFORE_FIRST_OBSERVATION,
      from: null,
      to: steps[0]?.takenAt,
    });
    expect(steps.slice(1).map((s) => s.gap.label)).toEqual(["no observation", "no observation"]);
    for (const s of steps) expect(s.gap.label).not.toMatch(/no change/i);
  });

  it("has one step, before the first observation, for a single snapshot", () => {
    const { steps } = buildTimeline([snapshot(1, "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z")], NOW);
    expect(steps).toHaveLength(1);
    expect(steps[0]?.gap.kind).toBe("before-first");
    expect(buildTimeline([], NOW).steps).toEqual([]);
  });
});

describe("compareStep", () => {
  it("lists added, removed and changed tables and columns against the previous step", () => {
    const previous = model(["app"], [table("app", "users"), table("app", "gone")]);
    const step = model(
      ["app"],
      [
        table("app", "users", [
          { name: "id", type: "bigint", nullable: false },
          { name: "email", type: "text", nullable: true },
        ]),
        table("app", "orders"),
      ],
    );
    const changes = compareStep(step, previous);
    expect(changes.added).toEqual(["app.orders"]);
    expect(changes.removed).toEqual(["app.gone"]);
    expect(changes.changed).toEqual(["app.users"]);
    expect(changes.notCompared).toEqual([]);
    expect(changes.columns["app.users"]).toEqual({
      id: { change: "altered", detail: "int → bigint" },
      email: { change: "added", detail: "column added (text)" },
    });
  });

  it("marks relations of a schema only one side read as not compared, never added or removed", () => {
    // Dedup means consecutive steps always differ; here only the schema list does.
    const previous = model(["app", "billing"], [table("app", "users"), table("billing", "invoices")]);
    const step = model(["app", "geo"], [table("app", "users"), table("geo", "countries")]);
    const changes = compareStep(step, previous);
    expect(changes.added).toEqual([]);
    expect(changes.removed).toEqual([]);
    expect(changes.changed).toEqual([]);
    expect(changes.notCompared).toEqual(["billing.invoices", "geo.countries"]);
    expect(changes.notComparedSchemas).toEqual(["billing", "geo"]);
  });
});
