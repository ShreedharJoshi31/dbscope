import { ShieldCheck } from "lucide-react";
import { useMemo } from "react";
import { schemaFindings, useCanvasStore } from "@/entities/schema";
import { useWorkbenchSchema } from "@/features/load-schema";
import { EmptyState } from "@/shared/ui/empty-state";
import { StatusIcon } from "@/shared/ui/status-icon";

/** Every design risk introspection found in the connection's schema; a row shows its table and details on the canvas. */
export function SchemaRisksTab() {
  const { query } = useWorkbenchSchema();
  const rows = useMemo(() => schemaFindings(query.data), [query.data]);
  if (rows.length === 0) {
    return (
      <EmptyState
        icon={ShieldCheck}
        title="No schema risks found"
        description="dbscope's design checks for this engine found nothing: no table missing a primary index, no field near its limit, no type conflict."
      />
    );
  }
  return (
    <div className="h-full min-h-0 overflow-auto">
      <table data-testid="schema-risks" className="w-full table-fixed border-collapse text-xs">
        <caption className="sr-only">Design risks in the schema. Select a table name to show it on the canvas.</caption>
        <thead className="sticky top-0 z-10 bg-surface-subtle text-left text-2xs tracking-wide text-text-muted uppercase">
          <tr>
            <th scope="col" className="w-20 px-2 py-1 font-medium">
              Level
            </th>
            <th scope="col" className="w-48 px-2 py-1 font-medium">
              Table
            </th>
            <th scope="col" className="px-2 py-1 font-medium">
              Risk
            </th>
            <th scope="col" className="w-56 px-2 py-1 font-medium">
              Rule
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map(({ relation, finding }) => (
            <tr
              key={relation + finding.ruleId + (finding.column ?? "") + finding.message}
              data-testid="schema-risk-row"
              data-level={finding.level}
              className="border-t border-border-default align-top"
            >
              <td className="px-2 py-1.5">
                <span className="flex items-center gap-1 capitalize">
                  <StatusIcon tone={finding.level} className="size-3.5" />
                  {finding.level === "danger" ? "high" : finding.level === "warn" ? "medium" : "low"}
                </span>
              </td>
              <td className="truncate px-2 py-1.5">
                <button
                  type="button"
                  className="truncate font-mono text-text-link hover:underline"
                  onClick={() => {
                    const canvas = useCanvasStore.getState();
                    canvas.select(relation, { reveal: true });
                    canvas.setDetailsOpen(true);
                  }}
                >
                  {relation}
                  {finding.column && `.${finding.column}`}
                </button>
              </td>
              <td className="px-2 py-1.5">{finding.message}</td>
              <td className="truncate px-2 py-1.5 font-mono text-text-muted">{finding.ruleId}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The tab's label, with the count of high and medium risks. */
export function SchemaRisksLabel() {
  const { query } = useWorkbenchSchema();
  const serious = useMemo(
    () => schemaFindings(query.data).filter((r) => r.finding.level !== "info").length,
    [query.data],
  );
  return (
    <>
      Schema risks
      {serious > 0 && (
        <span className="ml-1 rounded-full border border-status-warn-fg px-1 font-mono text-2xs text-status-warn-fg">
          {serious}
          <span className="sr-only"> risks</span>
        </span>
      )}
    </>
  );
}
