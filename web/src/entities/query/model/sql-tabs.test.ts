import { afterEach, describe, expect, it, vi } from "vitest";
import { ERD_TAB, openSqlTab, readTabs, useSqlTabs } from "@/entities/query/model/sql-tabs";

const state = () => useSqlTabs.getState();

afterEach(() => useSqlTabs.setState({ scope: null, tabs: [], docTabs: [], next: 1, active: ERD_TAB, reveal: null }));

describe("SQL tabs", () => {
  it("numbers new tabs, persists their text per connection and restores them", () => {
    state().setScope("p1");
    const a = openSqlTab("SELECT 1");
    const b = openSqlTab();
    expect(state().tabs.map((t) => t.title)).toEqual(["query-1.sql", "query-2.sql"]);
    expect(state().active).toBe(b);
    state().setSql(b, "SELECT 2");
    state().setActive(a);

    state().setScope("p2");
    expect(state().tabs).toEqual([]);
    openSqlTab("SELECT 'other'");

    state().setScope("p1");
    expect(state().tabs.map((t) => t.sql)).toEqual(["SELECT 1", "SELECT 2"]);
    expect(state().active).toBe(a);
    expect(readTabs("p2").tabs[0]?.sql).toBe("SELECT 'other'");
  });

  it("closing the active tab activates its neighbour, then the ERD; numbering keeps counting", () => {
    state().setScope("p1");
    const a = openSqlTab();
    const b = openSqlTab();
    state().closeTab(b);
    expect(state().active).toBe(a);
    state().closeTab(a);
    expect(state().active).toBe(ERD_TAB);
    openSqlTab();
    expect(state().tabs[0]?.title).toBe("query-3.sql");
  });

  it("survives unavailable or corrupted storage", () => {
    window.localStorage.setItem("dbscope.sqlTabs.bad", "{not json");
    expect(readTabs("bad").tabs).toEqual([]);
    window.localStorage.setItem(
      "dbscope.sqlTabs.mixed",
      JSON.stringify({ tabs: [{ id: "q1", title: "t", sql: "x" }, { id: 3 }], active: "zz" }),
    );
    expect(readTabs("mixed")).toEqual({ tabs: [{ id: "q1", title: "t", sql: "x" }], next: 2, active: ERD_TAB });

    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("quota", "QuotaExceededError");
    });
    state().setScope("p9");
    expect(() => openSqlTab("SELECT 1")).not.toThrow();
    expect(state().tabs).toHaveLength(1);
  });

  it("reveal requests target an existing tab and activate it", () => {
    state().setScope("p1");
    const a = openSqlTab("SELECT 1");
    state().setActive(ERD_TAB);
    state().revealInEditor(a, 7, 8);
    expect(state().reveal).toMatchObject({ tabId: a, from: 7, to: 8 });
    expect(state().active).toBe(a);
    state().revealInEditor("nope", 1);
    expect(state().reveal?.tabId).toBe(a);
  });

  it("migration tabs live in memory per scope, follow the SQL tabs and are not persisted", () => {
    state().setScope("p1");
    const q = openSqlTab();
    state().openDocTab({ id: "m1", title: "0042_orders.sql", kind: "migration" });
    expect(state().active).toBe("m1");
    expect(readTabs("p1").active).toBe(ERD_TAB); // a doc tab id is not restored after a reload
    state().setScope("p2");
    expect(state().docTabs.filter((t) => t.scope === "p2")).toEqual([]);
    state().setActive("m1"); // belongs to p1
    expect(state().active).not.toBe("m1");
    state().setScope("p1");
    state().setActive("m1");
    expect(state().active).toBe("m1");
    state().closeTab("m1");
    expect(state().active).toBe(q);
    expect(state().docTabs).toEqual([]);
  });
});
