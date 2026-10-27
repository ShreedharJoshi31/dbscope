/**
 * Crow's-foot markers, defined once per canvas. Paths run child -> parent, so
 * `markerStart` sits on the child (many) end and `markerEnd` on the parent
 * (one) end; orient="auto-start-reverse" lets one shape serve both ends. Each
 * shape exists in a muted and an active tone because SVG markers do not
 * inherit the stroke colour of the path they decorate.
 */
export type MarkerShape = "many" | "one" | "zero-one";
export type MarkerTone = "base" | "active";

export function markerUrl(shape: MarkerShape, tone: MarkerTone): string {
  return `url(#erd-${shape}-${tone})`;
}

/** +x points into the table at both ends, so shapes are drawn at negative x, on the line. */
const SHAPES: Record<Exclude<MarkerShape, "zero-one">, string> = {
  // Crow's foot: three toes spreading onto the table, "many".
  many: "M-12,0 L0,-6 M-12,0 L0,0 M-12,0 L0,6",
  // Two bars across the line: "exactly one".
  one: "M-4,-6 L-4,6 M-8,-6 L-8,6",
};

const TONE_CLASS: Record<MarkerTone, string> = {
  base: "stroke-(--erd-edge-stroke)",
  active: "stroke-accent-fg",
};

function Marker({ shape, tone }: { shape: MarkerShape; tone: MarkerTone }) {
  return (
    <marker
      id={`erd-${shape}-${tone}`}
      viewBox="-16 -8 18 16"
      refX={0}
      refY={0}
      markerWidth={18}
      markerHeight={16}
      markerUnits="userSpaceOnUse"
      orient="auto-start-reverse"
    >
      {shape === "zero-one" ? (
        <g className={TONE_CLASS[tone]} fill="none" strokeWidth={1.25}>
          <path d="M-4,-6 L-4,6" />
          <circle cx={-11} cy={0} r={3.5} className="fill-surface-canvas" />
        </g>
      ) : (
        <path d={SHAPES[shape]} fill="none" strokeWidth={1.25} className={TONE_CLASS[tone]} />
      )}
    </marker>
  );
}

export function EdgeMarkers() {
  return (
    <svg aria-hidden className="pointer-events-none absolute size-0" focusable="false">
      <defs>
        {(["many", "one", "zero-one"] as const).map((shape) =>
          (["base", "active"] as const).map((tone) => <Marker key={`${shape}-${tone}`} shape={shape} tone={tone} />),
        )}
      </defs>
    </svg>
  );
}
