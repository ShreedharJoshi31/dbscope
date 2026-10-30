import type { CodeUsage, MigrationBlocker, MigrationPending } from "@/shared/api";
import { StatusIcon } from "@/shared/ui/status-icon";

/** "45 s", "12 min", "3 h": how long something has run, rounded to what matters. */
export function durationWords(seconds: number): string {
  if (seconds < 1) return "under a second";
  if (seconds < 90) return `${Math.round(seconds)} s`;
  if (seconds < 90 * 60) return `${Math.round(seconds / 60)} min`;
  return `${Math.round(seconds / 3600)} h`;
}

/** Sessions that would hold the migration up, as read when it was analyzed. */
export function Blockers({ blockers, at }: { blockers: MigrationBlocker[]; at: string | undefined }) {
  if (blockers.length === 0) return null;
  const when = at ? new Date(at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : null;
  return (
    <section
      aria-labelledby="blockers-heading"
      data-testid="blockers"
      className="flex flex-col gap-1 rounded-md border border-status-danger-fg p-2"
    >
      <h4 id="blockers-heading" className="flex items-center gap-1.5 text-xs font-semibold text-text-primary">
        <StatusIcon tone="danger" className="size-3.5" />
        {blockers.length === 1 ? "1 open session" : `${blockers.length} open sessions`} would block this migration
        {when && <span className="font-normal text-text-muted">(as of {when})</span>}
      </h4>
      <ul className="flex flex-col gap-0.5">
        {blockers.map((b) => (
          <li key={`${b.session} ${b.relation}`} className="text-xs text-text-primary">
            <span className="font-mono">{b.relation}</span>: session <span className="font-mono">{b.session}</span>
            {b.user && ` (${b.user})`}, {b.state}, open {durationWords(b.ageSeconds)}
            {b.lock && `, holds ${b.lock}`}
            {b.query && (
              <span className="block truncate font-mono text-2xs text-text-muted" title={b.query}>
                {b.query}
              </span>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}

/** Which files the migration tool's history showed as already applied. */
export function Pending({ pending }: { pending: MigrationPending }) {
  const applied = pending.applied.length;
  return (
    <p data-testid="pending" className="flex flex-wrap items-center gap-1.5 text-xs text-text-primary">
      <StatusIcon tone="info" className="size-3.5" />
      <span>
        {applied === 0
          ? `No files are applied yet per ${pending.tool}'s ${pending.table}.`
          : `${applied} applied file${applied === 1 ? "" : "s"} skipped per ${pending.tool}'s ${pending.table}.`}
      </span>
      {pending.changed.length > 0 && (
        <span className="text-status-warn-fg" title={pending.changed.join("\n")}>
          {pending.changed.length} changed since applied, checked again.
        </span>
      )}
    </p>
  );
}

/** Lines of the project that still name what the migration drops or renames, grouped by name. */
export function Usages({ usages }: { usages: CodeUsage[] }) {
  if (usages.length === 0) return null;
  const byName = new Map<string, CodeUsage[]>();
  for (const u of usages) byName.set(u.name, [...(byName.get(u.name) ?? []), u]);
  return (
    <section aria-labelledby="usages-heading" data-testid="usages" className="flex flex-col gap-1">
      <h4 id="usages-heading" className="flex items-center gap-1.5 text-xs font-semibold text-text-primary">
        <StatusIcon tone="warn" className="size-3.5" />
        Still used in the project
      </h4>
      <ul className="flex flex-col gap-1">
        {[...byName].map(([name, lines]) => (
          <li key={name} className="text-xs">
            <span className="font-mono font-semibold text-text-primary">{name}</span>
            <ul className="ml-3 flex flex-col">
              {lines.map((u) => (
                <li key={`${u.file}:${u.line}`} className="flex min-w-0 gap-2">
                  <span className="shrink-0 font-mono text-text-link">{`${u.file}:${u.line}`}</span>
                  <span className="truncate font-mono text-text-muted" title={u.text}>
                    {u.text}
                  </span>
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </section>
  );
}
