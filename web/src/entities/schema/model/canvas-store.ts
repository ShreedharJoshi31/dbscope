import { create } from "zustand";
import { type FkEdge, neighboursWithin, relationshipChain, type SchemaGraph } from "@/entities/schema/model/graph";
import { type SearchMatch, searchGraph } from "@/entities/schema/model/search";

/**
 * Canvas selection, highlight and overlay state, keyed by node id (relation
 * key) and foreign-key id. It lives outside React Flow's nodes/edges arrays:
 * every node and edge selects only its own entry, so restyling (hover, search,
 * focus, a Phase 2 heatmap, Phase 4 migration colours) re-renders the affected
 * elements and never rebuilds the arrays or triggers a relayout.
 */

export type OverlayTone = "info" | "success" | "warn" | "danger";

/** Written by later phases. `label` is required: status is never shown by colour alone. */
export interface OverlayEntry {
  tone: OverlayTone;
  label: string;
  /** 0..1, for heatmaps. */
  intensity?: number;
  /** Tooltip lines, e.g. the plan's reasons and scans. */
  details?: readonly string[];
}

/** A query overlay's extra state: joined relation pairs get animated edges; others are dimmed. */
export interface HeatState {
  joins: readonly { from: string; to: string }[];
  /** Relations the query touched; everything else is dimmed. */
  involved: ReadonlySet<string>;
}

/** Joined foreign keys animated at most; more would be noise and cost paint time. */
export const MAX_FLOW_EDGES = 20;

export type FocusHops = 1 | 2 | 3;
export type NodeMark = "selected" | "current-match" | "match" | "chain" | "focus-root" | "highlight";
export type ColumnMark = "current-match" | "match" | "endpoint";

export interface RevealRequest {
  id: string;
  column?: string | undefined;
  nonce: number;
}

interface Derived {
  nodeMarks: ReadonlyMap<string, NodeMark>;
  columnMarks: ReadonlyMap<string, ReadonlyMap<string, ColumnMark>>;
  /** When set, nodes outside it are dimmed (focus mode, or a search with matches). */
  keep: ReadonlySet<string> | null;
  /** The hovered or selected foreign key. */
  activeEdge: string | null;
  /** Foreign keys joined by the last query plan, drawn with moving particles. */
  flowEdges: ReadonlySet<string>;
}

export interface CanvasState {
  graph: SchemaGraph | null;
  selectedId: string | null;
  hoverEdge: string | null;
  selectedEdge: string | null;
  focus: { root: string; hops: FocusHops } | null;
  focusHops: FocusHops;
  search: { query: string; matches: SearchMatch[]; index: number };
  overlay: Readonly<Record<string, OverlayEntry>>;
  heat: HeatState | null;
  /** Tables an assistant answer named ("N tables" badge); others are dimmed. */
  highlight: ReadonlySet<string> | null;
  reveal: RevealRequest | null;
  detailsOpen: boolean;
  derived: Derived;

  setGraph: (graph: SchemaGraph | null) => void;
  select: (id: string | null, options?: { reveal?: boolean; column?: string }) => void;
  setHoverEdge: (fkId: string | null) => void;
  setSelectedEdge: (fkId: string | null) => void;
  toggleFocus: () => boolean;
  setFocusHops: (hops: FocusHops) => void;
  setSearchQuery: (query: string) => void;
  stepMatch: (direction: 1 | -1) => SearchMatch | null;
  clearSearch: () => void;
  setDetailsOpen: (open: boolean) => void;
  /** Esc: clears the innermost active state. Returns what it cleared, or null. */
  clearOne: () => "search" | "focus" | "edge" | "details" | "selection" | "overlay" | null;
  setOverlay: (entries: Record<string, OverlayEntry>) => void;
  patchOverlay: (id: string, entry: OverlayEntry | null) => void;
  /** Writes a query heatmap: badges, animated joins and dimming in one store update. */
  setHeatmap: (entries: Record<string, OverlayEntry>, heat: HeatState) => void;
  setHighlight: (ids: Iterable<string> | null) => void;
  /** Removes badges, animated joins, dimming and highlights. */
  clearOverlay: () => void;
}

const EMPTY_SEARCH = { query: "", matches: [] as SearchMatch[], index: -1 };

type Inputs = Pick<
  CanvasState,
  "graph" | "selectedId" | "hoverEdge" | "selectedEdge" | "focus" | "search" | "heat" | "highlight"
>;

/** Foreign keys that connect each joined pair, in either direction, capped at MAX_FLOW_EDGES. */
export function flowEdgesFor(graph: SchemaGraph, joins: HeatState["joins"]): Set<string> {
  const out = new Set<string>();
  for (const { from, to } of joins) {
    if (from === to) continue;
    const candidates = [
      ...(graph.outgoing.get(from) ?? []).filter((e) => e.parent === to),
      ...(graph.outgoing.get(to) ?? []).filter((e) => e.parent === from),
    ];
    for (const e of candidates) {
      if (out.size >= MAX_FLOW_EDGES) return out;
      out.add(e.id);
    }
  }
  return out;
}

const NO_FLOW: ReadonlySet<string> = new Set();

function markColumns(
  marks: Map<string, Map<string, ColumnMark>>,
  nodeId: string,
  column: string,
  mark: ColumnMark,
): void {
  let byColumn = marks.get(nodeId);
  if (!byColumn) {
    byColumn = new Map();
    marks.set(nodeId, byColumn);
  }
  if (byColumn.get(column) !== "current-match") byColumn.set(column, mark);
}

function deriveEdge(
  edge: FkEdge,
  graph: SchemaGraph,
  nodeMarks: Map<string, NodeMark>,
  columnMarks: Map<string, Map<string, ColumnMark>>,
) {
  for (const key of relationshipChain(graph, edge)) if (!nodeMarks.has(key)) nodeMarks.set(key, "chain");
  for (const pair of edge.pairs) {
    markColumns(columnMarks, edge.child, pair.column, "endpoint");
    markColumns(columnMarks, edge.parent, pair.refColumn, "endpoint");
  }
}

export function derive({
  graph,
  selectedId,
  hoverEdge,
  selectedEdge,
  focus,
  search,
  heat,
  highlight,
}: Inputs): Derived {
  const nodeMarks = new Map<string, NodeMark>();
  const columnMarks = new Map<string, Map<string, ColumnMark>>();
  let keep: Set<string> | null = null;
  const activeEdge = hoverEdge ?? selectedEdge;

  // A query overlay or answer highlight dims the rest, unless focus mode or a search decides what stays.
  let queryKeep: Set<string> | null = heat && heat.involved.size > 0 ? new Set(heat.involved) : null;
  if (highlight) {
    // An answer highlights the tables it names; its result's plan may scan others. Both stay visible.
    queryKeep = new Set([...(queryKeep ?? []), ...highlight]);
    for (const id of highlight) nodeMarks.set(id, "highlight");
  }

  if (graph && focus) {
    keep = neighboursWithin(graph, focus.root, focus.hops);
    nodeMarks.set(focus.root, "focus-root");
  }
  if (search.matches.length > 0) {
    const matched = new Set<string>();
    search.matches.forEach((m, i) => {
      const current = i === search.index;
      matched.add(m.nodeId);
      if (m.column) markColumns(columnMarks, m.nodeId, m.column, current ? "current-match" : "match");
      if (current) nodeMarks.set(m.nodeId, "current-match");
      else if (!nodeMarks.has(m.nodeId)) nodeMarks.set(m.nodeId, "match");
    });
    keep = keep ? new Set([...keep].filter((k) => matched.has(k))) : matched;
  }
  if (selectedId) nodeMarks.set(selectedId, "selected");
  const edge = activeEdge ? graph?.edgeById.get(activeEdge) : undefined;
  if (graph && edge) deriveEdge(edge, graph, nodeMarks, columnMarks);

  keep ??= queryKeep;
  const flowEdges = graph && heat ? flowEdgesFor(graph, heat.joins) : NO_FLOW;
  return { nodeMarks, columnMarks, keep, activeEdge: edge ? activeEdge : null, flowEdges };
}

let revealNonce = 0;

export const useCanvasStore = create<CanvasState>((set, get) => {
  /** Applies a partial update and recomputes the derived marks in the same store write. */
  const update = (patch: Partial<CanvasState>) => {
    const next = { ...get(), ...patch };
    set({ ...patch, derived: derive(next) });
  };

  return {
    graph: null,
    selectedId: null,
    hoverEdge: null,
    selectedEdge: null,
    focus: null,
    focusHops: 1,
    search: EMPTY_SEARCH,
    overlay: {},
    heat: null,
    highlight: null,
    reveal: null,
    detailsOpen: false,
    derived: { nodeMarks: new Map(), columnMarks: new Map(), keep: null, activeEdge: null, flowEdges: NO_FLOW },

    setGraph: (graph) => {
      const s = get();
      const has = (id: string | null) => Boolean(id && graph?.byKey.has(id));
      const hasEdge = (id: string | null) => Boolean(id && graph?.edgeById.has(id));
      update({
        graph,
        selectedId: has(s.selectedId) ? s.selectedId : null,
        hoverEdge: hasEdge(s.hoverEdge) ? s.hoverEdge : null,
        selectedEdge: hasEdge(s.selectedEdge) ? s.selectedEdge : null,
        focus: s.focus && has(s.focus.root) ? s.focus : null,
        search: graph && s.search.query ? { ...s.search, matches: searchGraph(graph, s.search.query) } : EMPTY_SEARCH,
        detailsOpen: s.detailsOpen && has(s.selectedId),
      });
    },

    select: (id, options) => {
      const patch: Partial<CanvasState> = { selectedId: id };
      if (!id) patch.detailsOpen = false;
      if (id && options?.reveal) patch.reveal = { id, column: options.column, nonce: ++revealNonce };
      update(patch);
    },

    setHoverEdge: (hoverEdge) => {
      if (get().hoverEdge !== hoverEdge) update({ hoverEdge });
    },
    setSelectedEdge: (selectedEdge) => update({ selectedEdge }),

    toggleFocus: () => {
      const s = get();
      if (s.focus) {
        update({ focus: null });
        return true;
      }
      if (!s.selectedId) return false;
      update({ focus: { root: s.selectedId, hops: s.focusHops } });
      return true;
    },
    setFocusHops: (focusHops) => {
      const { focus } = get();
      update({ focusHops, focus: focus ? { ...focus, hops: focusHops } : null });
    },

    setSearchQuery: (query) => {
      const { graph } = get();
      update({ search: { query, matches: graph ? searchGraph(graph, query) : [], index: -1 } });
    },
    stepMatch: (direction) => {
      const { search } = get();
      const count = search.matches.length;
      if (count === 0) return null;
      const index = search.index === -1 && direction === -1 ? count - 1 : (search.index + direction + count) % count;
      const match = search.matches[index] ?? null;
      if (!match) return null;
      update({
        search: { ...search, index },
        selectedId: match.nodeId,
        reveal: { id: match.nodeId, column: match.column, nonce: ++revealNonce },
      });
      return match;
    },
    clearSearch: () => update({ search: EMPTY_SEARCH }),

    setDetailsOpen: (detailsOpen) => set({ detailsOpen: detailsOpen && Boolean(get().selectedId) }),

    clearOne: () => {
      const s = get();
      if (s.search.query) {
        update({ search: EMPTY_SEARCH });
        return "search";
      }
      if (s.detailsOpen) {
        set({ detailsOpen: false });
        return "details";
      }
      if (s.focus) {
        update({ focus: null });
        return "focus";
      }
      if (s.selectedEdge) {
        update({ selectedEdge: null });
        return "edge";
      }
      if (s.selectedId) {
        update({ selectedId: null });
        return "selection";
      }
      if (s.heat || s.highlight || Object.keys(s.overlay).length > 0) {
        update({ overlay: {}, heat: null, highlight: null });
        return "overlay";
      }
      return null;
    },

    setOverlay: (overlay) => set({ overlay }),
    setHeatmap: (overlay, heat) => update({ overlay, heat }),
    setHighlight: (ids) => update({ highlight: ids ? new Set(ids) : null }),
    clearOverlay: () => update({ overlay: {}, heat: null, highlight: null }),
    patchOverlay: (id, entry) => {
      const next = { ...get().overlay };
      if (entry) next[id] = entry;
      else delete next[id];
      set({ overlay: next });
    },
  };
});

/** Primitive per-node visual state, so a node re-renders only when its own state changes. */
export function nodeVisual(s: CanvasState, id: string): string {
  const mark = s.derived.nodeMarks.get(id) ?? "";
  const dim = s.derived.keep !== null && !s.derived.keep.has(id);
  return dim ? `${mark}|dim` : mark;
}

export function edgeVisual(
  s: CanvasState,
  fkId: string,
  child: string,
  parent: string,
): "active" | "flow" | "dim" | "" {
  if (s.derived.activeEdge === fkId) return "active";
  if (s.derived.flowEdges.has(fkId)) return "flow";
  const keep = s.derived.keep;
  if (keep && (!keep.has(child) || !keep.has(parent))) return "dim";
  return "";
}
