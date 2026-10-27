import { ERD_TAB, useSqlTabs } from "@/entities/query";
import { useCanvasStore } from "@/entities/schema";
import type { ResultColumn } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";

/** Switches to the ERD tab and selects (and pans to) the table column a result column came from. */
export function showColumnOnCanvas(column: ResultColumn): void {
  if (!column.relation) return;
  const { relation, column: name } = column;
  useSqlTabs.getState().setActive(ERD_TAB);
  // Wait a frame so the ERD panel is visible and React Flow measures it before panning.
  requestAnimationFrame(() => {
    const store = useCanvasStore.getState();
    if (!store.graph?.byKey.has(relation)) {
      announce(`${relation} is not on the canvas; it may be outside the selected schemas.`);
      return;
    }
    store.select(relation, { reveal: true, ...(name ? { column: name } : {}) });
    announce(`Showing ${relation}${name ? `.${name}` : ""} on the canvas.`);
  });
}
