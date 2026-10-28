import { Check, Copy, Crosshair, Lock } from "lucide-react";
import { type ReactNode, type Ref, useState } from "react";
import {
  lockModeTone,
  lockModeWords,
  lockTimeoutCopy,
  type MigrationReview,
  RatingBadge,
  type ReviewStatement,
  sizeWords,
  txWords,
  type UnanalyzableView,
  workWords,
} from "@/entities/migration-finding";
import { showReviewOnCanvas } from "@/features/analyze-migration";
import type { MigrationFinding } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { copyText } from "@/shared/lib/clipboard";
import { Button } from "@/shared/ui/button";
import { SqlCode } from "@/shared/ui/sql-code";
import { StatusIcon } from "@/shared/ui/status-icon";

function Fact({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-2 border-b border-border-default py-1.5 last:border-b-0">
      <dt className="text-xs text-text-muted">{term}</dt>
      <dd className="min-w-0 text-xs text-text-primary">{children}</dd>
    </div>
  );
}

/** The words say what the lock blocks; the tint only repeats it. */
const LOCK_TONE = {
  info: "text-status-info-fg",
  warn: "text-status-warn-fg",
  danger: "text-status-danger-fg",
} as const;

function Locks({ statement }: { statement: ReviewStatement }) {
  if (statement.locks.length === 0) return <span className="text-text-muted">No table locks</span>;
  return (
    <ul className="flex flex-col gap-1">
      {statement.locks.map((l) => (
        <li key={`${l.relation} ${l.mode}`} data-testid="statement-lock" className="flex items-start gap-1.5">
          <Lock aria-hidden className={`mt-px size-3.5 shrink-0 ${LOCK_TONE[lockModeTone(l.mode)]}`} />
          <span className="min-w-0">
            <span className="font-mono">{l.relation}</span>: <span className="font-semibold">{l.mode}</span> —{" "}
            {lockModeWords(l.mode)}
          </span>
        </li>
      ))}
    </ul>
  );
}

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="ghost"
      size="xs"
      aria-label={copied ? `Copied ${label}` : `Copy ${label}`}
      onClick={() =>
        void copyText(text).then((ok) => {
          announce(ok ? `Copied ${label}.` : "The browser refused clipboard access.");
          if (ok) {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }
        })
      }
    >
      {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
      {copied ? "Copied" : "Copy"}
    </Button>
  );
}

function FindingItem({ finding }: { finding: MigrationFinding }) {
  return (
    <li
      data-testid="statement-finding"
      data-rule={finding.ruleId}
      className="flex flex-col gap-1.5 rounded-md border border-border-default p-2"
    >
      <div className="flex items-center gap-2">
        <RatingBadge rating={finding.rating} />
        <code className="font-mono text-2xs text-text-muted">{finding.ruleId}</code>
      </div>
      <p className="text-xs break-words text-text-primary">{finding.message}</p>
      {finding.safeAlternative && (
        <div className="flex flex-col gap-1 rounded-sm border-l-2 border-status-success-fg bg-surface-subtle py-1 pr-1 pl-2">
          <div className="flex items-center gap-1">
            <span className="text-2xs font-semibold text-status-success-fg">Safe alternative</span>
            <span className="ml-auto">
              <CopyButton text={finding.safeAlternative} label="the safe alternative" />
            </span>
          </div>
          <p data-testid="safe-alternative" className="text-xs break-words text-text-primary">
            {finding.safeAlternative}
          </p>
        </div>
      )}
    </li>
  );
}

interface StatementDetailProps {
  review: MigrationReview;
  statement: ReviewStatement;
  total: number;
  unanalyzable: UnanalyzableView | undefined;
  headingRef: Ref<HTMLHeadingElement>;
}

/** The selected statement: what it locks and does, its transaction and lock_timeout context, findings and fixes. */
export function StatementDetail({ review, statement: s, total, unanalyzable, headingRef }: StatementDetailProps) {
  const timeout = lockTimeoutCopy(s.lockTimeout, s.locks.length);
  // The score and lock_timeout are PostgreSQL's; SQLite and ClickHouse findings carry the sizes and take no
  // lock to time out, and MySQL has lock_wait_timeout.
  const engine = review.model?.report.engine;
  const noLocks = engine === "sqlite" || engine === "clickhouse";
  const timeoutName = engine ? "lock_wait_timeout" : "lock_timeout";
  const diff = review.model?.diff;
  const changed = diff ? diff.added.length + diff.altered.length + diff.dropped.length : 0;
  return (
    <section
      aria-labelledby="statement-detail-heading"
      data-testid="statement-detail"
      data-statement={s.index}
      className="flex min-h-0 flex-col gap-3 overflow-y-auto p-3"
    >
      <div className="flex items-start gap-2">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h3
            id="statement-detail-heading"
            ref={headingRef}
            tabIndex={-1}
            className="truncate font-mono text-sm font-semibold text-text-primary outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
          >
            {s.kind === "unparseable" ? "Unparseable statement" : s.kind}
          </h3>
          <p className="text-2xs text-text-muted">
            Statement {s.index + 1} of {total} · <span className="font-mono">{`${s.file}:${s.line}`}</span>
          </p>
        </div>
        <RatingBadge rating={s.rating} size="md" />
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={() => showReviewOnCanvas(review.id, s.index)}
          disabled={s.relations.length === 0 && changed === 0}
        >
          <Crosshair aria-hidden />
          Show on canvas
        </Button>
        {!engine && (
          <span
            className="self-center font-mono text-2xs text-text-muted"
            title="Lock class × work × size tier, plus lock-queue, missing lock_timeout and held-lock terms (docs/MIGRATIONS.md)"
          >
            score {s.score}
          </span>
        )}
      </div>
      <SqlCode sql={s.sql} label={`SQL of the statement at ${s.file} line ${s.line}`} className="max-h-40" />
      {unanalyzable && (
        <p
          role="note"
          className="flex items-start gap-1.5 rounded-md border border-status-warn-fg p-2 text-xs text-text-primary"
        >
          <StatusIcon tone="warn" className="mt-px size-3.5" />
          {unanalyzable.explanation}
        </p>
      )}
      <dl className="flex flex-col">
        <Fact term="Locks">
          <Locks statement={s} />
        </Fact>
        <Fact term="Work">{workWords(s.work, engine)}</Fact>
        {!noLocks && <Fact term="Size">{sizeWords(s.sizeBytes, s.locks.length)}</Fact>}
        <Fact term="Transaction">{txWords(s.tx)}</Fact>
        {!noLocks && (
          <Fact term={timeoutName}>
            <span className="flex items-start gap-1.5">
              {timeout.tone === "success" ? (
                <Lock aria-hidden className="mt-px size-3.5 shrink-0 text-status-success-fg" />
              ) : (
                <StatusIcon tone={timeout.tone} className="mt-px size-3.5" />
              )}
              {timeout.text.replaceAll("lock_timeout", timeoutName)}
            </span>
          </Fact>
        )}
      </dl>
      <section aria-labelledby="statement-findings-heading" className="flex flex-col gap-2">
        <h4 id="statement-findings-heading" className="text-2xs font-semibold tracking-wide text-text-muted uppercase">
          Findings ({s.findings.length})
        </h4>
        {s.findings.length === 0 ? (
          <p className="text-xs text-text-muted">No findings: this statement is safe as written.</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {s.findings.map((f, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: findings have no id; the list is static per report.
              <FindingItem key={i} finding={f} />
            ))}
          </ul>
        )}
      </section>
    </section>
  );
}
