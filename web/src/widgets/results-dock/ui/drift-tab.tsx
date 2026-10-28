import { GitCompareArrows } from "lucide-react";
import { useCanvasStore } from "@/entities/schema";
import { clearComparison, openCompare, useCompareStore } from "@/features/compare-schema";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";

const CHANGE_LABEL: Record<string, string> = {
  added: "added here",
  dropped: "missing here",
  altered: "changed",
  renamed: "renamed",
  "not-visible": "not compared",
};

/** What differs from the schema the user compared with; a row shows its table on the canvas. */
export function DriftTab() {
  const comparison = useCompareStore((s) => s.comparison);
  if (!comparison) {
    return (
      <EmptyState
        icon={GitCompareArrows}
        title="No comparison"
        description="Compare this connection's schema with an earlier one, or with another connection, to see tables, columns, keys and indexes that differ."
        action={{ label: "Compare schema…", onClick: openCompare }}
      />
    );
  }
  const { drift, label } = comparison;
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border-default px-2 py-1 text-xs text-text-muted">
        <span>
          {drift.items.length === 0 ? "No differences" : `${drift.items.length} differences`} from{" "}
          <span className="text-text-primary">{label}</span>
        </span>
        <span className="flex-1" />
        <Button variant="ghost" size="sm" onClick={openCompare}>
          Compare again…
        </Button>
        <Button variant="ghost" size="sm" onClick={clearComparison}>
          Clear
        </Button>
      </div>
      {drift.items.length > 0 && (
        <div className="min-h-0 flex-1 overflow-auto">
          <table data-testid="drift-table" className="w-full table-fixed border-collapse text-xs">
            <caption className="sr-only">
              Schema differences from {label}. Select a table to show it on the canvas.
            </caption>
            <thead className="sticky top-0 z-10 bg-surface-subtle text-left text-2xs tracking-wide text-text-muted uppercase">
              <tr>
                <th scope="col" className="w-56 px-2 py-1 font-medium">
                  Table
                </th>
                <th scope="col" className="w-28 px-2 py-1 font-medium">
                  Change
                </th>
                <th scope="col" className="px-2 py-1 font-medium">
                  Detail
                </th>
              </tr>
            </thead>
            <tbody>
              {drift.items.map((item) => (
                <tr
                  key={item.relation + (item.column ?? "") + item.detail}
                  data-testid="drift-row"
                  data-change={item.change}
                  className="border-t border-border-default align-top"
                >
                  <td className="truncate px-2 py-1.5">
                    <button
                      type="button"
                      className="truncate font-mono text-text-link hover:underline"
                      onClick={() => useCanvasStore.getState().select(item.relation, { reveal: true })}
                    >
                      {item.relation}
                      {item.column && `.${item.column}`}
                    </button>
                  </td>
                  <td className="px-2 py-1.5">{CHANGE_LABEL[item.change] ?? item.change}</td>
                  <td className="px-2 py-1.5">{item.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
