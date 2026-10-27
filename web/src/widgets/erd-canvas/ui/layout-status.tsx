import { LoaderCircle, TriangleAlert, X } from "lucide-react";
import { formatGrouped } from "@/shared/lib/format-number";
import type { LayoutStatus } from "@/widgets/erd-canvas/model/use-canvas-model";

/** Counted progress while a layout runs in its worker (UX rule 9); nothing else is blocked. */
export function LayoutStatusBanner({ status, onDismiss }: { status: LayoutStatus; onDismiss: () => void }) {
  if (status.state === "idle") return null;
  if (status.state === "running") {
    return (
      <div
        role="status"
        data-testid="layout-status"
        className="pointer-events-none absolute top-3 left-1/2 z-10 flex -translate-x-1/2 items-center gap-2 rounded-md border border-border-default bg-surface-raised px-3 py-1.5 text-xs text-text-primary shadow-sm"
      >
        <LoaderCircle aria-hidden className="size-3.5 animate-spin text-text-muted" />
        Laying out {formatGrouped(status.count)} tables…
      </div>
    );
  }
  return (
    <div
      role="alert"
      className="absolute top-3 left-1/2 z-10 flex -translate-x-1/2 items-center gap-2 rounded-md border border-status-warn-fg bg-surface-raised px-3 py-1.5 text-xs text-text-primary shadow-sm"
    >
      <TriangleAlert aria-hidden className="size-3.5 text-status-warn-fg" />
      Layout failed: {status.message}
      <button
        type="button"
        aria-label="Dismiss"
        className="text-text-muted hover:text-text-primary"
        onClick={onDismiss}
      >
        <X aria-hidden className="size-3.5" />
      </button>
    </div>
  );
}
