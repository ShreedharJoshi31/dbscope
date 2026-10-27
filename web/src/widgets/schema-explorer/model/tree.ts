import { type ColumnRow, relationKey, type SchemaGraph, visibleColumns } from "@/entities/schema";
import type { Relation } from "@/shared/api";

export type TreeRow =
  | { id: string; level: 1; type: "schema"; schema: string; count: number; expanded: boolean }
  | { id: string; level: 2; type: "relation"; relation: Relation; key: string; expanded: boolean; hasChildren: boolean }
  | { id: string; level: 3; type: "column"; relationKey: string; row: ColumnRow };

export interface TreeRowPosition {
  setSize: number;
  posInSet: number;
  parentId: string | null;
}

export const schemaRowId = (schema: string) => `s:${schema}`;
export const relationRowId = (key: string) => `r:${key}`;
export const columnRowId = (key: string, column: string) => `c:${key}.${column}`;

function matches(text: string, q: string): boolean {
  return text.toLowerCase().includes(q);
}

/**
 * The visible rows of the explorer tree, in order: schemas, their relations,
 * and the columns of expanded relations. A filter keeps relations whose name
 * matches or that have a matching column, expands everything on the way to a
 * match, and shows only the matching columns.
 */
export function flattenTree(graph: SchemaGraph, expanded: ReadonlySet<string>, filter: string): TreeRow[] {
  const q = filter.trim().toLowerCase();
  const rows: TreeRow[] = [];
  const bySchema = new Map<string, Relation[]>();
  for (const r of graph.relations) {
    const list = bySchema.get(r.schema);
    if (list) list.push(r);
    else bySchema.set(r.schema, [r]);
  }

  for (const schema of graph.schemas) {
    const relations = bySchema.get(schema) ?? [];
    const kept: { relation: Relation; columns: ColumnRow[]; nameHit: boolean }[] = [];
    for (const relation of relations) {
      const columns = visibleColumns(relation, false);
      if (!q) {
        kept.push({ relation, columns, nameHit: false });
        continue;
      }
      const nameHit = matches(relationKey(relation), q);
      const hits = columns.filter((c) => matches(c.column.name, q));
      if (nameHit || hits.length > 0)
        kept.push({ relation, columns: nameHit && hits.length === 0 ? columns : hits, nameHit });
    }
    if (q && kept.length === 0) continue;

    const schemaId = schemaRowId(schema);
    const schemaOpen = q ? true : expanded.has(schemaId);
    rows.push({ id: schemaId, level: 1, type: "schema", schema, count: relations.length, expanded: schemaOpen });
    if (!schemaOpen) continue;

    for (const { relation, columns, nameHit } of kept) {
      const key = relationKey(relation);
      const id = relationRowId(key);
      const columnHit = Boolean(q) && !nameHit;
      const open = columnHit || expanded.has(id);
      rows.push({ id, level: 2, type: "relation", relation, key, expanded: open, hasChildren: columns.length > 0 });
      if (!open) continue;
      for (const row of columns) {
        rows.push({ id: columnRowId(key, row.column.name), level: 3, type: "column", relationKey: key, row });
      }
    }
  }
  return rows;
}

/** aria-setsize / aria-posinset / parent for each visible row. */
export function rowPositions(rows: readonly TreeRow[]): Map<string, TreeRowPosition> {
  const out = new Map<string, TreeRowPosition>();
  const stack: { id: string; level: number }[] = [];
  const siblings = new Map<string, string[]>();
  for (const row of rows) {
    while (stack.length > 0 && (stack[stack.length - 1]?.level ?? 0) >= row.level) stack.pop();
    const parentId = stack[stack.length - 1]?.id ?? null;
    const group = parentId ?? "";
    const list = siblings.get(group) ?? [];
    list.push(row.id);
    siblings.set(group, list);
    out.set(row.id, { setSize: 0, posInSet: list.length, parentId });
    stack.push({ id: row.id, level: row.level });
  }
  for (const list of siblings.values()) {
    for (const id of list) {
      const p = out.get(id);
      if (p) p.setSize = list.length;
    }
  }
  return out;
}
