import type { HeatState, OverlayEntry, OverlayTone } from "@/entities/schema/model/canvas-store";
import { useCanvasStore } from "@/entities/schema/model/canvas-store";
import type { Overlay, OverlayLevel, OverlayScan } from "@/shared/api";
import { formatCompact } from "@/shared/lib/format-number";

const LEVEL: Record<OverlayLevel, { tone: OverlayTone; label: string }> = {
  good: { tone: "success", label: "OK" },
  warn: { tone: "warn", label: "Warning" },
  danger: { tone: "danger", label: "Problem" },
};

function rows(n: number): string {
  return `${formatCompact(n)} row${n === 1 ? "" : "s"}`;
}

/** "Index Scan using orders_pkey: est. 1 row, actual 1 row". */
export function describeScan(scan: OverlayScan): string {
  const using = scan.index ? ` using ${scan.index}` : "";
  const actual = scan.actualRows === undefined ? "" : `, actual ${rows(scan.actualRows)}`;
  return `${scan.nodeType}${using}: est. ${rows(scan.planRows)}${actual}`;
}

/** Converts a query's plan overlay (docs/API.md Overlay) into canvas badges and join animation state. */
export function heatmapFromOverlay(overlay: Overlay): { entries: Record<string, OverlayEntry>; heat: HeatState } {
  const entries: Record<string, OverlayEntry> = {};
  for (const [key, rel] of Object.entries(overlay.relations ?? {})) {
    const { tone, label } = LEVEL[rel.level] ?? LEVEL.good;
    entries[key] = { tone, label, details: [...(rel.reasons ?? []), ...(rel.scans ?? []).map(describeScan)] };
  }
  const joins = (overlay.joins ?? []).map(({ from, to }) => ({ from, to }));
  const involved = new Set([...Object.keys(entries), ...joins.flatMap((j) => [j.from, j.to])]);
  return { entries, heat: { joins, involved } };
}

/**
 * Shows a query's plan on the canvas. Writes only the canvas store, never
 * React Flow's nodes or edges, so it cannot trigger a relayout.
 */
export function showQueryOverlay(overlay: Overlay): void {
  const { entries, heat } = heatmapFromOverlay(overlay);
  useCanvasStore.getState().setHeatmap(entries, heat);
}

export function highlightTables(ids: Iterable<string> | null): void {
  useCanvasStore.getState().setHighlight(ids);
}

export function clearCanvasOverlay(): void {
  useCanvasStore.getState().clearOverlay();
}

export function hasCanvasOverlay(s: {
  heat: HeatState | null;
  highlight: ReadonlySet<string> | null;
  overlay: Readonly<Record<string, OverlayEntry>>;
}): boolean {
  return Boolean(s.heat || s.highlight || Object.keys(s.overlay).length > 0);
}
