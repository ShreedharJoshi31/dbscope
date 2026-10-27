import { useStore } from "@xyflow/react";
import { createContext, useContext } from "react";

/** Below this zoom table nodes render their header only: column text would be under 6px anyway. */
export const LOD_ZOOM = 0.5;
/** Below this zoom, on large schemas, nodes are a name in a box and edges become one path. */
export const FAR_ZOOM = 0.25;
/** Overview mode is a paint-cost measure for big schemas; small ones keep markers and edge hover. */
export const FAR_MIN_TABLES = 200;

export type ZoomDetail = "full" | "header" | "far";

/** The zoom below which the overview applies; 0 disables it. Provided by the canvas from the table count. */
export const FarZoomContext = createContext(0);

export function farZoomFor(tables: number): number {
  return tables > FAR_MIN_TABLES ? FAR_ZOOM : 0;
}

/**
 * The level of detail for the current zoom. Subscribes to a string derived
 * from the zoom, not the transform, so a node or edge re-renders only when
 * the zoom crosses a threshold.
 */
export function useZoomDetail(): ZoomDetail {
  const far = useContext(FarZoomContext);
  return useStore((s) => (s.transform[2] >= LOD_ZOOM ? "full" : s.transform[2] >= far ? "header" : "far"));
}

/** True while column rows are worth drawing. */
export function useDetailedZoom(): boolean {
  return useZoomDetail() === "full";
}
