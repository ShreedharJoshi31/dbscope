import { DatabaseZap, Eye, Globe, Layers, type LucideIcon, Table2 } from "lucide-react";
import type { RelKind } from "@/shared/api";

export const KIND_META: Record<RelKind, { label: string; icon: LucideIcon }> = {
  table: { label: "Table", icon: Table2 },
  partitioned_table: { label: "Partitioned table", icon: Layers },
  view: { label: "View", icon: Eye },
  materialized_view: { label: "Materialized view", icon: DatabaseZap },
  foreign_table: { label: "Foreign table", icon: Globe },
};

/** Kind icon with the kind as accessible text, so the kind never depends on the icon alone. */
export function KindIcon({ kind, className }: { kind: RelKind; className?: string }) {
  const { icon: Icon, label } = KIND_META[kind];
  return (
    <span title={label} className="inline-flex shrink-0">
      <Icon aria-hidden className={className ?? "size-3.5 text-text-muted"} strokeWidth={1.75} />
      <span className="sr-only">{label}</span>
    </span>
  );
}
