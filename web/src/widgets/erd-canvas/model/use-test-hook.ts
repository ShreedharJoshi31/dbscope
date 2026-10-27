import { useReactFlow, type Viewport, type XYPosition } from "@xyflow/react";
import { type RefObject, useEffect, useRef } from "react";
import type { SchemaGraph } from "@/entities/schema";

export interface CanvasProbe {
  ready: boolean;
  tables: number;
  getViewport: () => Viewport;
  setViewport: (viewport: Viewport) => void;
  /** Canvas position of a table; off-screen nodes are not in the DOM (onlyRenderVisibleElements). */
  positionOf: (id: string) => XYPosition | null;
}

declare global {
  interface Window {
    __dbscopeCanvas?: CanvasProbe;
  }
}

/**
 * A small read/drive handle for the performance tests: they move the viewport
 * once per animation frame, which a pointer script cannot do reliably. It
 * exposes the viewport and table positions, never schema contents. Also records a
 * `dbscope:canvas-ready` measure from navigation start to the first painted
 * frame with the laid-out nodes.
 */
export function useTestHook(graph: SchemaGraph, ready: boolean, positions: RefObject<Map<string, XYPosition>>): void {
  const { getViewport, setViewport } = useReactFlow();
  const marked = useRef(false);

  useEffect(() => {
    window.__dbscopeCanvas = {
      ready,
      tables: graph.relations.length,
      getViewport,
      setViewport,
      positionOf: (id) => positions.current?.get(id) ?? null,
    };
    return () => {
      delete window.__dbscopeCanvas;
    };
  }, [graph, ready, getViewport, setViewport, positions]);

  useEffect(() => {
    if (!ready || marked.current) return;
    marked.current = true;
    requestAnimationFrame(() =>
      requestAnimationFrame(() => {
        try {
          performance.measure("dbscope:canvas-ready", {
            start: 0,
            end: performance.now(),
            detail: { tables: graph.relations.length },
          });
        } catch {
          // Diagnostics only.
        }
      }),
    );
  }, [ready, graph]);
}
