import { cn } from "cn";
import { buildChecklist, ERROR_KIND_COPY } from "@/entities/connection/model/test-result";
import type { Env, TestResult } from "@/shared/api";
import { StatusIcon } from "@/shared/ui/status-icon";

interface TestResultChecklistProps {
  result: TestResult;
  env: Env | undefined;
  className?: string;
}

export function TestResultChecklist({ result, env, className }: TestResultChecklistProps) {
  return (
    <section
      aria-label="Connection test result"
      className={cn("rounded-md border border-border-default bg-surface-subtle p-3", className)}
    >
      {result.ok ? <PassedResult result={result} env={env} /> : <FailedResult result={result} />}
    </section>
  );
}

function PassedResult({ result, env }: { result: TestResult; env: Env | undefined }) {
  return (
    <ul className="flex flex-col gap-2">
      {buildChecklist(result, env).map((item) => (
        <li key={item.id} className="flex items-start gap-2 text-sm" data-tone={item.tone}>
          <StatusIcon tone={item.tone} className="mt-0.5" />
          <div className="min-w-0">
            <p className={cn(item.tone === "danger" && "font-medium text-status-danger-fg")}>{item.text}</p>
            {item.details && (
              <ul className="mt-1 flex flex-col gap-0.5 font-mono text-xs text-text-muted">
                {item.details.map((detail) => (
                  <li key={detail}>{detail}</li>
                ))}
              </ul>
            )}
          </div>
        </li>
      ))}
    </ul>
  );
}

function FailedResult({ result }: { result: TestResult }) {
  const kind = result.error?.kind ?? "other";
  const copy = ERROR_KIND_COPY[kind];
  return (
    <div className="flex items-start gap-2 text-sm" data-error-kind={kind}>
      <StatusIcon tone="danger" className="mt-0.5" />
      <div className="flex min-w-0 flex-col gap-1">
        <p className="font-semibold text-status-danger-fg">{copy.title}</p>
        {result.error?.message && (
          <p className="break-words font-mono text-xs text-text-primary">
            {result.error.message}
            {result.error.sqlstate && <span className="text-text-muted"> (SQLSTATE {result.error.sqlstate})</span>}
          </p>
        )}
        <p className="text-xs text-text-muted">{copy.hint}</p>
      </div>
    </div>
  );
}
