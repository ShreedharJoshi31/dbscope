import { isMacPlatform } from "@/shared/lib/platform";

/** `mod` is Cmd on macOS and Ctrl elsewhere; `alt` is Option on macOS. */
export interface KeyCombo {
  key: string;
  mod?: boolean;
  shift?: boolean;
  alt?: boolean;
}

export type ShortcutId =
  | "commandPalette"
  | "shortcutSheet"
  | "toggleExplorer"
  | "toggleDock"
  | "toggleAssistant"
  | "nextRegion"
  | "previousRegion"
  | "canvasSearch"
  | "canvasFocusMode"
  | "canvasMoveSelection"
  | "canvasOpenDetails"
  | "canvasFollowFk"
  | "canvasClear"
  | "runQuery"
  | "cancelQuery"
  | "explainQuery"
  | "newSqlTab"
  | "focusAssistant";

export type ShortcutGroup = "General" | "Panels" | "Navigation" | "Query" | "Assistant" | "Canvas";

export const SHORTCUT_GROUPS: readonly ShortcutGroup[] = [
  "General",
  "Panels",
  "Navigation",
  "Query",
  "Assistant",
  "Canvas",
];

export interface ShortcutDefinition {
  combo: KeyCombo;
  label: string;
  group: ShortcutGroup;
  /** Key caps to show instead of the combo, for shortcuts that are a family of keys (the arrows). */
  keys?: string[];
}

export const SHORTCUTS: Record<ShortcutId, ShortcutDefinition> = {
  commandPalette: { combo: { key: "k", mod: true }, label: "Search or run a command", group: "General" },
  shortcutSheet: { combo: { key: "?" }, label: "Show keyboard shortcuts", group: "General" },
  toggleExplorer: { combo: { key: "b", mod: true }, label: "Toggle schema explorer", group: "Panels" },
  toggleDock: { combo: { key: "j", mod: true }, label: "Toggle results dock", group: "Panels" },
  toggleAssistant: { combo: { key: "i", mod: true }, label: "Toggle assistant", group: "Panels" },
  nextRegion: { combo: { key: "F6" }, label: "Focus next region", group: "Navigation" },
  previousRegion: { combo: { key: "F6", shift: true }, label: "Focus previous region", group: "Navigation" },
  canvasSearch: { combo: { key: "/" }, label: "Search tables and columns", group: "Canvas" },
  canvasFocusMode: { combo: { key: "f" }, label: "Focus on the selected table and its neighbours", group: "Canvas" },
  canvasMoveSelection: {
    combo: { key: "ArrowRight" },
    label: "Move the selection to the nearest table (canvas focused)",
    group: "Canvas",
    keys: ["↑", "↓", "←", "→"],
  },
  canvasOpenDetails: { combo: { key: "Enter" }, label: "Open details of the selected table", group: "Canvas" },
  canvasFollowFk: {
    combo: { key: "Tab" },
    label: "Follow the selected table's foreign key (Shift+Tab goes back)",
    group: "Canvas",
  },
  canvasClear: {
    combo: { key: "Escape" },
    label: "Clear search, focus mode, selection or the query overlay",
    group: "Canvas",
  },
  runQuery: { combo: { key: "Enter", mod: true }, label: "Run the query in the SQL tab", group: "Query" },
  cancelQuery: { combo: { key: ".", mod: true }, label: "Cancel the running query", group: "Query" },
  // Not Ctrl+Shift+E: Firefox opens its network panel on it.
  explainQuery: {
    combo: { key: "Enter", mod: true, shift: true },
    label: "Explain the query (plan only, nothing runs)",
    group: "Query",
  },
  // Cmd/Ctrl+N and +T are reserved by browsers; Alt keeps it free.
  newSqlTab: { combo: { key: "n", mod: true, alt: true }, label: "New SQL tab", group: "Query" },
  focusAssistant: {
    combo: { key: "a", mod: true, alt: true },
    label: "Ask the assistant (focus the question box)",
    group: "Assistant",
  },
};

const KEY_NAMES: Record<string, string> = {
  Escape: "Esc",
  ArrowUp: "↑",
  ArrowDown: "↓",
  ArrowLeft: "←",
  ArrowRight: "→",
};

function keyLabel(key: string): string {
  return key.length === 1 ? key.toUpperCase() : (KEY_NAMES[key] ?? key);
}

/** Visible key caps, e.g. ["⌘", "K"] on macOS or ["Ctrl", "K"] elsewhere. */
export function comboKeys(combo: KeyCombo, mac = isMacPlatform()): string[] {
  const keys: string[] = [];
  if (combo.mod) keys.push(mac ? "⌘" : "Ctrl");
  if (combo.alt) keys.push(mac ? "⌥" : "Alt");
  if (combo.shift) keys.push(mac ? "⇧" : "Shift");
  keys.push(keyLabel(combo.key));
  return keys;
}

/** Plain-text form for tooltips and labels, e.g. "Ctrl+K" or "⌘K". */
export function formatCombo(combo: KeyCombo, mac = isMacPlatform()): string {
  return comboKeys(combo, mac).join(mac ? "" : "+");
}

/** Value for the aria-keyshortcuts attribute. */
export function ariaKeyShortcut(combo: KeyCombo, mac = isMacPlatform()): string {
  const parts: string[] = [];
  if (combo.mod) parts.push(mac ? "Meta" : "Control");
  if (combo.alt) parts.push("Alt");
  if (combo.shift) parts.push("Shift");
  parts.push(combo.key.length === 1 ? combo.key.toUpperCase() : combo.key);
  return parts.join("+");
}

export function shortcutLabel(id: ShortcutId): string {
  const { keys, combo } = SHORTCUTS[id];
  return keys ? keys.join(" ") : formatCombo(combo);
}
