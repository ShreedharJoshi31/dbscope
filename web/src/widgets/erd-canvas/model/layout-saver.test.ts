import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createLayoutSaver, SAVE_DEBOUNCE_MS, snapshotLayout } from "@/widgets/erd-canvas/model/layout-saver";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

const layout = (x: number) => ({ key: "public", positions: { "public.users": { x, y: 0 } }, collapsed: [] });

describe("createLayoutSaver", () => {
  it("saves once, one second after the last change, with the latest snapshot", () => {
    const save = vi.fn();
    const saver = createLayoutSaver(save);
    saver.schedule(layout(1));
    vi.advanceTimersByTime(600);
    saver.schedule(layout(2));
    vi.advanceTimersByTime(600);
    saver.schedule(layout(3));
    vi.advanceTimersByTime(SAVE_DEBOUNCE_MS - 1);
    expect(save).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(save).toHaveBeenCalledTimes(1);
    expect(save).toHaveBeenCalledWith(layout(3));
    vi.advanceTimersByTime(5000);
    expect(save).toHaveBeenCalledTimes(1);
  });

  it("flush saves the pending snapshot now; cancel drops it", () => {
    const save = vi.fn();
    const saver = createLayoutSaver(save);
    saver.schedule(layout(1));
    saver.flush();
    expect(save).toHaveBeenCalledWith(layout(1));
    saver.flush();
    expect(save).toHaveBeenCalledTimes(1);
    saver.schedule(layout(2));
    saver.cancel();
    vi.advanceTimersByTime(5000);
    expect(save).toHaveBeenCalledTimes(1);
  });
});

describe("snapshotLayout", () => {
  it("rounds positions and sorts the collapsed set", () => {
    const snap = snapshotLayout(
      "app,shop",
      new Map([["shop.orders", { x: 10.6, y: -3.2 }]]),
      new Set(["shop.orders", "app.audit_log"]),
    );
    expect(snap).toEqual({
      key: "app,shop",
      positions: { "shop.orders": { x: 11, y: -3 } },
      collapsed: ["app.audit_log", "shop.orders"],
    });
  });
});
