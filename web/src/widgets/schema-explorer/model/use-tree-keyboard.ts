import type { KeyboardEvent } from "react";
import type { TreeRow, TreeRowPosition } from "@/widgets/schema-explorer/model/tree";

interface Options {
  rows: readonly TreeRow[];
  positions: ReadonlyMap<string, TreeRowPosition>;
  activeId: string | null;
  setActive: (id: string) => void;
  setExpanded: (id: string, open: boolean) => void;
  activate: (row: TreeRow) => void;
}

function isExpandable(row: TreeRow): boolean {
  return row.type === "schema" || (row.type === "relation" && row.hasChildren);
}

/** WAI-ARIA tree keyboard model: Up/Down, Home/End, Right/Left to open, close or move, Enter/Space to select. */
export function treeKeyHandler({ rows, positions, activeId, setActive, setExpanded, activate }: Options) {
  return (event: KeyboardEvent<HTMLElement>) => {
    const index = rows.findIndex((r) => r.id === activeId);
    const row = rows[index];
    if (!row) return;
    const move = (to: number) => {
      const target = rows[Math.max(0, Math.min(rows.length - 1, to))];
      if (target) setActive(target.id);
    };
    switch (event.key) {
      case "ArrowDown":
        move(index + 1);
        break;
      case "ArrowUp":
        move(index - 1);
        break;
      case "Home":
        move(0);
        break;
      case "End":
        move(rows.length - 1);
        break;
      case "ArrowRight":
        if (isExpandable(row) && row.type !== "column" && !row.expanded) setExpanded(row.id, true);
        else if (isExpandable(row)) move(index + 1);
        break;
      case "ArrowLeft": {
        if (row.type !== "column" && isExpandable(row) && row.expanded) {
          setExpanded(row.id, false);
          break;
        }
        const parent = positions.get(row.id)?.parentId;
        if (parent) setActive(parent);
        break;
      }
      case "Enter":
      case " ":
        activate(row);
        break;
      default:
        return;
    }
    event.preventDefault();
  };
}
