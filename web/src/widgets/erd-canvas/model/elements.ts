import type { Edge, XYPosition } from "@xyflow/react";
import {
  anchorHandle,
  type FkEdge,
  nodeLayout,
  pairEdgeId,
  relationKey,
  type SchemaGraph,
  type TableNodeType,
} from "@/entities/schema";

export interface RelationshipEdgeData extends Record<string, unknown> {
  fkId: string;
  child: string;
  parent: string;
  pairIndex: number;
  pairCount: number;
  /** Index of this foreign key among those joining the same two tables, for fanning out. */
  lane: number;
  laneCount: number;
  inferred: boolean;
  accepted: boolean;
  confidence: number | undefined;
  optional: boolean;
  oneToOne: boolean;
  selfRef: boolean;
  childWidth: number;
  parentWidth: number;
}

export type RelationshipEdgeType = Edge<RelationshipEdgeData, "relationship">;

export interface EdgeOptions {
  collapsed: ReadonlySet<string>;
  /** Unreviewed inferred keys; accepted ones always show, they count as real foreign keys. */
  showInferred: boolean;
}

function visibleFk(edge: FkEdge, showInferred: boolean): boolean {
  return showInferred || !edge.inferred || edge.accepted;
}

/**
 * One React Flow edge per column pair. Edges run from the child (FK) column's
 * right handle to the parent (referenced) column's left handle; the edge
 * component re-picks sides from the actual positions. Foreign keys between the
 * same two tables get lanes so parallel edges fan out instead of overlapping.
 */
export function buildEdges(graph: SchemaGraph, { collapsed, showInferred }: EdgeOptions): RelationshipEdgeType[] {
  const fks = graph.edges.filter((e) => visibleFk(e, showInferred));
  const lanes = new Map<string, FkEdge[]>();
  for (const e of fks) {
    const pair = e.child < e.parent ? `${e.child}\n${e.parent}` : `${e.parent}\n${e.child}`;
    const list = lanes.get(pair);
    if (list) list.push(e);
    else lanes.set(pair, [e]);
  }

  const out: RelationshipEdgeType[] = [];
  for (const group of lanes.values()) {
    group.forEach((e, lane) => {
      const childRel = graph.byKey.get(e.child);
      const parentRel = graph.byKey.get(e.parent);
      if (!childRel || !parentRel) return;
      const childLayout = nodeLayout(childRel, collapsed.has(e.child));
      const parentLayout = nodeLayout(parentRel, collapsed.has(e.parent));
      e.pairs.forEach((pair, pairIndex) => {
        out.push({
          id: pairEdgeId(e.id, pairIndex),
          type: "relationship",
          source: e.child,
          target: e.parent,
          sourceHandle: anchorHandle(childLayout, pair.column, "R"),
          targetHandle: anchorHandle(parentLayout, pair.refColumn, "L"),
          data: {
            fkId: e.id,
            child: e.child,
            parent: e.parent,
            pairIndex,
            pairCount: e.pairs.length,
            lane,
            laneCount: group.length,
            inferred: e.inferred,
            accepted: e.accepted,
            confidence: e.confidence,
            optional: e.optional,
            oneToOne: e.oneToOne,
            selfRef: e.selfRef,
            childWidth: childLayout.width,
            parentWidth: parentLayout.width,
          },
        });
      });
    });
  }
  return out;
}

/** Handle ids each node's edges attach to. */
export function usedHandles(edges: readonly RelationshipEdgeType[]): Map<string, ReadonlySet<string>> {
  const out = new Map<string, Set<string>>();
  const add = (node: string, handle: string | null | undefined) => {
    if (!handle) return;
    let set = out.get(node);
    if (!set) {
      set = new Set();
      out.set(node, set);
    }
    set.add(handle);
  };
  for (const e of edges) {
    add(e.source, e.sourceHandle);
    add(e.target, e.targetHandle);
  }
  return out;
}

const NO_ANCHORS: ReadonlySet<string> = new Set();

function sameSet(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  if (a === b) return true;
  if (a.size !== b.size) return false;
  for (const x of a) if (!b.has(x)) return false;
  return true;
}

/**
 * Table nodes with explicit size, `measured` and precomputed handles, so React
 * Flow neither measures the DOM nor waits for a ResizeObserver. A previous
 * node object is reused when nothing about it changed, keeping React Flow's
 * per-node memoization effective.
 */
export function buildNodes(
  graph: SchemaGraph,
  positions: ReadonlyMap<string, XYPosition>,
  collapsed: ReadonlySet<string>,
  previous?: ReadonlyMap<string, TableNodeType>,
  anchors?: ReadonlyMap<string, ReadonlySet<string>>,
): TableNodeType[] {
  return graph.relations.map((relation) => {
    const id = relationKey(relation);
    const isCollapsed = collapsed.has(id);
    const layout = nodeLayout(relation, isCollapsed);
    const position = positions.get(id) ?? { x: 0, y: 0 };
    const prev = previous?.get(id);
    const nodeAnchors = anchors?.get(id) ?? NO_ANCHORS;
    if (
      prev &&
      prev.data.relation === relation &&
      prev.data.layout === layout &&
      sameSet(prev.data.anchors, nodeAnchors) &&
      prev.position.x === position.x &&
      prev.position.y === position.y
    ) {
      return prev;
    }
    return {
      id,
      type: relation.stub ? "stub" : "table",
      position,
      width: layout.width,
      height: layout.height,
      measured: { width: layout.width, height: layout.height },
      handles: layout.handles,
      data: { relation, layout, collapsed: isCollapsed, anchors: nodeAnchors },
    };
  });
}

/** Nodes sized for layout: what the workers need, nothing more. */
export interface LayoutNode {
  id: string;
  width: number;
  height: number;
}

export function layoutNodes(graph: SchemaGraph, collapsed: ReadonlySet<string>): LayoutNode[] {
  return graph.relations.map((r) => {
    const id = relationKey(r);
    const { width, height } = nodeLayout(r, collapsed.has(id));
    return { id, width, height };
  });
}
