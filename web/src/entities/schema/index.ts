export {
  type CanvasCommandId,
  runCanvasCommand,
  useCanvasCommands,
} from "@/entities/schema/model/canvas-commands";
export {
  type CanvasState,
  type ColumnMark,
  type DiffChange,
  type DiffImpact,
  type DiffOverlay,
  derive,
  edgeVisual,
  type FocusHops,
  flowEdgesFor,
  type HeatState,
  MAX_FLOW_EDGES,
  type NodeMark,
  nodeVisual,
  type OverlayEntry,
  type OverlayTone,
  type RevealRequest,
  useCanvasStore,
} from "@/entities/schema/model/canvas-store";
export {
  clearMigrationOverlay,
  DIFF_CHANGE_META,
  type DiffChangeMeta,
  hasMigrationOverlay,
  partitionDiffRelations,
  showMigrationDiff,
} from "@/entities/schema/model/diff-overlay";
export {
  type FindingRow,
  findingCount,
  schemaFindings,
  worstLevel,
} from "@/entities/schema/model/findings";
export {
  buildSchemaGraph,
  type ColumnFlags,
  type ColumnPair,
  columnFlags,
  type FkColumnSets,
  type FkEdge,
  fkColumnSets,
  getSchemaGraph,
  neighboursWithin,
  relationByKey,
  relationshipChain,
  type SchemaGraph,
} from "@/entities/schema/model/graph";
export {
  clearCanvasOverlay,
  describeScan,
  hasCanvasOverlay,
  heatmapFromOverlay,
  highlightTables,
  showQueryOverlay,
} from "@/entities/schema/model/heatmap";
export {
  fkIdOfPairEdge,
  type HandleSide,
  HEADER_HANDLE,
  handleId,
  pairEdgeId,
  relationKey,
} from "@/entities/schema/model/keys";
export { type SearchMatch, searchGraph } from "@/entities/schema/model/search";
export {
  anchorHandle,
  type ColumnRow,
  columnOffsetY,
  FOOTER_ROW_HEIGHT,
  type FooterRow,
  type HandleSpec,
  HEADER_HEIGHT,
  NODE_WIDTH,
  type NodeLayout,
  nodeLayout,
  nodeSize,
  ROW_HEIGHT,
  STUB_HEIGHT,
  STUB_WIDTH,
  visibleColumns,
} from "@/entities/schema/model/sizing";
export {
  closeSampleWorkspace,
  openSampleWorkspace,
  type SchemaSource,
  useWorkspaceStore,
} from "@/entities/schema/model/workspace-store";
export { BadgeList, columnBadges, KeyIcon } from "@/entities/schema/ui/column-badges";
export { KIND_META, KindIcon } from "@/entities/schema/ui/kind-icon";
export { rowEstimateLabel } from "@/entities/schema/ui/row-estimate";
export type { TableNodeData, TableNodeType } from "@/entities/schema/ui/table-node";
export type { TableNodeActions } from "@/entities/schema/ui/table-node-actions";
