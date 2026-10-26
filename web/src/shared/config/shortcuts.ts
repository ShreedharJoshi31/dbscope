import { isMacPlatform } from "@/shared/lib/platform";

/** `mod` is Cmd on macOS and Ctrl elsewhere. */
export interface KeyCombo {
  key: string;
  mod?: boolean;
  shift?: boolean;
}

export type ShortcutId =
  | "commandPalette"
  | "shortcutSheet"
  | "toggleExplorer"
  | "toggleDock"
  | "toggleAssistant"
  | "nextRegion"
  | "previousRegion";

export interface ShortcutDefinition {
  combo: KeyCombo;
  label: string;
  group: "General" | "Panels" | "Navigation";
}

export const SHORTCUTS: Record<ShortcutId, ShortcutDefinition> = {
  commandPalette: { combo: { key: "k", mod: true }, label: "Search or run a command", group: "General" },
  shortcutSheet: { combo: { key: "?" }, label: "Show keyboard shortcuts", group: "General" },
  toggleExplorer: { combo: { key: "b", mod: true }, label: "Toggle schema explorer", group: "Panels" },
  toggleDock: { combo: { key: "j", mod: true }, label: "Toggle results dock", group: "Panels" },
  toggleAssistant: { combo: { key: "i", mod: true }, label: "Toggle assistant", group: "Panels" },
  nextRegion: { combo: { key: "F6" }, label: "Focus next region", group: "Navigation" },
  previousRegion: { combo: { key: "F6", shift: true }, label: "Focus previous region", group: "Navigation" },
};

function keyLabel(key: string): string {
  return key.length === 1 ? key.toUpperCase() : key;
}

/** Visible key caps, e.g. ["⌘", "K"] on macOS or ["Ctrl", "K"] elsewhere. */
export function comboKeys(combo: KeyCombo, mac = isMacPlatform()): string[] {
  const keys: string[] = [];
  if (combo.mod) keys.push(mac ? "⌘" : "Ctrl");
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
  if (combo.shift) parts.push("Shift");
  parts.push(keyLabel(combo.key));
  return parts.join("+");
}

export function shortcutLabel(id: ShortcutId): string {
  return formatCombo(SHORTCUTS[id].combo);
}
