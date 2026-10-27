import { useEffect, useRef } from "react";
import type { KeyCombo } from "@/shared/config/shortcuts";
import { isMacPlatform } from "@/shared/lib/platform";

export function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  if (target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) return true;
  if (target instanceof HTMLInputElement) {
    return !["button", "checkbox", "radio", "submit", "reset", "range", "color", "file"].includes(target.type);
  }
  return target.getAttribute("role") === "textbox" || target.getAttribute("role") === "combobox";
}

function isLetter(key: string): boolean {
  return /^[a-z]$/i.test(key);
}

export function matchesCombo(event: KeyboardEvent, combo: KeyCombo, mac = isMacPlatform()): boolean {
  if (event.altKey !== Boolean(combo.alt)) return false;
  // On Windows and Linux AltGr reports Ctrl+Alt; it types characters (Polish AltGr+N is "ń"), never a shortcut.
  if (combo.alt && !mac && event.getModifierState?.("AltGraph")) return false;
  const modPressed = mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
  const noMod = !event.metaKey && !event.ctrlKey;
  if (combo.mod ? !modPressed : !noMod) return false;
  if (isLetter(combo.key) || combo.key.length > 1) {
    if (event.shiftKey !== Boolean(combo.shift)) return false;
    // Option on macOS changes event.key ("n" becomes "˜"), so letters with Alt match the physical key.
    if (combo.alt && isLetter(combo.key)) return event.code === `Key${combo.key.toUpperCase()}`;
    return event.key.toLowerCase() === combo.key.toLowerCase();
  }
  // Symbols such as "?" already encode Shift in event.key.
  return event.key === combo.key;
}

/** Single printable keys must not fire while the user is typing. */
function firesInEditable(combo: KeyCombo): boolean {
  return Boolean(combo.mod) || combo.key.length > 1;
}

export interface HotkeyBinding {
  combo: KeyCombo;
  handler: (event: KeyboardEvent) => void;
}

/** Registers window-level shortcuts. Bindings may change on every render. */
export function useHotkeys(bindings: HotkeyBinding[]): void {
  const latest = useRef(bindings);
  latest.current = bindings;

  useEffect(() => {
    const mac = isMacPlatform();
    function onKeyDown(event: KeyboardEvent) {
      if (event.isComposing || event.defaultPrevented) return;
      for (const binding of latest.current) {
        if (!matchesCombo(event, binding.combo, mac)) continue;
        if (!firesInEditable(binding.combo) && isEditableTarget(event.target)) return;
        event.preventDefault();
        binding.handler(event);
        return;
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
}

/** CodeMirror key name for a combo, e.g. "Mod-Shift-Enter". */
export function codeMirrorKey(combo: KeyCombo): string {
  const parts: string[] = [];
  if (combo.mod) parts.push("Mod");
  if (combo.alt) parts.push("Alt");
  if (combo.shift) parts.push("Shift");
  parts.push(combo.key.length === 1 ? combo.key.toLowerCase() : combo.key);
  return parts.join("-");
}
