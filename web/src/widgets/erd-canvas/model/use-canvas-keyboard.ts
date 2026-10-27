import type { XYPosition } from "@xyflow/react";
import { type KeyboardEvent, type RefObject, useCallback, useRef } from "react";
import { nodeLayout, type SchemaGraph, useCanvasStore } from "@/entities/schema";
import { announce } from "@/shared/lib/announcer";
import { type Box, type Direction, firstInReadingOrder, nearestInDirection } from "@/widgets/erd-canvas/model/spatial";

const ARROWS: Record<string, Direction> = {
  ArrowUp: "up",
  ArrowDown: "down",
  ArrowLeft: "left",
  ArrowRight: "right",
};

export function describeTable(graph: SchemaGraph, id: string): string {
  const r = graph.byKey.get(id);
  if (!r) return id;
  if (r.stub) return `${id}, outside the selected schemas`;
  const out = graph.outgoing.get(id)?.length ?? 0;
  const incoming = graph.incoming.get(id)?.length ?? 0;
  return `${id}, ${r.kind.replace("_", " ")}, ${r.columns?.length ?? 0} columns, ${out} foreign keys, referenced by ${incoming}`;
}

function go(graph: SchemaGraph, id: string, prefix = ""): void {
  useCanvasStore.getState().select(id, { reveal: true });
  announce(`${prefix}${describeTable(graph, id)}`);
}

interface Options {
  positions: RefObject<Map<string, XYPosition>>;
  collapsed: ReadonlySet<string>;
}

/**
 * Canvas-local keys (the canvas element has focus): arrows move the selection
 * spatially, Enter opens details, Tab follows the selected table's foreign
 * keys in turn and Shift+Tab walks back, Esc clears one thing at a time. Tab
 * and Shift+Tab fall through to normal focus movement when there is nothing
 * to follow, so the canvas never traps focus.
 */
export function useCanvasKeyboard({ positions, collapsed }: Options) {
  const trail = useRef<string[]>([]);
  const fkCursor = useRef(new Map<string, number>());

  const boxes = useCallback(
    (graph: SchemaGraph): Box[] => {
      const out: Box[] = [];
      for (const [id, position] of positions.current ?? []) {
        const relation = graph.byKey.get(id);
        if (!relation) continue;
        const { width, height } = nodeLayout(relation, collapsed.has(id));
        out.push({ id, position, width, height });
      }
      return out;
    },
    [positions, collapsed],
  );

  return useCallback(
    (event: KeyboardEvent<HTMLElement>) => {
      if (event.target !== event.currentTarget || event.altKey || event.metaKey || event.ctrlKey) return;
      const store = useCanvasStore.getState();
      const { graph, selectedId } = store;
      if (!graph) return;

      const direction = ARROWS[event.key];
      if (direction) {
        event.preventDefault();
        const all = boxes(graph);
        const next = selectedId ? nearestInDirection(all, selectedId, direction) : firstInReadingOrder(all);
        if (next) {
          trail.current = [];
          store.setSelectedEdge(null);
          go(graph, next);
        } else if (selectedId) {
          announce(`No table ${direction === "up" || direction === "down" ? direction : `to the ${direction}`}.`);
        }
        return;
      }

      if (event.key === "Enter" && selectedId) {
        event.preventDefault();
        store.setDetailsOpen(true);
        return;
      }

      if (event.key === "Tab" && selectedId) {
        if (event.shiftKey) {
          const back = trail.current.pop();
          if (!back) return;
          event.preventDefault();
          store.setSelectedEdge(null);
          go(graph, back, "Back to ");
          return;
        }
        const outgoing = (graph.outgoing.get(selectedId) ?? []).filter((e) => !e.selfRef);
        if (outgoing.length === 0) return;
        event.preventDefault();
        const index = fkCursor.current.get(selectedId) ?? 0;
        const edge = outgoing[index % outgoing.length];
        if (!edge) return;
        fkCursor.current.set(selectedId, index + 1);
        trail.current.push(selectedId);
        store.setSelectedEdge(edge.id);
        const columns = edge.pairs.map((p) => p.column).join(", ");
        go(graph, edge.parent, `Followed ${columns} (${(index % outgoing.length) + 1} of ${outgoing.length}) to `);
        return;
      }

      if (event.key === "Escape") {
        const cleared = store.clearOne();
        if (cleared) {
          event.preventDefault();
          if (cleared === "selection") trail.current = [];
        }
      }
    },
    [boxes],
  );
}
