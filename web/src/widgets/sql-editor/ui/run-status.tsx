import { useEffect, useState } from "react";
import { type RunMeta, useRunStore } from "@/entities/query";
import { formatDuration } from "@/features/run-query";

/** Seconds since the run started, ticking while it runs (UX rule 9). */
function Elapsed({ meta }: { meta: RunMeta }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 100);
    return () => window.clearInterval(timer);
  }, []);
  return <span className="font-mono tabular-nums">{formatDuration(Math.max(0, now - meta.startedAt))}</span>;
}

/** Running state, or the outcome of this tab's last run. */
export function RunStatus({ tabId }: { tabId: string }) {
  const running = useRunStore((s) => s.running);
  const output = useRunStore((s) => (s.output?.meta.tabId === tabId ? s.output : null));
  const error = useRunStore((s) => (s.lastError?.meta.tabId === tabId ? s.lastError : null));

  if (running) {
    return (
      <span role="status" data-testid="run-status" className="flex items-center gap-1 text-xs text-text-muted">
        {running.tabId === tabId
          ? running.kind === "explain"
            ? "Explaining…"
            : "Running…"
          : "Another tab is running…"}
        <Elapsed meta={running} />
      </span>
    );
  }
  if (error) {
    return (
      <span data-testid="run-status" className="text-xs text-status-danger-fg">
        Failed, see Messages
      </span>
    );
  }
  if (output?.result) {
    const r = output.result;
    return (
      <span data-testid="run-status" className="text-xs text-text-muted">
        <span className="font-mono tabular-nums">{r.rowCount.toLocaleString("en")}</span> rows{r.truncated ? "+" : ""}{" "}
        in <span className="font-mono tabular-nums">{formatDuration(r.durationMs)}</span>
      </span>
    );
  }
  if (output) {
    return (
      <span data-testid="run-status" className="text-xs text-text-muted">
        Plan ready
      </span>
    );
  }
  return null;
}
