import "@xyflow/react/dist/base.css";
import "@/widgets/erd-canvas/ui/erd-canvas.css";
import {
  Background,
  BackgroundVariant,
  ConnectionMode,
  type EdgeMouseHandler,
  MiniMap,
  type NodeMouseHandler,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
} from "@xyflow/react";
import { useCallback, useEffect, useMemo, useRef } from "react";
import {
  fkIdOfPairEdge,
  type SchemaGraph,
  type TableNodeType,
  useCanvasCommands,
  useCanvasStore,
} from "@/entities/schema";
import {
  FarZoomContext,
  farZoomFor,
  StubNode,
  TableNode,
  TableNodeActionsContext,
  useZoomDetail,
} from "@/entities/schema/canvas";
import { useResolvedTheme } from "@/features/toggle-theme";
import type { CanvasLayoutInput } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { motionDuration } from "@/shared/lib/reduced-motion";
import { useCanvasPrefs } from "@/widgets/erd-canvas/model/canvas-prefs";
import type { RelationshipEdgeType } from "@/widgets/erd-canvas/model/elements";
import { defaultAlgorithm, LAYOUT_LABELS, type LayoutAlgorithm } from "@/widgets/erd-canvas/model/layout/types";
import { useCanvasKeyboard } from "@/widgets/erd-canvas/model/use-canvas-keyboard";
import { useCanvasModel } from "@/widgets/erd-canvas/model/use-canvas-model";
import { useReveal } from "@/widgets/erd-canvas/model/use-reveal";
import { useTestHook } from "@/widgets/erd-canvas/model/use-test-hook";
import { CanvasToolbar } from "@/widgets/erd-canvas/ui/canvas-toolbar";
import { EdgeMarkers } from "@/widgets/erd-canvas/ui/edge-markers";
import { FarEdges } from "@/widgets/erd-canvas/ui/far-edges";
import { LayoutStatusBanner } from "@/widgets/erd-canvas/ui/layout-status";
import { MigrationLegend } from "@/widgets/erd-canvas/ui/migration-legend";
import { RelationshipEdge } from "@/widgets/erd-canvas/ui/relationship-edge";
import { TableDetails } from "@/widgets/erd-canvas/ui/table-details";
import { ZoomControls } from "@/widgets/erd-canvas/ui/zoom-controls";

/** Module-level so React Flow never sees new component types (its docs warn this remounts everything). */
const NODE_TYPES = { table: TableNode, stub: StubNode };
const EDGE_TYPES = { relationship: RelationshipEdge };
const PRO_OPTIONS = { hideAttribution: false };
const NO_EDGES: RelationshipEdgeType[] = [];

interface CanvasViewProps {
  graph: SchemaGraph;
  layoutKey: string;
  restored: CanvasLayoutInput | null | undefined;
  schedule: (layout: CanvasLayoutInput) => void;
  /** The connection the canvas shows; undefined for the sample schema. */
  profileId?: string | undefined;
}

/** The lazily loaded canvas root; it owns its React Flow store. */
export function CanvasView(props: CanvasViewProps) {
  return (
    <ReactFlowProvider>
      <FarZoomContext value={farZoomFor(props.graph.relations.length)}>
        <CanvasViewInner {...props} />
      </FarZoomContext>
    </ReactFlowProvider>
  );
}

function CanvasViewInner({ graph, layoutKey, restored, schedule, profileId }: CanvasViewProps) {
  const theme = useResolvedTheme();
  const { showInferred, minimap, algorithm: preferred, set: setPrefs } = useCanvasPrefs();
  const algorithm = preferred ?? defaultAlgorithm(graph.relations.length);
  const model = useCanvasModel({ graph, layoutKey, restored, showInferred, preferred, schedule });
  const { fitView } = useReactFlow();
  const paneRef = useRef<HTMLDivElement>(null);
  // Overview zoom: nearly every table is on screen, so viewport culling only adds per-frame work and
  // mount churn, and per-edge components (each recomputing on every pan frame) give way to one path.
  // Measured with 1,500 tables: ~100 ms a pan frame before, ~17 ms after (see e2e/perf.spec.ts).
  const far = useZoomDetail() === "far";
  const cull = !far;
  const searchRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    useCanvasStore.getState().setGraph(graph);
  }, [graph]);
  useEffect(() => () => useCanvasStore.getState().setGraph(null), []);

  useReveal(model.positions, model.collapsed);
  useTestHook(graph, model.ready, model.positions);
  const onKeyDown = useCanvasKeyboard({ positions: model.positions, collapsed: model.collapsed });

  const runLayoutChoice = useCallback(
    (next: LayoutAlgorithm) => {
      setPrefs({ algorithm: next });
      void model.relayout(next);
    },
    [setPrefs, model.relayout],
  );

  const toggleFocus = useCallback(() => {
    const store = useCanvasStore.getState();
    if (!store.toggleFocus()) {
      announce("Select a table first, then press F to focus on it and its neighbours.");
      return;
    }
    const { focus, derived } = useCanvasStore.getState();
    if (focus && derived.keep) {
      void fitView({
        nodes: [...derived.keep].map((id) => ({ id })),
        padding: 0.2,
        duration: motionDuration(250),
        maxZoom: 1.2,
      });
      announce(
        `Focus mode: ${derived.keep.size} tables within ${focus.hops} hop${focus.hops === 1 ? "" : "s"} of ${focus.root}.`,
      );
    } else {
      announce("Left focus mode.");
    }
  }, [fitView]);

  const focusSearch = useCallback(() => {
    searchRef.current?.focus();
    searchRef.current?.select();
  }, []);
  const focusPane = useCallback(() => paneRef.current?.focus(), []);

  const register = useCanvasCommands((s) => s.register);
  useEffect(
    () =>
      register({
        focusSearch,
        toggleFocus,
        fitView: model.fitAll,
        collapseAll: model.collapseAll,
        expandAll: model.expandAll,
        layoutLayered: () => runLayoutChoice("layered"),
        layoutForce: () => runLayoutChoice("force"),
        layoutGrid: () => runLayoutChoice("grid"),
        resetLayout: model.resetLayout,
        toggleInferred: () => setPrefs({ showInferred: !useCanvasPrefs.getState().showInferred }),
        toggleMinimap: () => setPrefs({ minimap: !useCanvasPrefs.getState().minimap }),
      }),
    [
      register,
      focusSearch,
      toggleFocus,
      model.fitAll,
      model.collapseAll,
      model.expandAll,
      model.resetLayout,
      runLayoutChoice,
      setPrefs,
    ],
  );

  const onNodeClick: NodeMouseHandler<TableNodeType> = useCallback((_, node) => {
    const store = useCanvasStore.getState();
    store.select(node.id);
    store.setSelectedEdge(null);
  }, []);
  const onNodeDoubleClick: NodeMouseHandler<TableNodeType> = useCallback((_, node) => {
    useCanvasStore.getState().select(node.id);
    useCanvasStore.getState().setDetailsOpen(true);
  }, []);
  const onEdgeClick: EdgeMouseHandler<RelationshipEdgeType> = useCallback((_, edge) => {
    useCanvasStore.getState().setSelectedEdge(fkIdOfPairEdge(edge.id));
  }, []);
  const onEdgeEnter: EdgeMouseHandler<RelationshipEdgeType> = useCallback((_, edge) => {
    useCanvasStore.getState().setHoverEdge(fkIdOfPairEdge(edge.id));
  }, []);
  const onEdgeLeave: EdgeMouseHandler<RelationshipEdgeType> = useCallback(() => {
    useCanvasStore.getState().setHoverEdge(null);
  }, []);
  const onPaneClick = useCallback(() => {
    const store = useCanvasStore.getState();
    store.select(null);
    store.setSelectedEdge(null);
  }, []);

  const actions = useMemo(() => ({ toggleCollapsed: model.toggleCollapsed }), [model.toggleCollapsed]);
  const inferredCount = useMemo(() => graph.edges.filter((e) => e.inferred && !e.accepted).length, [graph]);

  return (
    <div className="erd-canvas flex h-full min-h-0 flex-col">
      <CanvasToolbar
        profileId={profileId}
        searchRef={searchRef}
        onExitSearch={focusPane}
        algorithm={algorithm}
        layoutRunning={model.status.state === "running"}
        onLayout={runLayoutChoice}
        onCollapseAll={model.collapseAll}
        onExpandAll={model.expandAll}
        onToggleFocus={toggleFocus}
        onResetLayout={() => {
          setPrefs({ algorithm: null });
          model.resetLayout();
          announce(`Reset the layout to ${LAYOUT_LABELS[defaultAlgorithm(graph.relations.length)].toLowerCase()}.`);
        }}
        inferredCount={inferredCount}
      />
      <div
        ref={paneRef}
        role="application"
        aria-roledescription="ERD canvas"
        aria-label={`ERD canvas, ${graph.relations.length} tables. Arrow keys move between tables, Enter opens details, Tab follows a foreign key.`}
        data-testid="erd-canvas"
        // biome-ignore lint/a11y/noNoninteractiveTabindex: role=application is the canvas's single tab stop; arrows, Enter, Tab and Esc act on tables (use-canvas-keyboard).
        tabIndex={0}
        onKeyDown={onKeyDown}
        className="relative min-h-0 flex-1 outline-none focus-visible:ring-2 focus-visible:ring-border-focus focus-visible:ring-inset"
      >
        <EdgeMarkers />
        <TableNodeActionsContext value={actions}>
          <ReactFlow<TableNodeType, RelationshipEdgeType>
            nodes={model.nodes}
            edges={far ? NO_EDGES : model.edges}
            nodeTypes={NODE_TYPES}
            edgeTypes={EDGE_TYPES}
            onNodesChange={model.onNodesChange}
            onNodeClick={onNodeClick}
            onNodeDoubleClick={onNodeDoubleClick}
            onEdgeClick={onEdgeClick}
            onEdgeMouseEnter={onEdgeEnter}
            onEdgeMouseLeave={onEdgeLeave}
            onPaneClick={onPaneClick}
            colorMode={theme}
            connectionMode={ConnectionMode.Loose}
            nodesConnectable={false}
            elementsSelectable={false}
            nodesFocusable={false}
            edgesFocusable={false}
            disableKeyboardA11y
            onlyRenderVisibleElements={cull}
            zoomOnDoubleClick={false}
            minZoom={0.05}
            maxZoom={2}
            proOptions={PRO_OPTIONS}
            attributionPosition="top-right"
          >
            <Background variant={BackgroundVariant.Dots} gap={24} size={1} color="var(--border-default)" />
            {far && <FarEdges nodes={model.nodes} edges={model.edges} />}
            <ZoomControls onFit={model.fitAll} />
            {minimap && (
              <MiniMap
                pannable
                zoomable
                ariaLabel="Minimap of the ERD"
                nodeColor="var(--border-control)"
                nodeStrokeWidth={0}
                maskColor="var(--overlay-backdrop)"
                bgColor="var(--surface-subtle)"
              />
            )}
          </ReactFlow>
        </TableNodeActionsContext>
        <LayoutStatusBanner status={model.status} onDismiss={model.dismissError} />
        <MigrationLegend />
        <TableDetails
          onClose={() => {
            useCanvasStore.getState().setDetailsOpen(false);
            focusPane();
          }}
        />
      </div>
    </div>
  );
}
