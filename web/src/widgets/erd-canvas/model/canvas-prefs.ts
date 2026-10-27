import { create } from "zustand";
import { safeLocalStorage } from "@/shared/lib/storage";
import type { LayoutAlgorithm } from "@/widgets/erd-canvas/model/layout/types";

const PREFS_KEY = "dbscope.erd.prefs";

interface Prefs {
  /** The algorithm the picker shows; null until the user picks one (then the size-based default applies). */
  algorithm: LayoutAlgorithm | null;
  showInferred: boolean;
  minimap: boolean;
}

const DEFAULTS: Prefs = { algorithm: null, showInferred: true, minimap: true };

function read(): Prefs {
  try {
    const stored = JSON.parse(safeLocalStorage.getItem(PREFS_KEY) ?? "null") as Partial<Prefs> | null;
    return {
      algorithm: ["layered", "force", "grid"].includes(stored?.algorithm as string)
        ? (stored?.algorithm as LayoutAlgorithm)
        : null,
      showInferred: typeof stored?.showInferred === "boolean" ? stored.showInferred : DEFAULTS.showInferred,
      minimap: typeof stored?.minimap === "boolean" ? stored.minimap : DEFAULTS.minimap,
    };
  } catch {
    return DEFAULTS;
  }
}

interface PrefsState extends Prefs {
  set: (patch: Partial<Prefs>) => void;
}

/** View preferences of the canvas, kept per browser. Positions are saved per layout key instead. */
export const useCanvasPrefs = create<PrefsState>((set, get) => ({
  ...read(),
  set: (patch) => {
    set(patch);
    const { algorithm, showInferred, minimap } = get();
    safeLocalStorage.setItem(PREFS_KEY, JSON.stringify({ algorithm, showInferred, minimap }));
  },
}));
