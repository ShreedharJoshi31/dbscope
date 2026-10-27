import { useEffect } from "react";
import { create } from "zustand";
import { safeLocalStorage } from "@/shared/lib/storage";

/** The ERD is the permanent first centre tab; SQL tabs follow it. */
export const ERD_TAB = "erd";

export interface SqlTab {
  id: string;
  title: string;
  sql: string;
}

interface Persisted {
  tabs: SqlTab[];
  next: number;
  active: string;
}

/** Asks the tab's editor to focus and select a range (from the Messages tab). */
export interface EditorReveal {
  tabId: string;
  from: number;
  to: number;
  nonce: number;
}

interface SqlTabsState extends Persisted {
  reveal: EditorReveal | null;
  revealInEditor: (tabId: string, from?: number, to?: number) => void;
  /** Profile id, "sample", or null before the workbench knows. Tabs are kept per scope. */
  scope: string | null;
  setScope: (scope: string | null) => void;
  /** Opens a new tab ("query-N.sql"), optionally with SQL, and makes it active. Returns its id. */
  openTab: (sql?: string) => string;
  closeTab: (id: string) => void;
  setSql: (id: string, sql: string) => void;
  setActive: (id: string) => void;
}

const EMPTY: Persisted = { tabs: [], next: 1, active: ERD_TAB };

const storageKey = (scope: string) => `dbscope.sqlTabs.${scope}`;

function isTab(value: unknown): value is SqlTab {
  if (typeof value !== "object" || value === null) return false;
  const t = value as Record<string, unknown>;
  return typeof t.id === "string" && typeof t.title === "string" && typeof t.sql === "string";
}

/** Reads a scope's tabs; anything malformed falls back to no tabs. */
export function readTabs(scope: string): Persisted {
  try {
    const raw: unknown = JSON.parse(safeLocalStorage.getItem(storageKey(scope)) ?? "null");
    if (typeof raw !== "object" || raw === null) return EMPTY;
    const r = raw as Record<string, unknown>;
    const tabs = Array.isArray(r.tabs) ? r.tabs.filter(isTab) : [];
    const next = typeof r.next === "number" && r.next > tabs.length ? Math.floor(r.next) : tabs.length + 1;
    const active = typeof r.active === "string" && tabs.some((t) => t.id === r.active) ? r.active : ERD_TAB;
    return { tabs, next, active };
  } catch {
    return EMPTY;
  }
}

function write(scope: string | null, state: Persisted): void {
  if (!scope) return;
  const { tabs, next, active } = state;
  safeLocalStorage.setItem(storageKey(scope), JSON.stringify({ tabs, next, active }));
}

let revealNonce = 0;

export const useSqlTabs = create<SqlTabsState>((set, get) => {
  const commit = (patch: Partial<Persisted>) => {
    set(patch);
    const s = get();
    write(s.scope, s);
  };

  return {
    ...EMPTY,
    scope: null,
    reveal: null,
    revealInEditor: (tabId, from = 0, to = from) => {
      if (!get().tabs.some((t) => t.id === tabId)) return;
      revealNonce += 1;
      commit({ active: tabId });
      set({ reveal: { tabId, from, to, nonce: revealNonce } });
    },
    setScope: (scope) => {
      if (get().scope === scope) return;
      set({ scope, ...(scope ? readTabs(scope) : EMPTY) });
    },
    openTab: (sql = "") => {
      const { tabs, next } = get();
      const id = `q${next}`;
      commit({ tabs: [...tabs, { id, title: `query-${next}.sql`, sql }], next: next + 1, active: id });
      return id;
    },
    closeTab: (id) => {
      const { tabs, active } = get();
      const index = tabs.findIndex((t) => t.id === id);
      if (index === -1) return;
      const rest = tabs.filter((t) => t.id !== id);
      const neighbour = rest[Math.min(index, rest.length - 1)]?.id ?? ERD_TAB;
      commit({ tabs: rest, active: active === id ? neighbour : active });
    },
    setSql: (id, sql) => {
      const { tabs } = get();
      if (tabs.find((t) => t.id === id)?.sql === sql) return;
      commit({ tabs: tabs.map((t) => (t.id === id ? { ...t, sql } : t)) });
    },
    setActive: (id) => {
      if (id !== ERD_TAB && !get().tabs.some((t) => t.id === id)) return;
      commit({ active: id });
    },
  };
});

/** Switches the tab set when the workbench changes connection (or enters the sample). */
export function useSqlTabsScope(scope: string | null): void {
  useEffect(() => {
    useSqlTabs.getState().setScope(scope);
  }, [scope]);
}

export function activeSqlTab(): SqlTab | undefined {
  const { tabs, active } = useSqlTabs.getState();
  return tabs.find((t) => t.id === active);
}

/** Opens SQL in a new tab (from history, the assistant or the palette). */
export function openSqlTab(sql = ""): string {
  return useSqlTabs.getState().openTab(sql);
}
