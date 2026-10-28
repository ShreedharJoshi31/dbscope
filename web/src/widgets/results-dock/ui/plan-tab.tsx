import { ChevronsDownUp, ChevronsUpDown, ListTree } from "lucide-react";
import { useState } from "react";
import {
  countPlanNodes,
  ExecutedSql,
  isAnalyzed,
  planNodeIds,
  type RunOutput,
  summarizeFlags,
  useRunStore,
} from "@/entities/query";
import { formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { formatCompact } from "@/shared/lib/format-number";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";
import { StatusIcon } from "@/shared/ui/status-icon";
import { cost, PlanTreeNode } from "@/widgets/results-dock/ui/plan-tree";

function PlanSummary({ output, stepsOnly }: { output: RunOutput; stepsOnly: boolean }) {
  const { plan } = output;
  const analyzed = isAnalyzed(plan);
  const flags = summarizeFlags(plan);
  return (
    <section aria-label="Plan summary" className="flex flex-col gap-1.5 border-b border-border-default px-3 py-2">
      {stepsOnly ? (
        <p className="text-xs text-text-muted">
          The steps the database will take, without cost estimates · {countPlanNodes(plan) - 1} steps
          {flags.length === 0 && " · dbscope does not check plans of this engine for problems"}
        </p>
      ) : (
        <p className="text-xs text-text-muted">
          {analyzed ? "Measured with EXPLAIN ANALYZE" : "Estimates only, nothing was measured"} · total cost{" "}
          <span className="font-mono text-text-primary tabular-nums">{cost(plan.totalCost)}</span> · est.{" "}
          <span className="font-mono text-text-primary tabular-nums">{formatCompact(plan.planRows)}</span> rows ·{" "}
          {countPlanNodes(plan)} nodes
        </p>
      )}
      {stepsOnly && flags.length === 0 ? null : flags.length === 0 ? (
        <p className="flex items-center gap-1 text-xs">
          <StatusIcon tone="success" className="size-3.5" />
          No plan problems flagged.
        </p>
      ) : (
        <ul aria-label="Top plan findings" className="flex flex-col gap-0.5">
          {flags.map((f) => (
            <li key={f.message} data-testid="plan-flag" className="flex items-start gap-1 text-xs">
              <StatusIcon tone={f.level} className="mt-0.5 size-3.5" />
              <span>
                {f.message} <span className="text-text-muted">({f.node})</span>
              </span>
            </li>
          ))}
        </ul>
      )}
      <ExecutedSql executedSql={output.executedSql} submittedSql={output.meta.sql} />
    </section>
  );
}

function PlanView({ output }: { output: RunOutput }) {
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const toggle = (id: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const all = planNodeIds(output.plan).filter((id) => id !== "0");
  const stepsOnly = Boolean(output.plan.stepsOnly);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PlanSummary output={output} stepsOnly={stepsOnly} />
      <div className="flex shrink-0 items-center gap-1 border-b border-border-default px-2 py-0.5">
        <Button variant="ghost" size="xs" onClick={() => setCollapsed(new Set())}>
          <ChevronsUpDown aria-hidden />
          Expand all
        </Button>
        <Button variant="ghost" size="xs" onClick={() => setCollapsed(new Set(all))}>
          <ChevronsDownUp aria-hidden />
          Collapse all
        </Button>
      </div>
      <ul aria-label="Query plan" data-testid="plan-tree" className="min-h-0 flex-1 overflow-auto px-2 py-1">
        <PlanTreeNode
          node={output.plan}
          id="0"
          depth={0}
          analyzed={isAnalyzed(output.plan)}
          stepsOnly={stepsOnly}
          collapsed={collapsed}
          onToggle={toggle}
        />
      </ul>
    </div>
  );
}

export function PlanTab() {
  const output = useRunStore((s) => s.output);
  if (!output) {
    return (
      <EmptyState
        icon={ListTree}
        title="No plan"
        description={`Explain a query (${formatCombo(SHORTCUTS.explainQuery.combo)}) or run one to see its plan as a tree with estimated and actual costs.`}
      />
    );
  }
  return <PlanView key={output.meta.id} output={output} />;
}
