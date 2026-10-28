import { beforeEach, describe, expect, it } from "vitest";
import { type RunMeta, useRunStore } from "@/entities/query/model/run-store";

const meta = (id: number): RunMeta => ({
  id,
  kind: "query",
  analyze: false,
  sql: "SELECT 1",
  tabId: null,
  source: "editor",
  startedAt: 0,
});

const output = (m: RunMeta) => ({
  meta: m,
  result: null,
  executedSql: "SELECT 1",
  plan: { nodeType: "Result", flags: [], children: [] } as never,
  overlay: { relations: {}, joins: [] } as never,
  warnings: ["w"],
});

describe("run store reset", () => {
  beforeEach(() => useRunStore.getState().reset());

  it("forgets the previous connection's output and messages", () => {
    const s = useRunStore.getState();
    s.start(meta(1));
    s.succeed(output(meta(1)));
    expect(useRunStore.getState().output).not.toBeNull();
    expect(useRunStore.getState().messages).toHaveLength(1);
    useRunStore.getState().reset();
    const after = useRunStore.getState();
    expect(after.output).toBeNull();
    expect(after.messages).toEqual([]);
    expect(after.last).toBeNull();
  });

  it("ignores a run that was still in flight when the connection changed", () => {
    useRunStore.getState().start(meta(2));
    useRunStore.getState().reset();
    useRunStore.getState().succeed(output(meta(2)));
    expect(useRunStore.getState().output).toBeNull();
  });
});
