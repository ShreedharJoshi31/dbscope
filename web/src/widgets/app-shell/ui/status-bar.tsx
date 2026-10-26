import { LoaderCircle, ShieldAlert } from "lucide-react";
import { EnvBadge, ERROR_KIND_COPY } from "@/entities/connection";
import { type Profile, useHealth, useSavedProfileTest } from "@/shared/api";
import { StatusIcon } from "@/shared/ui/status-icon";

function ConnectionState({ profile }: { profile: Profile }) {
  const test = useSavedProfileTest(profile.id);

  if (test.isPending) {
    return (
      <span className="flex items-center gap-1">
        <LoaderCircle aria-hidden className="size-3 animate-spin" />
        Checking connection…
      </span>
    );
  }
  if (test.isError) {
    const locked = test.error.code === "secrets_locked";
    return (
      <span className="flex items-center gap-1">
        <StatusIcon tone="warn" className="size-3" />
        {locked ? "Vault locked, connection not checked" : `Check failed: ${test.error.message}`}
      </span>
    );
  }
  const result = test.data;
  if (!result.ok) {
    return (
      <span className="flex items-center gap-1 text-status-danger-fg">
        <StatusIcon tone="danger" className="size-3" />
        {ERROR_KIND_COPY[result.error?.kind ?? "other"].title}
      </span>
    );
  }
  return (
    <>
      <span className="flex items-center gap-1">
        <StatusIcon tone="success" className="size-3" />
        Connected
        {result.latencyMs !== undefined && <span className="font-mono tabular-nums">{result.latencyMs} ms</span>}
      </span>
      {result.serverVersion && <span className="font-mono">PostgreSQL {result.serverVersion}</span>}
    </>
  );
}

export function StatusBar({ profile }: { profile: Profile | undefined }) {
  const { data: health } = useHealth();

  return (
    <footer
      data-region="status"
      tabIndex={-1}
      className="flex h-6 shrink-0 items-center gap-4 border-t border-border-default bg-surface-subtle px-2 text-2xs text-text-muted"
    >
      {profile && (
        <>
          <EnvBadge env={profile.env} className="h-4" />
          {profile.env === "prod-replica" && (
            <span className="flex items-center gap-1 font-semibold text-env-prod-fg">
              <ShieldAlert aria-hidden className="size-3" />
              Read-only
            </span>
          )}
          <ConnectionState profile={profile} />
        </>
      )}
      <span className="ml-auto">No schema snapshot yet</span>
      {health && <span className="font-mono">dbscope {health.version}</span>}
    </footer>
  );
}
