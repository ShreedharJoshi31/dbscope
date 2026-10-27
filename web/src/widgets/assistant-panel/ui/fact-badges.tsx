import { BookCheck, Gauge, ShieldCheck, Table2, Wrench } from "lucide-react";
import type { ReactNode } from "react";
import type { Turn } from "@/entities/assistant-turn";
import { ERD_TAB, useSqlTabs } from "@/entities/query";
import { highlightTables } from "@/entities/schema";
import { announce } from "@/shared/lib/announcer";
import { formatCompact, formatGrouped } from "@/shared/lib/format-number";

const BADGE =
  "flex items-center gap-1 rounded-xs border border-border-control bg-surface-canvas px-1.5 py-0.5 text-2xs text-text-primary";

function Badge({ icon, children, title }: { icon: ReactNode; children: ReactNode; title?: string }) {
  return (
    <li className={BADGE} title={title}>
      {icon}
      {children}
    </li>
  );
}

/** Facts about the answer, never model confidence (UX rule 6). */
export function FactBadges({ turn }: { turn: Turn }) {
  const answer = turn.answer;
  if (answer?.kind !== "sql") return null;
  const tables = answer.tables;
  const plan = turn.result?.plan;
  const estimate = plan
    ? { cost: plan.totalCost, rows: plan.planRows }
    : turn.confirmation?.reason === "cost"
      ? { cost: turn.confirmation.estimatedCost, rows: turn.confirmation.estimatedRows }
      : null;

  const showTables = () => {
    useSqlTabs.getState().setActive(ERD_TAB);
    highlightTables(tables);
    announce(`Highlighted ${tables.length} tables on the canvas: ${tables.join(", ")}.`);
  };

  return (
    <ul data-section="facts" aria-label="Facts about this answer" className="flex flex-wrap gap-1">
      <Badge
        icon={<ShieldCheck aria-hidden className="size-3 text-status-success-fg" />}
        title="dbscope checked that this is one read-only SELECT and runs it in a read-only transaction that is rolled back."
      >
        Reads only
      </Badge>
      {tables.length > 0 && (
        <li>
          <button
            type="button"
            onClick={showTables}
            title={tables.join(", ")}
            className={`${BADGE} hover:bg-surface-hover`}
          >
            <Table2 aria-hidden className="size-3" />
            {tables.length} {tables.length === 1 ? "table" : "tables"}
            <span className="sr-only">, show on the canvas</span>
          </button>
        </li>
      )}
      {estimate && (
        <Badge
          icon={<Gauge aria-hidden className="size-3" />}
          title="From EXPLAIN, before running. An estimate, not a measurement."
        >
          Plan estimate: cost <span className="font-mono">{formatGrouped(Math.round(estimate.cost))}</span>, ~
          <span className="font-mono">{formatCompact(estimate.rows)}</span> rows
        </Badge>
      )}
      {answer.repaired && (
        <Badge
          icon={<Wrench aria-hidden className="size-3 text-status-warn-fg" />}
          title="The first SQL failed. dbscope sent the error (with literals removed) back to the model once, and this is the fixed SQL."
        >
          Repaired once
        </Badge>
      )}
      {turn.clarification?.saveToGlossary && (
        <Badge
          icon={<BookCheck aria-hidden className="size-3" />}
          title="Your answer was saved to this connection's glossary."
        >
          Saved to glossary
        </Badge>
      )}
    </ul>
  );
}
