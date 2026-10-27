import { describe, expect, it } from "vitest";
import { comboKeys, formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { diffWords, hasChanges } from "@/shared/lib/diff";
import { codeMirrorKey, matchesCombo } from "@/shared/lib/hotkeys";

function key(init: KeyboardEventInit & { altGraph?: boolean }) {
  const event = new KeyboardEvent("keydown", init);
  // Real browsers report AltGraph only for the AltGr key; set it explicitly instead of relying on the DOM shim.
  Object.defineProperty(event, "getModifierState", {
    value: (k: string) => Boolean(init.altGraph) && k === "AltGraph",
  });
  return event;
}

describe("shortcuts with Alt", () => {
  const newTab = SHORTCUTS.newSqlTab.combo;

  it("matches Cmd+Option+N on macOS by physical key, since Option changes event.key", () => {
    expect(matchesCombo(key({ key: "˜", code: "KeyN", metaKey: true, altKey: true }), newTab, true)).toBe(true);
    expect(matchesCombo(key({ key: "n", code: "KeyN", metaKey: true }), newTab, true)).toBe(false);
  });

  it("matches Ctrl+Alt+N elsewhere, but not AltGr typing", () => {
    expect(matchesCombo(key({ key: "n", code: "KeyN", ctrlKey: true, altKey: true }), newTab, false)).toBe(true);
    expect(
      matchesCombo(key({ key: "ń", code: "KeyN", ctrlKey: true, altKey: true, altGraph: true }), newTab, false),
    ).toBe(false);
  });

  it("keeps Alt out of combos that do not ask for it", () => {
    expect(matchesCombo(key({ key: "Enter", ctrlKey: true, altKey: true }), SHORTCUTS.runQuery.combo, false)).toBe(
      false,
    );
    expect(matchesCombo(key({ key: "Enter", ctrlKey: true }), SHORTCUTS.runQuery.combo, false)).toBe(true);
    expect(
      matchesCombo(key({ key: "Enter", ctrlKey: true, shiftKey: true }), SHORTCUTS.explainQuery.combo, false),
    ).toBe(true);
    expect(matchesCombo(key({ key: ".", metaKey: true }), SHORTCUTS.cancelQuery.combo, true)).toBe(true);
  });

  it("labels and CodeMirror names", () => {
    expect(comboKeys(newTab, true)).toEqual(["⌘", "⌥", "N"]);
    expect(formatCombo(newTab, false)).toBe("Ctrl+Alt+N");
    expect(codeMirrorKey(SHORTCUTS.explainQuery.combo)).toBe("Mod-Shift-Enter");
    expect(codeMirrorKey(SHORTCUTS.cancelQuery.combo)).toBe("Mod-.");
  });
});

describe("word diff", () => {
  it("marks changed words and re-joins to both texts", () => {
    const before = "WHERE placed_at >= now() - interval '30 days'\nORDER BY 2";
    const after = "WHERE placed_at >= now() - interval '7 days'\nORDER BY 2";
    const parts = diffWords(before, after);
    expect(hasChanges(parts)).toBe(true);
    expect(
      parts
        .filter((p) => p.type !== "added")
        .map((p) => p.text)
        .join(""),
    ).toBe(before);
    expect(
      parts
        .filter((p) => p.type !== "removed")
        .map((p) => p.text)
        .join(""),
    ).toBe(after);
    expect(parts.filter((p) => p.type === "removed").map((p) => p.text.trim())).toEqual(["'30"]);
    expect(hasChanges(diffWords("a b", "a b"))).toBe(false);
  });
});
