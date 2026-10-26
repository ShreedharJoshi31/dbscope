import { cn } from "cn";
import { CircleCheck, Info, OctagonX, TriangleAlert } from "lucide-react";

export type StatusTone = "success" | "warn" | "danger" | "info";

const TONES = {
  success: { icon: CircleCheck, className: "text-status-success-fg", srLabel: "Passed:" },
  warn: { icon: TriangleAlert, className: "text-status-warn-fg", srLabel: "Warning:" },
  danger: { icon: OctagonX, className: "text-status-danger-fg", srLabel: "Problem:" },
  info: { icon: Info, className: "text-status-info-fg", srLabel: "Note:" },
} as const;

/** Icon plus a screen-reader label, so status never depends on colour (UX rule 7). */
export function StatusIcon({ tone, className }: { tone: StatusTone; className?: string }) {
  const { icon: Icon, className: toneClass, srLabel } = TONES[tone];
  return (
    <>
      <Icon aria-hidden data-tone={tone} className={cn("size-4 shrink-0", toneClass, className)} />
      <span className="sr-only">{srLabel} </span>
    </>
  );
}
