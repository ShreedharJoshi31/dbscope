import { cn } from "cn";
import { KeyRound, Link2 } from "lucide-react";
import type { ColumnFlags } from "@/entities/schema/model/graph";

interface Badge {
  text: string;
  title: string;
}

/** Text badges with a title tooltip; PK and FK also get an icon. Never colour alone. */
export function columnBadges(flags: ColumnFlags, generatedKind?: string): Badge[] {
  const badges: Badge[] = [];
  if (flags.unique && !flags.pk) badges.push({ text: "UQ", title: "Unique" });
  if (flags.identity) badges.push({ text: "ID", title: "Identity column" });
  if (flags.generated)
    badges.push({ text: "GEN", title: `Generated column${generatedKind ? ` (${generatedKind})` : ""}` });
  if (flags.nullable) badges.push({ text: "NULL", title: "Nullable" });
  return badges;
}

export function KeyIcon({ flags }: { flags: ColumnFlags }) {
  if (flags.pk) {
    return (
      <span
        title={flags.fk ? "Primary key and foreign key" : "Primary key"}
        className="flex w-7 shrink-0 items-center gap-0.5 text-2xs font-semibold text-text-primary"
      >
        <KeyRound aria-hidden className="size-3" />
        PK
      </span>
    );
  }
  if (flags.fk || flags.inferredFk) {
    return (
      <span
        title={flags.fk ? "Foreign key" : "Inferred foreign key: guessed from the name, not declared"}
        className="flex w-7 shrink-0 items-center gap-0.5 text-2xs font-semibold text-text-muted"
      >
        <Link2 aria-hidden className="size-3" />
        {flags.fk ? "FK" : "FK?"}
      </span>
    );
  }
  return <span aria-hidden className="w-7 shrink-0" />;
}

export function BadgeList({ badges, className }: { badges: Badge[]; className?: string }) {
  if (badges.length === 0) return null;
  return (
    <span className={cn("flex shrink-0 items-center gap-0.5", className)}>
      {badges.map((b) => (
        <span
          key={b.text}
          title={b.title}
          className="rounded-xs border border-border-default px-0.5 font-mono text-2xs leading-3.5 text-text-muted"
        >
          {b.text}
        </span>
      ))}
    </span>
  );
}
