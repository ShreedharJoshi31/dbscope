import { KeyRound, LoaderCircle } from "lucide-react";
import { useActiveConnection } from "@/entities/connection";
import { formatBytes, RatingBadge } from "@/entities/migration-finding";
import { type KeyspaceReport, useKeyspace } from "@/shared/api";
import { formatGrouped } from "@/shared/lib/format-number";
import { EmptyState } from "@/shared/ui/empty-state";

function Summary({ report }: { report: KeyspaceReport }) {
  const { server, memory } = report;
  const name = server.flavor === "valkey" ? "Valkey" : "Redis";
  const limit =
    memory.max > 0 ? `of ${formatBytes(memory.max)} (${memory.policy})` : `with no maxmemory (${memory.policy})`;
  return (
    <section
      aria-label="Keyspace summary"
      className="flex flex-col gap-1 border-b border-border-default px-3 py-2 text-xs"
    >
      <p className="text-text-muted">
        {name} {server.version}
        {server.role === "slave" || server.role === "replica" ? " replica" : ""} · {formatBytes(memory.used)} used{" "}
        {limit} · {formatGrouped(report.dbKeys)} keys, {formatGrouped(report.dbExpire)} with an expiry
      </p>
      <p className="text-text-muted">
        Scanned {formatGrouped(report.scanned)} keys in {formatGrouped(report.tookMs)} ms
        {report.complete ? ", the whole keyspace" : ", then stopped at the scan budget"}. Values are never read.
      </p>
      {report.warnings.map((w) => (
        <p key={w} role="note" className="text-status-warn-fg">
          {w}
        </p>
      ))}
    </section>
  );
}

function Findings({ report }: { report: KeyspaceReport }) {
  if (report.findings.length === 0) {
    return (
      <p className="px-3 py-2 text-xs text-text-muted">No risks found in the keyspace or the server's settings.</p>
    );
  }
  return (
    <ul aria-label="Keyspace findings" className="flex flex-col divide-y divide-border-default">
      {report.findings.map((f) => (
        <li
          key={f.ruleId + f.message}
          data-testid="keyspace-finding"
          className="flex items-start gap-2 px-3 py-1.5 text-xs"
        >
          <RatingBadge rating={f.rating} />
          <span className="flex min-w-0 flex-col gap-0.5">
            <span>{f.message}</span>
            {f.safeAlternative && <span className="text-text-muted">Safer: {f.safeAlternative}</span>}
            <span className="font-mono text-2xs text-text-muted">{f.ruleId}</span>
          </span>
        </li>
      ))}
    </ul>
  );
}

function Patterns({ report }: { report: KeyspaceReport }) {
  return (
    <table aria-label="Key patterns" className="w-full border-collapse text-xs">
      <thead className="sticky top-0 bg-surface-subtle text-left text-2xs tracking-wide text-text-muted uppercase">
        <tr>
          <th className="px-3 py-1 font-medium">Pattern</th>
          <th className="px-2 py-1 font-medium">Types</th>
          <th className="px-2 py-1 text-right font-medium">Keys</th>
          <th className="px-2 py-1 text-right font-medium">Memory</th>
          <th className="px-2 py-1 text-right font-medium">No expiry</th>
          <th className="px-3 py-1 font-medium">Largest key</th>
        </tr>
      </thead>
      <tbody>
        {report.patterns.map((p) => (
          <tr key={p.pattern} data-testid="key-pattern" className="border-t border-border-default">
            <td className="px-3 py-1 font-mono">{p.pattern}</td>
            <td className="px-2 py-1 text-text-muted">
              {Object.entries(p.types)
                .map(([t, n]) => (Object.keys(p.types).length > 1 ? `${t} ${n}` : t))
                .join(", ")}
            </td>
            <td className="px-2 py-1 text-right font-mono tabular-nums">{formatGrouped(p.keys)}</td>
            <td className="px-2 py-1 text-right font-mono tabular-nums">{formatBytes(p.bytes)}</td>
            <td className="px-2 py-1 text-right font-mono tabular-nums">
              {p.keys > 0 ? `${Math.round((100 * p.noTtl) / p.keys)}%` : ""}
            </td>
            <td className="truncate px-3 py-1 font-mono text-text-muted" title={p.largest?.name}>
              {p.largest ? `${p.largest.name} (${formatBytes(p.largest.bytes)})` : ""}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** The keyspace report of a Redis or Valkey connection (internal/redis). */
export function KeyspaceTab() {
  const { profile } = useActiveConnection();
  const enabled = Boolean(profile?.capabilities.keyspace);
  const report = useKeyspace(profile?.id, enabled);
  if (!enabled) {
    return (
      <EmptyState
        icon={KeyRound}
        title="No keyspace report"
        description="The keyspace report is for Redis and Valkey connections."
      />
    );
  }
  if (report.isPending) {
    return (
      <p role="status" className="flex items-center gap-2 p-3 text-xs text-text-muted">
        <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
        Scanning the keyspace…
      </p>
    );
  }
  if (report.isError) {
    return (
      <p role="alert" className="p-3 text-xs text-status-danger-fg">
        Could not read the keyspace: {report.error.message}
      </p>
    );
  }
  return (
    <div className="flex h-full min-h-0 flex-col overflow-auto">
      <Summary report={report.data} />
      <Findings report={report.data} />
      <Patterns report={report.data} />
    </div>
  );
}
