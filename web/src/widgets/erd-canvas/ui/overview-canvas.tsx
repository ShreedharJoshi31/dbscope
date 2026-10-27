import { useStoreApi } from "@xyflow/react";
import { memo, useLayoutEffect, useMemo, useRef } from "react";
import { type CanvasState, nodeVisual, type TableNodeType, useCanvasStore } from "@/entities/schema";
import { onPixelRatioChange, tokenColors } from "@/widgets/erd-canvas/model/canvas-paint";
import type { RelationshipEdgeType } from "@/widgets/erd-canvas/model/elements";
import { farEdgesPath } from "@/widgets/erd-canvas/ui/far-edges";

/** Schemas above this many tables draw the overview zoom on a canvas instead of as cards. */
export const CANVAS_OVERVIEW_MIN_TABLES = 2000;

const TOKENS = [
  "--surface-subtle",
  "--border-control",
  "--erd-edge-stroke",
  "--accent-fg",
  "--status-info-fg",
  "--status-success-fg",
  "--status-warn-fg",
  "--status-danger-fg",
] as const;
type Token = (typeof TOKENS)[number];

/** How a table is drawn. Marks differ in width and dash, never only in colour. */
interface Look {
  stroke: string;
  width: number;
  dash: number[];
  alpha: number;
  /** Drawn after (above) looks with a lower rank; the hit test follows the same order. */
  rank: number;
}

const MARK_LOOK: Record<string, { color: Token; width: number; dash: number[] }> = {
  selected: { color: "--accent-fg", width: 4, dash: [] },
  "focus-root": { color: "--accent-fg", width: 4, dash: [] },
  "current-match": { color: "--status-info-fg", width: 3, dash: [] },
  match: { color: "--status-info-fg", width: 2, dash: [6, 4] },
  chain: { color: "--accent-fg", width: 1.5, dash: [] },
  highlight: { color: "--accent-fg", width: 2.5, dash: [] },
  impact: { color: "--accent-fg", width: 2, dash: [2, 3] },
  "impact-heuristic": { color: "--accent-fg", width: 1.5, dash: [2, 5] },
};
const DIFF_LOOK: Record<string, { color: Token; dash: number[] }> = {
  added: { color: "--status-success-fg", dash: [] },
  altered: { color: "--status-warn-fg", dash: [] },
  dropped: { color: "--status-danger-fg", dash: [8, 4] },
};

function lookKey(s: CanvasState, n: TableNodeType): string {
  const [mark = "", dim = ""] = nodeVisual(s, n.id).split("|");
  return `${mark}|${dim}|${s.diff?.relations[n.id] ?? ""}|${n.data.relation.stub ? "stub" : ""}`;
}

function lookOf(key: string, colors: Record<Token, string>): Look {
  const [mark = "", dim, diff = "", stub] = key.split("|");
  const l: Look = { stroke: colors["--border-control"], width: 1, dash: stub ? [4, 4] : [], alpha: 1, rank: 0 };
  const d = DIFF_LOOK[diff];
  if (d) Object.assign(l, { stroke: colors[d.color], width: 3, dash: d.dash, rank: 1 });
  const m = MARK_LOOK[mark];
  if (m) Object.assign(l, { stroke: colors[m.color], width: m.width, dash: m.dash, rank: 2 });
  if (dim === "dim") Object.assign(l, { alpha: 0.25, rank: -1 });
  return l;
}

function rankOf(s: CanvasState, id: string): number {
  const [mark = "", dim] = nodeVisual(s, id).split("|");
  if (dim === "dim") return -1;
  if (mark) return 2;
  return s.diff?.relations[id] ? 1 : 0;
}

/**
 * The table under a flow-coordinate point: the one drawn on top, so marked
 * tables (drawn last) win over plain ones they overlap.
 */
export function tableAt(nodes: readonly TableNodeType[], x: number, y: number): string | null {
  const s = useCanvasStore.getState();
  let best: string | null = null;
  let bestRank = Number.NEGATIVE_INFINITY;
  for (const n of nodes) {
    const inside =
      x >= n.position.x &&
      x <= n.position.x + (n.width ?? 0) &&
      y >= n.position.y &&
      y <= n.position.y + (n.height ?? 0);
    if (!inside) continue;
    const rank = rankOf(s, n.id);
    if (rank >= bestRank) [best, bestRank] = [n.id, rank];
  }
  return best;
}

/**
 * The overview of a very large schema, painted on one canvas: no DOM per
 * table, so zooming 5,000 tables costs a few path fills instead of
 * re-rasterising thousands of cards (docs/ARCHITECTURE.md, "Canvas
 * performance"). React Flow keeps the tables as hidden nodes for fit view and
 * the minimap; the canvas takes no pointer events, so panning and zooming stay
 * React Flow's, and clicks are hit-tested by the caller with tableAt.
 */
function OverviewCanvasView({
  nodes,
  edges,
  theme,
}: {
  nodes: TableNodeType[];
  edges: RelationshipEdgeType[];
  theme: string;
}) {
  const ref = useRef<HTMLCanvasElement>(null);
  const flow = useStoreApi();
  const links = useMemo(() => new Path2D(farEdgesPath(nodes, edges)), [nodes, edges]);

  // A layout effect: the cards are hidden in this same commit, so the first paint must not wait a frame.
  // biome-ignore lint/correctness/useExhaustiveDependencies: theme changes what the tokens resolve to.
  useLayoutEffect(() => {
    const canvas = ref.current;
    if (!canvas) return;
    const colors = tokenColors(canvas, TOKENS);
    const looks = new Map<string, Look>();

    let layers: { look: Look; path: Path2D }[] = [];
    const group = (s: CanvasState) => {
      const paths = new Map<string, Path2D>();
      for (const n of nodes) {
        const key = lookKey(s, n);
        let p = paths.get(key);
        if (!p) {
          p = new Path2D();
          paths.set(key, p);
        }
        p.rect(n.position.x, n.position.y, n.width ?? 0, n.height ?? 0);
      }
      layers = [...paths].map(([key, path]) => {
        let look = looks.get(key);
        if (!look) {
          look = lookOf(key, colors);
          looks.set(key, look);
        }
        return { look, path };
      });
      layers.sort((a, b) => a.look.rank - b.look.rank);
    };

    const draw = () => {
      const { transform, width, height } = flow.getState();
      const dpr = window.devicePixelRatio || 1;
      const w = Math.round(width * dpr);
      const h = Math.round(height * dpr);
      if (canvas.width !== w || canvas.height !== h) {
        canvas.width = w;
        canvas.height = h;
      }
      const ctx = canvas.getContext("2d");
      if (!ctx) return;
      const [tx, ty, zoom] = transform;
      ctx.setTransform(1, 0, 0, 1, 0, 0);
      ctx.clearRect(0, 0, w, h);
      ctx.setTransform(dpr * zoom, 0, 0, dpr * zoom, dpr * tx, dpr * ty);
      ctx.globalAlpha = 1;
      ctx.setLineDash([]);
      ctx.strokeStyle = colors["--erd-edge-stroke"];
      // 1px, unlike FarEdges' 2px: a hairline is rasterised on a fast path, and 10,000 wider
      // strokes made a frame ten times slower.
      ctx.lineWidth = 1 / zoom;
      ctx.stroke(links);
      ctx.fillStyle = colors["--surface-subtle"];
      for (const { look, path } of layers) {
        ctx.globalAlpha = look.alpha;
        ctx.fill(path);
        ctx.strokeStyle = look.stroke;
        ctx.lineWidth = look.width / zoom;
        ctx.setLineDash(look.dash.map((d) => d / zoom));
        ctx.stroke(path);
      }
    };

    let frame = 0;
    const later = () => {
      if (!frame) {
        frame = requestAnimationFrame(() => {
          frame = 0;
          draw();
        });
      }
    };

    group(useCanvasStore.getState());
    draw();
    // A pan or zoom draws at once: transitions move the viewport inside an animation
    // frame, and waiting for the next one would paint a frame behind.
    const offFlow = flow.subscribe((s, prev) => {
      if (s.transform !== prev.transform || s.width !== prev.width || s.height !== prev.height) draw();
    });
    const offCanvas = useCanvasStore.subscribe((s, prev) => {
      if (s.derived !== prev.derived || s.diff !== prev.diff) {
        group(s);
        later();
      }
    });
    const offRatio = onPixelRatioChange(draw);
    return () => {
      offFlow();
      offCanvas();
      offRatio();
      if (frame) cancelAnimationFrame(frame);
    };
  }, [flow, nodes, links, theme]);

  return (
    <canvas
      ref={ref}
      data-testid="erd-overview-canvas"
      aria-hidden
      className="pointer-events-none absolute inset-0 z-[3] size-full"
    />
  );
}

export const OverviewCanvas = memo(OverviewCanvasView);
