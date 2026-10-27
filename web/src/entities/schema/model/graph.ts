import { relationKey } from "@/entities/schema/model/keys";
import type { Column, ForeignKey, Model, Relation } from "@/shared/api";

export interface ColumnPair {
  column: string;
  refColumn: string;
}

/** A foreign key between two relations of the model, declared or inferred. */
export interface FkEdge {
  /** `<child key>|<fk name>`; unique within a model. */
  id: string;
  fk: ForeignKey;
  /** The relation that holds the foreign key (the "many" side). */
  child: string;
  /** The referenced relation (the "one" side). */
  parent: string;
  /** Column pairs in declaration order; composite keys have more than one. */
  pairs: ColumnPair[];
  inferred: boolean;
  /** An inferred key the user accepted: the server returns it with confidence 1. */
  accepted: boolean;
  confidence: number | undefined;
  selfRef: boolean;
  /** Any child column is nullable: the parent side reads "zero or one". */
  optional: boolean;
  /** The child columns are the primary key or a unique key: one-to-one. */
  oneToOne: boolean;
}

export interface SchemaGraph {
  model: Model;
  relations: Relation[];
  byKey: Map<string, Relation>;
  edges: FkEdge[];
  edgeById: Map<string, FkEdge>;
  /** FKs a relation declares, by child key. */
  outgoing: Map<string, FkEdge[]>;
  /** FKs that reference a relation, by parent key. */
  incoming: Map<string, FkEdge[]>;
  /** Selected schemas in model order, then any schema only stubs live in. */
  schemas: string[];
  /** Foreign keys whose referenced relation is missing from the model. */
  danglingCount: number;
}

function sameColumns(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((c) => set.has(c));
}

function isOneToOne(relation: Relation, columns: readonly string[]): boolean {
  if (relation.primaryKey && sameColumns(relation.primaryKey, columns)) return true;
  return (relation.uniques ?? []).some((u) => sameColumns(u, columns));
}

function toEdge(relation: Relation, fk: ForeignKey, child: string, byKey: Map<string, Relation>): FkEdge | null {
  const parent = relationKey(fk.ref);
  if (!byKey.has(parent)) return null;
  const columnsByName = new Map((relation.columns ?? []).map((c): [string, Column] => [c.name, c]));
  const pairs = fk.columns.map((column, i) => ({ column, refColumn: fk.refColumns[i] ?? fk.refColumns[0] ?? "" }));
  const inferred = Boolean(fk.inferred);
  return {
    id: `${child}|${fk.name}`,
    fk,
    child,
    parent,
    pairs,
    inferred,
    accepted: inferred && !isUnreviewed(fk),
    confidence: fk.confidence,
    selfRef: child === parent,
    optional: fk.columns.some((c) => columnsByName.get(c)?.nullable ?? false),
    oneToOne: isOneToOne(relation, fk.columns),
  };
}

function pushTo<K, V>(map: Map<K, V[]>, key: K, value: V): void {
  const list = map.get(key);
  if (list) list.push(value);
  else map.set(key, [value]);
}

export function buildSchemaGraph(model: Model): SchemaGraph {
  const relations = model.relations ?? [];
  const byKey = new Map(relations.map((r): [string, Relation] => [relationKey(r), r]));
  const edges: FkEdge[] = [];
  const outgoing = new Map<string, FkEdge[]>();
  const incoming = new Map<string, FkEdge[]>();
  let danglingCount = 0;

  for (const relation of relations) {
    const child = relationKey(relation);
    for (const fk of relation.foreignKeys ?? []) {
      const edge = toEdge(relation, fk, child, byKey);
      if (!edge) {
        danglingCount++;
        continue;
      }
      edges.push(edge);
      pushTo(outgoing, child, edge);
      pushTo(incoming, edge.parent, edge);
    }
  }

  const schemas = [...(model.schemas ?? [])];
  for (const r of relations) if (!schemas.includes(r.schema)) schemas.push(r.schema);

  return {
    model,
    relations,
    byKey,
    edges,
    edgeById: new Map(edges.map((e): [string, FkEdge] => [e.id, e])),
    outgoing,
    incoming,
    schemas,
    danglingCount,
  };
}

const graphCache = new WeakMap<Model, SchemaGraph>();

/** Builds the graph once per Model object; every consumer of the same snapshot shares it. */
export function getSchemaGraph(model: Model): SchemaGraph {
  let graph = graphCache.get(model);
  if (!graph) {
    graph = buildSchemaGraph(model);
    graphCache.set(model, graph);
  }
  return graph;
}

export function relationByKey(graph: SchemaGraph, key: string): Relation | undefined {
  return graph.byKey.get(key);
}

/** Relations reachable within `hops` foreign-key steps in either direction, including the start. */
export function neighboursWithin(graph: SchemaGraph, start: string, hops: number): Set<string> {
  const seen = new Set<string>();
  if (!graph.byKey.has(start)) return seen;
  seen.add(start);
  let frontier = [start];
  for (let depth = 0; depth < hops && frontier.length > 0; depth++) {
    const next: string[] = [];
    const visit = (k: string) => {
      if (seen.has(k)) return;
      seen.add(k);
      next.push(k);
    };
    for (const key of frontier) {
      for (const e of graph.outgoing.get(key) ?? []) visit(e.parent);
      for (const e of graph.incoming.get(key) ?? []) visit(e.child);
    }
    frontier = next;
  }
  return seen;
}

/**
 * The chain lit up when a relationship is hovered or selected: the child, the
 * parent, the parent's own parents and the child's own children, each up to
 * `depth` further steps along the foreign-key direction.
 */
export function relationshipChain(graph: SchemaGraph, edge: FkEdge, depth = 2): Set<string> {
  const chain = new Set([edge.child, edge.parent]);
  const walk = (start: string, step: (key: string) => string[]) => {
    let frontier = [start];
    for (let d = 0; d < depth && frontier.length > 0; d++) {
      const next: string[] = [];
      for (const key of frontier) {
        for (const k of step(key)) {
          if (chain.has(k)) continue;
          chain.add(k);
          next.push(k);
        }
      }
      frontier = next;
    }
  };
  walk(edge.parent, (k) => (graph.outgoing.get(k) ?? []).map((e) => e.parent));
  walk(edge.child, (k) => (graph.incoming.get(k) ?? []).map((e) => e.child));
  return chain;
}

/** Column flags a table node and the explorer both show. */
export interface ColumnFlags {
  pk: boolean;
  /** Part of a declared foreign key, or of an inferred one the user accepted. */
  fk: boolean;
  /** Part of an inferred foreign key nobody has reviewed yet. */
  inferredFk: boolean;
  unique: boolean;
  nullable: boolean;
  identity: boolean;
  generated: boolean;
}

export interface FkColumnSets {
  confirmed: ReadonlySet<string>;
  inferred: ReadonlySet<string>;
}

function isUnreviewed(fk: ForeignKey): boolean {
  return Boolean(fk.inferred) && fk.confidence !== 1;
}

/** Columns that take part in the relation's foreign keys, split by whether the key is confirmed. */
export function fkColumnSets(relation: Relation): FkColumnSets {
  const confirmed = new Set<string>();
  const inferred = new Set<string>();
  for (const fk of relation.foreignKeys ?? []) {
    for (const c of fk.columns) (isUnreviewed(fk) ? inferred : confirmed).add(c);
  }
  for (const c of confirmed) inferred.delete(c);
  return { confirmed, inferred };
}

export function columnFlags(relation: Relation, column: Column, sets: FkColumnSets): ColumnFlags {
  return {
    pk: relation.primaryKey?.includes(column.name) ?? false,
    fk: sets.confirmed.has(column.name),
    inferredFk: sets.inferred.has(column.name),
    unique: (relation.uniques ?? []).some((u) => u.length === 1 && u[0] === column.name),
    nullable: column.nullable,
    identity: Boolean(column.identity),
    generated: Boolean(column.generated),
  };
}
