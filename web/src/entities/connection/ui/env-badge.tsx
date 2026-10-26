import { cn } from "cn";
import { ENV_META } from "@/entities/connection/model/env";
import type { Env } from "@/shared/api";

interface EnvBadgeProps {
  env: Env;
  className?: string;
}

/** Outline badge: icon + text + colour, so the environment never relies on colour alone. */
export function EnvBadge({ env, className }: EnvBadgeProps) {
  const meta = ENV_META[env];
  const Icon = meta.icon;
  return (
    <span
      data-env={env}
      className={cn(
        "inline-flex h-5 shrink-0 items-center gap-1 rounded-sm border bg-surface-canvas px-1 font-mono text-2xs font-semibold uppercase tracking-wide",
        meta.tone,
        className,
      )}
    >
      <Icon aria-hidden className="size-3" />
      <span className="sr-only">Environment: </span>
      {meta.label}
    </span>
  );
}
