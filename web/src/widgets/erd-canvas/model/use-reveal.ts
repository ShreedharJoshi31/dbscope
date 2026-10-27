import { useReactFlow, type XYPosition } from "@xyflow/react";
import { type RefObject, useEffect } from "react";
import { columnOffsetY, nodeLayout, useCanvasStore } from "@/entities/schema";
import { motionDuration } from "@/shared/lib/reduced-motion";

/** Zoom used when revealing a column: high enough that rows render (above the LOD threshold). */
const READABLE_ZOOM = 1;

/**
 * Pans to the node (and column) named by the store's reveal request, from the
 * explorer, search or keyboard. Uses an animated pan unless the user prefers
 * reduced motion.
 */
export function useReveal(positions: RefObject<Map<string, XYPosition>>, collapsed: ReadonlySet<string>): void {
  const { setCenter, getZoom } = useReactFlow();
  const reveal = useCanvasStore((s) => s.reveal);

  useEffect(() => {
    if (!reveal) return;
    const { graph } = useCanvasStore.getState();
    const relation = graph?.byKey.get(reveal.id);
    const position = positions.current?.get(reveal.id);
    if (!relation || !position) return;
    const layout = nodeLayout(relation, collapsed.has(reveal.id));
    // A tall table is centred near its header so its name stays on screen.
    const y = reveal.column ? columnOffsetY(layout, reveal.column) : Math.min(layout.height / 2, 160);
    const zoom = reveal.column ? Math.max(getZoom(), READABLE_ZOOM) : Math.max(getZoom(), 0.6);
    void setCenter(position.x + layout.width / 2, position.y + y, {
      zoom,
      duration: motionDuration(300),
    });
  }, [reveal, positions, collapsed, setCenter, getZoom]);
}
