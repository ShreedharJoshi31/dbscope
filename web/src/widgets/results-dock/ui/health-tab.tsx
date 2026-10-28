import { HeartPulse, LoaderCircle, RefreshCw } from "lucide-react";
import { useActiveConnection } from "@/entities/connection";
import { type ServerHealthCheck, type ServerHealthStatus, useServerHealth } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";
import { StatusIcon, type StatusTone } from "@/shared/ui/status-icon";

const TONE: Record<ServerHealthStatus, StatusTone> = { ok: "success", warn: "warn", danger: "danger", unknown: "info" };
const LABEL: Record<ServerHealthStatus, string> = {
  ok: "OK",
  warn: "Watch",
  danger: "Act now",
  unknown: "Not readable",
};
const ORDER: Record<ServerHealthStatus, number> = { danger: 0, warn: 1, unknown: 2, ok: 3 };

function CheckRow({ check }: { check: ServerHealthCheck }) {
  return (
    <li data-testid="health-check" data-status={check.status} className="flex items-start gap-2 px-3 py-1.5 text-xs">
      <StatusIcon tone={TONE[check.status]} className="mt-0.5 size-3.5 shrink-0" />
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex flex-wrap items-baseline gap-x-2">
          <span className="font-medium">{check.title}</span>
          <span className="font-mono tabular-nums">{check.value}</span>
          <span className="sr-only">{LABEL[check.status]}</span>
        </span>
        {check.detail && <span className="text-text-muted">{check.detail}</span>}
        {check.status !== "ok" && check.fix && <span className="text-text-muted">What to do: {check.fix}</span>}
      </span>
    </li>
  );
}

/** The server health checks of a connection (internal/health), worst first. */
export function HealthTab() {
  const { profile } = useActiveConnection();
  const enabled = Boolean(profile?.capabilities.health);
  const report = useServerHealth(profile?.id, enabled);
  if (!enabled) {
    return (
      <EmptyState icon={HeartPulse} title="No health checks" description="This engine has no health checks yet." />
    );
  }
  if (report.isPending) {
    return (
      <p role="status" className="flex items-center gap-2 p-3 text-xs text-text-muted">
        <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
        Reading the server's health…
      </p>
    );
  }
  if (report.isError) {
    return (
      <p role="alert" className="p-3 text-xs text-status-danger-fg">
        Could not read the server's health: {report.error.message}
      </p>
    );
  }
  const checks = [...report.data.checks].sort((a, b) => ORDER[a.status] - ORDER[b.status]);
  const counts = checks.reduce<Record<ServerHealthStatus, number>>(
    (acc, c) => {
      acc[c.status]++;
      return acc;
    },
    { ok: 0, warn: 0, danger: 0, unknown: 0 },
  );
  return (
    <div className="flex h-full min-h-0 flex-col overflow-auto">
      <section
        aria-label="Health summary"
        className="flex items-center gap-2 border-b border-border-default px-3 py-2 text-xs text-text-muted"
      >
        <span className="flex-1">
          {report.data.server} · {counts.danger} to act on, {counts.warn} to watch, {counts.unknown} not readable,{" "}
          {counts.ok} fine · read in {report.data.tookMs} ms
        </span>
        <Button variant="ghost" size="sm" onClick={() => report.refetch()} disabled={report.isFetching}>
          <RefreshCw aria-hidden className={report.isFetching ? "animate-spin" : undefined} />
          Refresh
        </Button>
      </section>
      <ul aria-label="Health checks" className="flex flex-col divide-y divide-border-default">
        {checks.map((c) => (
          <CheckRow key={c.id} check={c} />
        ))}
      </ul>
    </div>
  );
}
