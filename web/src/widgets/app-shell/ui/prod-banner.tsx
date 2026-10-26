import { ShieldAlert } from "lucide-react";
import type { Profile } from "@/shared/api";

/** Always visible for prod-replica; it has no close button by design (UX rule 2). */
export function ProdBanner({ profile }: { profile: Profile }) {
  return (
    <div
      role="note"
      aria-label="Production replica warning"
      data-testid="prod-banner"
      className="flex h-8 shrink-0 items-center gap-3 bg-env-prod-banner-bg px-4 text-xs text-env-prod-banner-fg"
    >
      <ShieldAlert aria-hidden className="size-4" />
      <span className="font-semibold tracking-wide uppercase">Prod-replica</span>
      <span className="font-semibold tracking-wide uppercase">Read-only</span>
      <span className="truncate font-mono">
        {profile.database} @ {profile.host}
      </span>
      <span className="truncate font-mono">{profile.user}</span>
    </div>
  );
}
