import type { QualifiedName } from "@/shared/api";

/** A relation's id everywhere in the UI (node id, layout key entry, store key): "schema.name". */
export function relationKey(q: QualifiedName): string {
  return `${q.schema}.${q.name}`;
}

export type HandleSide = "L" | "R";

/** Handles sit on column rows; HEADER_HANDLE is used for stubs and for columns hidden by collapsing. */
export const HEADER_HANDLE = "__header";

export function handleId(column: string, side: HandleSide): string {
  return `${column}:${side}`;
}

/** One React Flow edge per column pair of a foreign key. */
export function pairEdgeId(fkEdgeId: string, pairIndex: number): string {
  return `${fkEdgeId}#${pairIndex}`;
}

/** The foreign key group a per-pair React Flow edge belongs to. */
export function fkIdOfPairEdge(edgeId: string): string {
  const hash = edgeId.lastIndexOf("#");
  return hash === -1 ? edgeId : edgeId.slice(0, hash);
}
