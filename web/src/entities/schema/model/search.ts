import type { SchemaGraph } from "@/entities/schema/model/graph";
import { relationKey } from "@/entities/schema/model/keys";

export interface SearchMatch {
  /** Relation key. */
  nodeId: string;
  /** Set for a column match. */
  column?: string;
}

/** Lower ranks sort first: exact name, name prefix, name contains, then column matches. */
function tableRank(name: string, qualified: string, q: string): number {
  if (name === q || qualified === q) return 0;
  if (name.startsWith(q) || qualified.startsWith(q)) return 1;
  if (name.includes(q) || qualified.includes(q)) return 2;
  return -1;
}

/**
 * Finds tables and columns for the canvas search box. Case-insensitive;
 * "table.column" narrows to a column of matching tables. Results are ordered
 * by table relevance, then model order, with column matches after tables.
 */
export function searchGraph(graph: SchemaGraph, query: string): SearchMatch[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  const dot = q.lastIndexOf(".");
  const tableHits: { rank: number; index: number; match: SearchMatch }[] = [];
  const columnHits: SearchMatch[] = [];

  graph.relations.forEach((relation, index) => {
    const key = relationKey(relation);
    const name = relation.name.toLowerCase();
    const qualified = key.toLowerCase();
    const rank = tableRank(name, qualified, q);
    if (rank >= 0) tableHits.push({ rank, index, match: { nodeId: key } });

    // "orders.cust" -> columns of tables named like "orders" containing "cust".
    const tablePart = dot > 0 ? q.slice(0, dot) : null;
    const columnPart = dot > 0 ? q.slice(dot + 1) : q;
    if (tablePart !== null && !name.includes(tablePart) && !qualified.includes(tablePart)) return;
    if (!columnPart) return;
    for (const column of relation.columns ?? []) {
      if (column.name.toLowerCase().includes(columnPart)) columnHits.push({ nodeId: key, column: column.name });
    }
  });

  tableHits.sort((a, b) => a.rank - b.rank || a.index - b.index);
  return [...tableHits.map((h) => h.match), ...columnHits];
}
