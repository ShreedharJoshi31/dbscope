import "@xyflow/react/dist/base.css";
import {
  Background,
  BackgroundVariant,
  BaseEdge,
  Controls,
  type Edge,
  type EdgeProps,
  Handle,
  MarkerType,
  type Node,
  type NodeProps,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
} from "@xyflow/react";
import { Expand, LoaderCircle, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { formatCell } from "@/entities/query";
import { useResolvedTheme } from "@/features/toggle-theme";
import { type ApiError, expandGraphNode, type GraphNode, type ResultGraph } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { formatGrouped } from "@/shared/lib/format-number";
import { Button } from "@/shared/ui/button";
import { layoutGraph, type Point } from "@/widgets/results-dock/model/graph-layout";
import { caption, labelColors, mergeGraph } from "@/widgets/results-dock/model/graph-view";

/** Neighbours fetched per expansion; the agent allows up to 200. */
const EXPAND_LIMIT = 50;
const SIZE = 36;
const DOUBLE_CLICK_MS = 250;
const FIT = { padding: 0.1, maxZoom: 1.25, duration: 150 };

type GraphNodeData = { node: GraphNode; color: number | undefined; caption: string; loading: boolean };
type DrawnNode = Node<GraphNodeData, "graphNode">;

function GraphNodeView({ data, selected }: NodeProps<DrawnNode>) {
  const { node, color } = data;
  const fill = color ? `var(--graph-${color})` : "var(--text-muted)";
  return (
    <div
      className="flex w-24 flex-col items-center gap-1"
      title={node.stub ? "Not in the result" : node.labels.join(", ")}
    >
      <div
        className={`flex items-center justify-center rounded-full border-2 text-2xs font-medium ${selected ? "ring-2 ring-border-focus" : ""} ${node.stub ? "border-dashed" : ""}`}
        style={{
          width: SIZE,
          height: SIZE,
          borderColor: fill,
          background: node.stub ? "var(--surface-canvas)" : `color-mix(in srgb, ${fill} 18%, var(--surface-raised))`,
          color: fill,
        }}
      >
        {data.loading ? (
          <LoaderCircle aria-hidden className="size-4 animate-spin" />
        ) : (
          (node.labels[0] ?? "?").slice(0, 3)
        )}
      </div>
      <span className="max-w-24 truncate text-center text-2xs text-text-primary">{data.caption}</span>
      {/* Edges run centre to centre, so both ends attach at the circle's middle, unseen. */}
      <Handle type="target" position={Position.Top} isConnectable={false} style={HANDLE} />
      <Handle type="source" position={Position.Top} isConnectable={false} style={HANDLE} />
    </div>
  );
}

const HANDLE = { top: SIZE / 2, left: "50%", opacity: 0, width: 1, height: 1, minWidth: 0, minHeight: 0, border: 0 };

const NODE_TYPES = { graphNode: GraphNodeView };

/** Handles sit at circle centres, so the line is trimmed to the rims and the arrowhead stays visible. */
function GraphEdgeView({ id, sourceX, sourceY, targetX, targetY, label, markerEnd }: EdgeProps) {
  const dx = targetX - sourceX;
  const dy = targetY - sourceY;
  const len = Math.hypot(dx, dy);
  const r = SIZE / 2 + 2;
  let path: string;
  let lx: number;
  let ly: number;
  if (len < r * 2) {
    // A self-loop, or nodes on top of each other: a small loop above the node.
    path = `M ${sourceX - 8},${sourceY - r} C ${sourceX - 30},${sourceY - r - 40} ${sourceX + 30},${sourceY - r - 40} ${sourceX + 8},${sourceY - r}`;
    lx = sourceX;
    ly = sourceY - r - 32;
  } else {
    const ux = dx / len;
    const uy = dy / len;
    const sx = sourceX + ux * r;
    const sy = sourceY + uy * r;
    const tx = targetX - ux * r;
    const ty = targetY - uy * r;
    path = `M ${sx},${sy} L ${tx},${ty}`;
    lx = (sx + tx) / 2;
    ly = (sy + ty) / 2;
  }
  return (
    <BaseEdge
      id={id}
      path={path}
      markerEnd={markerEnd}
      style={{ stroke: "var(--border-control)" }}
      label={label}
      labelX={lx}
      labelY={ly}
      labelStyle={{ fontSize: 10, fill: "var(--text-muted)" }}
      labelShowBg
      labelBgStyle={{ fill: "var(--surface-canvas)" }}
    />
  );
}

const EDGE_TYPES = { graphEdge: GraphEdgeView };

function toFlow(graph: ResultGraph, at: Record<string, Point>, loading: string | null) {
  const colors = labelColors(graph.nodes);
  const nodes: DrawnNode[] = graph.nodes.map((n) => {
    const p = at[n.id] ?? { x: 0, y: 0 };
    return {
      id: n.id,
      type: "graphNode",
      // Positions are centres; React Flow places top-left corners (the card is 96 wide, the circle SIZE high).
      position: { x: p.x - 48, y: p.y - SIZE / 2 },
      data: { node: n, color: colors.get(n.labels[0] ?? ""), caption: caption(n), loading: loading === n.id },
      ariaLabel: `${n.labels.join(" ") || "node"} ${caption(n)}`,
    };
  });
  const edges: Edge[] = graph.relationships.map((r) => ({
    id: r.id,
    source: r.start,
    target: r.end,
    type: "graphEdge",
    label: r.type,
    markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14 },
  }));
  return { nodes, edges };
}

interface GraphViewProps {
  initial: ResultGraph;
  profileId: string;
  canExpand: boolean;
}

export default function GraphView(props: GraphViewProps) {
  return (
    <ReactFlowProvider>
      <GraphViewInner {...props} />
    </ReactFlowProvider>
  );
}

function GraphViewInner({ initial, profileId, canExpand }: GraphViewProps) {
  const theme = useResolvedTheme();
  const { fitView } = useReactFlow();
  const [graph, setGraph] = useState(initial);
  const [at, setAt] = useState(() => layoutGraph(initial));
  const [selected, setSelected] = useState<string | null>(null);
  // The details panel covers part of the canvas, so it opens only once a click
  // is known not to be the first half of a double-click on a node beneath it.
  // React Flow fits the first drawing itself (the fitView prop). After an expansion the new
  // nodes are measured a render or two later, and a fit before that frames only the old ones,
  // so fit again once they have had time (measured in Chromium: one frame is not enough).
  const expanded = graph !== initial;
  // biome-ignore lint/correctness/useExhaustiveDependencies: graph is the trigger; each expansion changes it.
  useEffect(() => {
    if (!expanded) return;
    const frame = requestAnimationFrame(() => void fitView(FIT));
    const later = setTimeout(() => void fitView(FIT), 250);
    return () => {
      cancelAnimationFrame(frame);
      clearTimeout(later);
    };
  }, [expanded, graph, fitView]);

  // The tab can mount while the dock is still sizing it, so fit again whenever the
  // canvas changes size, until the user pans or zooms it themselves.
  const canvasRef = useRef<HTMLDivElement>(null);
  const userMoved = useRef(false);
  useEffect(() => {
    const el = canvasRef.current;
    if (!el) return;
    let frame = 0;
    const observer = new ResizeObserver(() => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (!userMoved.current && el.clientWidth > 0 && el.clientHeight > 0) void fitView(FIT);
      });
    });
    observer.observe(el);
    return () => {
      observer.disconnect();
      cancelAnimationFrame(frame);
    };
  }, [fitView]);
  const clickTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(clickTimer.current), []);
  const onNodeClick = (id: string) => {
    clearTimeout(clickTimer.current);
    clickTimer.current = setTimeout(() => setSelected(id), DOUBLE_CLICK_MS);
  };
  const onNodeDoubleClick = (id: string) => {
    clearTimeout(clickTimer.current);
    void expand(id);
  };
  const [loading, setLoading] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const flow = useMemo(() => toFlow(graph, at, loading), [graph, at, loading]);
  const node = graph.nodes.find((n) => n.id === selected);

  async function expand(id: string) {
    if (!canExpand || loading) return;
    setLoading(id);
    setNote(null);
    try {
      const x = await expandGraphNode(profileId, id, EXPAND_LIMIT);
      const next = mergeGraph(graph, x.graph);
      const added = next.nodes.length - graph.nodes.length;
      setGraph(next);
      setAt(layoutGraph(next, at));
      const msg =
        x.shown < x.degree
          ? `Showing ${formatGrouped(x.shown)} of ${formatGrouped(x.degree)} relationships of this node; ${formatGrouped(added)} new nodes.`
          : `All ${formatGrouped(x.degree)} relationships shown; ${formatGrouped(added)} new nodes.`;
      setNote(msg);
      announce(msg);
    } catch (err) {
      const msg = `Could not expand the node. ${(err as ApiError).message}`;
      setNote(msg);
      announce(msg);
    } finally {
      setLoading(null);
    }
  }

  return (
    <div className="relative h-full min-h-0">
      <div ref={canvasRef} className="h-full min-h-0" data-testid="graph-view">
        <ReactFlow<DrawnNode, Edge>
          nodes={flow.nodes}
          edges={flow.edges}
          nodeTypes={NODE_TYPES}
          edgeTypes={EDGE_TYPES}
          fitView
          fitViewOptions={FIT}
          onNodeClick={(_, n) => onNodeClick(n.id)}
          onNodeDoubleClick={(_, n) => onNodeDoubleClick(n.id)}
          onPaneClick={() => setSelected(null)}
          onMoveStart={(event) => {
            // Programmatic fits have no event; a wheel, drag or pinch does.
            if (event) userMoved.current = true;
          }}
          nodesConnectable={false}
          colorMode={theme}
          minZoom={0.05}
          proOptions={{ hideAttribution: false }}
        >
          <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
          <Controls showInteractive={false} position="top-left" style={{ marginTop: 28 }} />
        </ReactFlow>
        <p className="pointer-events-none absolute top-2 left-2 rounded-sm bg-surface-canvas/90 px-1.5 py-0.5 text-2xs text-text-muted">
          {formatGrouped(graph.nodes.length)} nodes, {formatGrouped(graph.relationships.length)} relationships
          {initial.truncated && " (the result had more; only the first 500 nodes are drawn)"}
          {canExpand && " · double-click a node to expand it"}
        </p>
        {note && (
          <p
            role="status"
            className="absolute right-2 bottom-2 left-2 rounded-sm bg-surface-raised px-2 py-1 text-xs shadow-sm"
          >
            {note}
          </p>
        )}
      </div>
      {node && (
        <aside
          aria-label="Node details"
          // Over the canvas, not beside it: resizing the canvas would move nodes between the two clicks of a double-click.
          className="absolute top-0 right-0 bottom-0 flex w-72 flex-col gap-2 overflow-y-auto border-l border-border-default bg-surface-canvas p-2 shadow-md"
        >
          <div className="flex items-center gap-1">
            <span className="truncate text-xs font-medium">{caption(node)}</span>
            <Button
              variant="ghost"
              size="icon-xs"
              className="ml-auto"
              aria-label="Close node details"
              onClick={() => setSelected(null)}
            >
              <X aria-hidden />
            </Button>
          </div>
          <p className="text-2xs text-text-muted">
            {node.stub ? "This node is a relationship's end the query did not return." : node.labels.join(", ")}
          </p>
          {canExpand && (
            <Button variant="outline" size="xs" disabled={loading !== null} onClick={() => void expand(node.id)}>
              {loading === node.id ? <LoaderCircle aria-hidden className="animate-spin" /> : <Expand aria-hidden />}
              Expand neighbours (up to {EXPAND_LIMIT})
            </Button>
          )}
          <dl className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-1 text-2xs">
            {Object.entries(node.props)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([k, v]) => (
                <div key={k} className="contents">
                  <dt className="font-mono text-text-muted">{k}</dt>
                  <dd className="font-mono break-all">{formatCell(v)}</dd>
                </div>
              ))}
          </dl>
        </aside>
      )}
    </div>
  );
}
