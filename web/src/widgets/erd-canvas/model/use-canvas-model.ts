import { applyNodeChanges, type NodeChange, useReactFlow, type XYPosition } from "@xyflow/react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { relationKey, type SchemaGraph, type TableNodeType } from "@/entities/schema";
import type { CanvasLayoutInput } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { motionDuration } from "@/shared/lib/reduced-motion";
import { buildEdges, buildNodes, layoutNodes, usedHandles } from "@/widgets/erd-canvas/model/elements";
import { LayoutCancelled, runLayout } from "@/widgets/erd-canvas/model/layout/run-layout";
import { defaultAlgorithm, LAYOUT_LABELS, type LayoutAlgorithm } from "@/widgets/erd-canvas/model/layout/types";
import { snapshotLayout } from "@/widgets/erd-canvas/model/layout-saver";
import { coversAll, placeMissing } from "@/widgets/erd-canvas/model/positions";

export type LayoutStatus =
  | { state: "idle" }
  | { state: "running"; algorithm: LayoutAlgorithm; count: number }
  | { state: "error"; message: string };

interface Options {
  graph: SchemaGraph;
  layoutKey: string;
  restored: CanvasLayoutInput | null | undefined;
  showInferred: boolean;
  preferred: LayoutAlgorithm | null;
  schedule: (layout: CanvasLayoutInput) => void;
}

function byId(nodes: readonly TableNodeType[]): Map<string, TableNodeType> {
  return new Map(nodes.map((n): [string, TableNodeType] => [n.id, n]));
}

/**
 * Owns the React Flow nodes and edges. Positions change only on drag, an
 * explicit layout, or a schema whose layout key has nothing saved; selection,
 * search, focus and overlays live in the canvas store and never pass through
 * here, so they never rebuild these arrays or relayout.
 */
export function useCanvasModel({ graph, layoutKey, restored, showInferred, preferred, schedule }: Options) {
  const { fitView } = useReactFlow();
  const [nodes, setNodes] = useState<TableNodeType[]>([]);
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const [status, setStatus] = useState<LayoutStatus>({ state: "idle" });
  const [ready, setReady] = useState(false);
  const positions = useRef(new Map<string, XYPosition>());
  const collapsedRef = useRef(collapsed);
  collapsedRef.current = collapsed;
  const graphRef = useRef(graph);
  graphRef.current = graph;
  const initialised = useRef(false);

  const edges = useMemo(() => buildEdges(graph, { collapsed, showInferred }), [graph, collapsed, showInferred]);
  const anchors = useMemo(() => usedHandles(edges), [edges]);
  const anchorsRef = useRef(anchors);
  anchorsRef.current = anchors;

  const save = useCallback(() => {
    schedule(snapshotLayout(layoutKey, positions.current, collapsedRef.current));
  }, [layoutKey, schedule]);

  const rebuild = useCallback((nextCollapsed: ReadonlySet<string>) => {
    const g = graphRef.current;
    setNodes((prev) => buildNodes(g, positions.current, nextCollapsed, byId(prev), anchorsRef.current));
  }, []);

  const fitAll = useCallback(() => {
    // Hidden: on very large schemas the overview hides the tables and paints them on a canvas.
    void fitView({ padding: 0.08, duration: motionDuration(250), maxZoom: 1.2, includeHiddenNodes: true });
  }, [fitView]);

  const relayout = useCallback(
    async (algorithm: LayoutAlgorithm, options: { save?: boolean } = {}) => {
      const g = graphRef.current;
      setStatus({ state: "running", algorithm, count: g.relations.length });
      try {
        const result = await runLayout(algorithm, g, collapsedRef.current);
        positions.current = placeMissing(layoutNodes(graphRef.current, collapsedRef.current), result.positions);
        rebuild(collapsedRef.current);
        setStatus({ state: "idle" });
        setReady(true);
        fitAll();
        if (options.save !== false) save();
        announce(
          `${LAYOUT_LABELS[algorithm]} layout of ${g.relations.length} tables took ${(result.ms / 1000).toFixed(1)} s.`,
        );
      } catch (error) {
        if (error instanceof LayoutCancelled) return;
        setStatus({ state: "error", message: error instanceof Error ? error.message : String(error) });
        // Never leave the canvas empty: fall back to a quick in-thread placement.
        if (positions.current.size === 0) {
          positions.current = placeMissing(layoutNodes(g, collapsedRef.current), new Map());
          rebuild(collapsedRef.current);
          setReady(true);
          fitAll();
        }
      }
    },
    [rebuild, fitAll, save],
  );

  // First paint for this layout key: saved positions, or a fresh layout.
  useEffect(() => {
    if (restored === undefined || initialised.current) return;
    initialised.current = true;
    const nextCollapsed = new Set((restored?.collapsed ?? []).filter((id) => graph.byKey.has(id)));
    collapsedRef.current = nextCollapsed;
    setCollapsed(nextCollapsed);
    const saved = new Map(Object.entries(restored?.positions ?? {}));
    const sized = layoutNodes(graph, nextCollapsed);
    if (saved.size > 0) {
      positions.current = placeMissing(sized, saved);
      rebuild(nextCollapsed);
      setReady(true);
      fitAll();
      if (!coversAll(sized, saved)) save();
      return;
    }
    void relayout(preferred ?? defaultAlgorithm(graph.relations.length));
  }, [restored, graph, preferred, rebuild, relayout, fitAll, save]);

  // A refreshed or edited model with the same layout key keeps every position; new tables go to the side.
  useEffect(() => {
    if (!initialised.current || positions.current.size === 0) return;
    const sized = layoutNodes(graph, collapsedRef.current);
    const added = !coversAll(sized, positions.current);
    positions.current = placeMissing(sized, positions.current);
    rebuild(collapsedRef.current);
    if (added) save();
  }, [graph, rebuild, save]);

  const onNodesChange = useCallback(
    (changes: NodeChange<TableNodeType>[]) => {
      let dragEnded = false;
      for (const c of changes) {
        if (c.type === "position" && c.position) positions.current.set(c.id, c.position);
        if (c.type === "position" && c.dragging === false) dragEnded = true;
      }
      setNodes((prev) => applyNodeChanges(changes, prev));
      if (dragEnded) save();
    },
    [save],
  );

  const applyCollapsed = useCallback(
    (next: ReadonlySet<string>) => {
      collapsedRef.current = next;
      setCollapsed(next);
      rebuild(next);
      save();
    },
    [rebuild, save],
  );

  const toggleCollapsed = useCallback(
    (id: string) => {
      const next = new Set(collapsedRef.current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      applyCollapsed(next);
    },
    [applyCollapsed],
  );

  const collapseAll = useCallback(() => {
    applyCollapsed(new Set(graphRef.current.relations.filter((r) => !r.stub).map(relationKey)));
  }, [applyCollapsed]);
  const expandAll = useCallback(() => applyCollapsed(new Set()), [applyCollapsed]);

  const resetLayout = useCallback(() => {
    collapsedRef.current = new Set();
    setCollapsed(collapsedRef.current);
    void relayout(defaultAlgorithm(graphRef.current.relations.length));
  }, [relayout]);

  // Edges changed (collapse, inferred keys shown or decided): refresh the nodes' DOM anchors.
  useEffect(() => {
    if (anchors && initialised.current && positions.current.size > 0) rebuild(collapsedRef.current);
  }, [anchors, rebuild]);

  return {
    nodes,
    edges,
    collapsed,
    status,
    ready,
    positions,
    onNodesChange,
    toggleCollapsed,
    collapseAll,
    expandAll,
    relayout,
    resetLayout,
    fitAll,
    dismissError: () => setStatus({ state: "idle" }),
  };
}

export type CanvasModel = ReturnType<typeof useCanvasModel>;
