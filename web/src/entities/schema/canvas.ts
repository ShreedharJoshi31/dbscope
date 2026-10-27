/**
 * Second public entry of the schema slice: the React Flow node components.
 * Kept out of index.ts so the shell bundle does not pull in React Flow; only
 * the lazily loaded ERD canvas imports this file.
 */
export {
  FAR_MIN_TABLES,
  FAR_ZOOM,
  FarZoomContext,
  farZoomFor,
  LOD_ZOOM,
  useDetailedZoom,
  useZoomDetail,
  type ZoomDetail,
} from "@/entities/schema/ui/lod";
export { StubNode, TableNode, type TableNodeData, type TableNodeType } from "@/entities/schema/ui/table-node";
export { type TableNodeActions, TableNodeActionsContext } from "@/entities/schema/ui/table-node-actions";
