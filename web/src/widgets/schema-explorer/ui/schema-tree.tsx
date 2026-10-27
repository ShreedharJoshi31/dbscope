import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { type SchemaGraph, useCanvasStore } from "@/entities/schema";
import { announce } from "@/shared/lib/announcer";
import { flattenTree, rowPositions, schemaRowId, type TreeRow } from "@/widgets/schema-explorer/model/tree";
import { treeKeyHandler } from "@/widgets/schema-explorer/model/use-tree-keyboard";
import { TreeRowView } from "@/widgets/schema-explorer/ui/tree-row";

interface SchemaTreeProps {
  graph: SchemaGraph;
  filter: string;
}

/**
 * The non-visual equivalent of the canvas (UX rule 12): one tab stop with a
 * roving tabindex, arrow-key navigation, and selection shared with the canvas
 * through the canvas store.
 */
export function SchemaTree({ graph, filter }: SchemaTreeProps) {
  const [expanded, setExpandedSet] = useState<ReadonlySet<string>>(() => new Set(graph.schemas.map(schemaRowId)));
  const [activeId, setActiveId] = useState<string | null>(null);
  const selectedId = useCanvasStore((s) => s.selectedId);
  const listRef = useRef<HTMLDivElement>(null);
  const focusOnChange = useRef(false);

  const rows = useMemo(() => flattenTree(graph, expanded, filter), [graph, expanded, filter]);
  const positions = useMemo(() => rowPositions(rows), [rows]);
  const active = rows.some((r) => r.id === activeId) ? activeId : (rows[0]?.id ?? null);

  // Moving the roving tab stop moves DOM focus only when the user is already in the tree.
  useEffect(() => {
    if (!focusOnChange.current || !active) return;
    focusOnChange.current = false;
    const el = listRef.current?.querySelector<HTMLElement>(`[data-row-id="${CSS.escape(active)}"]`);
    el?.focus();
    el?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const setActive = useCallback((id: string) => {
    focusOnChange.current = true;
    setActiveId(id);
  }, []);

  const setExpanded = useCallback((id: string, open: boolean) => {
    setExpandedSet((prev) => {
      const next = new Set(prev);
      if (open) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  const activate = useCallback(
    (row: TreeRow) => {
      if (row.type === "schema") {
        setExpanded(row.id, !row.expanded);
        return;
      }
      const store = useCanvasStore.getState();
      if (row.type === "relation") {
        store.select(row.key, { reveal: true });
        announce(`Selected ${row.key} on the canvas.`);
      } else {
        store.select(row.relationKey, { reveal: true, column: row.row.column.name });
        announce(`Selected ${row.relationKey}.${row.row.column.name} on the canvas.`);
      }
    },
    [setExpanded],
  );

  const onRowClick = useCallback(
    (row: TreeRow) => {
      setActiveId(row.id);
      activate(row);
    },
    [activate],
  );
  const onToggle = useCallback(
    (row: TreeRow) => {
      if (row.type !== "column") setExpanded(row.id, !row.expanded);
    },
    [setExpanded],
  );

  const onKeyDown = treeKeyHandler({ rows, positions, activeId: active, setActive, setExpanded, activate });

  if (rows.length === 0) {
    return (
      <p role="status" className="px-3 py-4 text-xs text-text-muted">
        No tables or columns match “{filter}”.
      </p>
    );
  }

  return (
    <div
      ref={listRef}
      role="tree"
      aria-label="Schemas, tables and columns"
      aria-multiselectable={false}
      className="min-h-0 flex-1 overflow-y-auto py-1"
      onKeyDown={onKeyDown}
    >
      {rows.map((row) => (
        <TreeRowView
          key={row.id}
          row={row}
          position={positions.get(row.id)}
          active={row.id === active}
          selected={row.type === "relation" && row.key === selectedId}
          onClick={onRowClick}
          onToggle={onToggle}
        />
      ))}
    </div>
  );
}
