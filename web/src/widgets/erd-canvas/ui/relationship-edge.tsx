import { BaseEdge, EdgeLabelRenderer, type EdgeProps } from "@xyflow/react";
import { cn } from "cn";
import { memo } from "react";
import { edgeVisual, useCanvasStore } from "@/entities/schema";
import { useZoomDetail } from "@/entities/schema/canvas";
import type { RelationshipEdgeType } from "@/widgets/erd-canvas/model/elements";
import { routeEdge } from "@/widgets/erd-canvas/model/route";
import { markerUrl } from "@/widgets/erd-canvas/ui/edge-markers";
import { InferredEdgeLabel } from "@/widgets/erd-canvas/ui/inferred-edge-label";

function RelationshipEdgeView({ id, data, sourceX, sourceY, targetX, targetY }: EdgeProps<RelationshipEdgeType>) {
  const visual = useCanvasStore((s) => (data ? edgeVisual(s, data.fkId, data.child, data.parent) : ""));
  // At overview zoom the markers are a few pixels wide and cost most of the paint time; drop them.
  const far = useZoomDetail() === "far";
  if (!data) return null;

  const route = routeEdge({ ...data, sourceX, sourceY, targetX, targetY });
  const tone = visual === "active" ? "active" : "base";
  const unreviewed = data.inferred && !data.accepted;
  const childShape = data.oneToOne ? "one" : "many";
  const parentShape = data.optional ? "zero-one" : "one";
  // Composite keys draw one path per column pair; only the first carries the label.
  const showLabel = data.inferred && data.pairIndex === 0 && !far;

  return (
    <>
      <BaseEdge
        id={id}
        path={route.path}
        interactionWidth={14}
        markerStart={far ? undefined : markerUrl(childShape, tone)}
        markerEnd={far ? undefined : markerUrl(parentShape, tone)}
        className={cn(
          "erd-edge",
          visual === "active" && "erd-edge-active",
          visual === "dim" && "erd-edge-dim",
          unreviewed && "erd-edge-inferred",
          data.accepted && "erd-edge-accepted",
        )}
      />
      {showLabel && (
        <EdgeLabelRenderer>
          <InferredEdgeLabel fkId={data.fkId} x={route.labelX} y={route.labelY} dim={visual === "dim"} />
        </EdgeLabelRenderer>
      )}
    </>
  );
}

/** Declared at module level and memoized, per React Flow's performance guidance. */
export const RelationshipEdge = memo(RelationshipEdgeView);
