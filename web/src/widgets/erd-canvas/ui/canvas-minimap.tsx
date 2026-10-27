import { type Node, Panel, useReactFlow, useStore } from "@xyflow/react";
import { type PointerEvent, useEffect, useLayoutEffect, useMemo, useRef, useState, type WheelEvent } from "react";
import { onPixelRatioChange, tokenColors } from "@/widgets/erd-canvas/model/canvas-paint";

const WIDTH = 200;
const HEIGHT = 150;
const PAD = 6;

interface Fit {
  minX: number;
  minY: number;
  scale: number;
  /** Offsets that centre the tables in the minimap. */
  ox: number;
  oy: number;
}

function fitNodes(nodes: readonly Node[]): Fit {
  let minX = Number.POSITIVE_INFINITY;
  let minY = Number.POSITIVE_INFINITY;
  let maxX = Number.NEGATIVE_INFINITY;
  let maxY = Number.NEGATIVE_INFINITY;
  for (const n of nodes) {
    const w = n.measured?.width ?? n.width ?? 0;
    const h = n.measured?.height ?? n.height ?? 0;
    minX = Math.min(minX, n.position.x);
    minY = Math.min(minY, n.position.y);
    maxX = Math.max(maxX, n.position.x + w);
    maxY = Math.max(maxY, n.position.y + h);
  }
  if (!Number.isFinite(minX)) return { minX: 0, minY: 0, scale: 1, ox: PAD, oy: PAD };
  const bw = Math.max(1, maxX - minX);
  const bh = Math.max(1, maxY - minY);
  const scale = Math.min((WIDTH - 2 * PAD) / bw, (HEIGHT - 2 * PAD) / bh);
  return { minX, minY, scale, ox: (WIDTH - bw * scale) / 2, oy: (HEIGHT - bh * scale) / 2 };
}

/** A canvas sized for the screen's pixel density, in CSS pixels. */
function context(canvas: HTMLCanvasElement | null): CanvasRenderingContext2D | null {
  if (!canvas) return null;
  const dpr = window.devicePixelRatio || 1;
  const w = Math.round(WIDTH * dpr);
  const h = Math.round(HEIGHT * dpr);
  if (canvas.width !== w || canvas.height !== h) {
    canvas.width = w;
    canvas.height = h;
  }
  const ctx = canvas.getContext("2d");
  ctx?.setTransform(w / WIDTH, 0, 0, h / HEIGHT, 0, 0);
  return ctx;
}

const COLORS = ["--surface-subtle", "--border-control", "--overlay-backdrop", "--border-focus"] as const;

/**
 * The minimap, drawn on two canvases instead of React Flow's SVG one: the
 * tables are redrawn only when the nodes change (a layout, a drag), and a pan
 * or zoom redraws only the viewport rectangle. React Flow's minimap re-renders one SVG rectangle per
 * table on every viewport change, which at 1,500 tables made each pan frame
 * cost three times as much (docs/ARCHITECTURE.md, "Canvas performance").
 */
export function CanvasMinimap({ theme }: { theme: string }) {
  const nodes = useStore((s) => s.nodes);
  const transform = useStore((s) => s.transform);
  const width = useStore((s) => s.width);
  const height = useStore((s) => s.height);
  const minZoom = useStore((s) => s.minZoom);
  const maxZoom = useStore((s) => s.maxZoom);
  const { setViewport } = useReactFlow();
  const base = useRef<HTMLCanvasElement>(null);
  const top = useRef<HTMLCanvasElement>(null);
  const fit = useMemo(() => fitNodes(nodes), [nodes]);
  const [pixelRatio, setPixelRatio] = useState(() => window.devicePixelRatio);
  useEffect(() => onPixelRatioChange(() => setPixelRatio(window.devicePixelRatio)), []);
  const [colors, setColors] = useState<Record<(typeof COLORS)[number], string> | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: theme changes what the tokens resolve to.
  useLayoutEffect(() => {
    if (base.current) setColors(tokenColors(base.current, COLORS));
  }, [theme]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: a new pixel ratio needs a sharper redraw.
  useEffect(() => {
    const ctx = context(base.current);
    if (!ctx || !colors) return;
    ctx.fillStyle = colors["--surface-subtle"];
    ctx.fillRect(0, 0, WIDTH, HEIGHT);
    ctx.fillStyle = colors["--border-control"];
    for (const n of nodes) {
      const w = (n.measured?.width ?? n.width ?? 0) * fit.scale;
      const h = (n.measured?.height ?? n.height ?? 0) * fit.scale;
      ctx.fillRect(
        fit.ox + (n.position.x - fit.minX) * fit.scale,
        fit.oy + (n.position.y - fit.minY) * fit.scale,
        Math.max(1, w),
        Math.max(1, h),
      );
    }
  }, [nodes, fit, colors, pixelRatio]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: a new pixel ratio needs a sharper redraw.
  useEffect(() => {
    const ctx = context(top.current);
    if (!ctx || !colors) return;
    const [tx, ty, zoom] = transform;
    const x = fit.ox + (-tx / zoom - fit.minX) * fit.scale;
    const y = fit.oy + (-ty / zoom - fit.minY) * fit.scale;
    const w = (width / zoom) * fit.scale;
    const h = (height / zoom) * fit.scale;
    ctx.clearRect(0, 0, WIDTH, HEIGHT);
    ctx.fillStyle = colors["--overlay-backdrop"];
    ctx.fillRect(0, 0, WIDTH, HEIGHT);
    ctx.clearRect(x, y, w, h);
    ctx.strokeStyle = colors["--border-focus"];
    ctx.lineWidth = 1;
    ctx.strokeRect(x + 0.5, y + 0.5, Math.max(0, w - 1), Math.max(0, h - 1));
  }, [transform, width, height, fit, colors, pixelRatio]);

  /** Centres the canvas on the flow point under a minimap position. */
  const centreAt = (event: PointerEvent<HTMLDivElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    const fx = (event.clientX - rect.left - fit.ox) / fit.scale + fit.minX;
    const fy = (event.clientY - rect.top - fit.oy) / fit.scale + fit.minY;
    const zoom = transform[2];
    void setViewport({ x: width / 2 - fx * zoom, y: height / 2 - fy * zoom, zoom });
  };
  const onWheel = (event: WheelEvent<HTMLDivElement>) => {
    if (!event.deltaY) return; // a sideways swipe
    const [tx, ty, zoom] = transform;
    // Proportional to the scroll, so a trackpad's many small events zoom smoothly.
    const next = Math.min(maxZoom, Math.max(minZoom, zoom * Math.exp(-event.deltaY * 0.002)));
    const cx = (width / 2 - tx) / zoom;
    const cy = (height / 2 - ty) / zoom;
    void setViewport({ x: width / 2 - cx * next, y: height / 2 - cy * next, zoom: next });
  };

  return (
    <Panel position="bottom-right">
      <div
        data-testid="erd-minimap"
        role="img"
        aria-label="Minimap of the ERD: click or drag to move the canvas"
        className="erd-minimap relative cursor-pointer"
        style={{ width: WIDTH, height: HEIGHT }}
        onPointerDown={(e) => {
          if (e.button !== 0) return;
          e.currentTarget.setPointerCapture(e.pointerId);
          centreAt(e);
        }}
        onPointerMove={(e) => {
          if (e.buttons === 1) centreAt(e);
        }}
        onWheel={onWheel}
      >
        <canvas ref={base} aria-hidden className="absolute inset-0" style={{ width: WIDTH, height: HEIGHT }} />
        <canvas ref={top} aria-hidden className="absolute inset-0" style={{ width: WIDTH, height: HEIGHT }} />
      </div>
    </Panel>
  );
}
