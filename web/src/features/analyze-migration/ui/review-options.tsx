import { cn } from "cn";
import { useId } from "react";
import type { ReviewOptions } from "@/entities/migration-finding";
import type { TxMode } from "@/shared/api";
import { Checkbox } from "@/shared/ui/checkbox";
import { NativeSelect } from "@/shared/ui/native-select";

const TX_MODES: { value: TxMode; label: string; description: string }[] = [
  {
    value: "auto",
    label: "Auto-detect",
    description: "From the migration tool's file names and directives (Flyway, golang-migrate, Prisma, Atlas)",
  },
  { value: "file", label: "One transaction per file", description: "Every file runs inside BEGIN … COMMIT" },
  { value: "statement", label: "Each statement on its own", description: "Every statement commits by itself" },
  { value: "none", label: "No wrapping", description: "Only the BEGIN … COMMIT written in the files" },
];

export function txModeDescription(mode: TxMode): string {
  return TX_MODES.find((m) => m.value === mode)?.description ?? "";
}

interface ReviewOptionsFieldsProps {
  value: ReviewOptions;
  onChange: (next: ReviewOptions) => void;
  /** One row for the review header; stacked with help text in the dialog. */
  compact?: boolean;
  disabled?: boolean;
}

/** Transaction mode and "my deploy tool sets lock_timeout", the two analysis options of docs/API.md Phase 4. */
export function ReviewOptionsFields({ value, onChange, compact, disabled }: ReviewOptionsFieldsProps) {
  const id = useId();
  return (
    <div className={cn("flex", compact ? "flex-wrap items-center gap-3" : "flex-col gap-3")}>
      <div className={cn("flex", compact ? "items-center gap-1.5" : "flex-col gap-1")}>
        <label htmlFor={`${id}-tx`} className="text-xs font-medium whitespace-nowrap text-text-primary">
          Transactions
        </label>
        <NativeSelect
          id={`${id}-tx`}
          aria-describedby={`${id}-tx-help`}
          value={value.txMode}
          disabled={disabled}
          onChange={(e) => onChange({ ...value, txMode: e.target.value as TxMode })}
          className={compact ? "h-7 w-auto text-xs" : undefined}
        >
          {TX_MODES.map((m) => (
            <option key={m.value} value={m.value}>
              {m.label}
            </option>
          ))}
        </NativeSelect>
        <p id={`${id}-tx-help`} className={compact ? "sr-only" : "text-xs text-text-muted"}>
          {txModeDescription(value.txMode)}
        </p>
      </div>
      {/* biome-ignore lint/a11y/noLabelWithoutControl: Base UI's Checkbox renders the control inside the wrapping label. */}
      <label className="flex items-start gap-2 text-xs text-text-primary">
        <Checkbox
          checked={value.assumeLockTimeout}
          disabled={disabled}
          onCheckedChange={(on) => onChange({ ...value, assumeLockTimeout: on })}
          className="mt-px"
        />
        <span className="flex flex-col gap-0.5">
          My deploy tool sets lock_timeout
          {!compact && (
            <span className="text-text-muted">
              Statements are then scored as if a lock_timeout is in effect, even without a SET in the files.
            </span>
          )}
        </span>
      </label>
    </div>
  );
}
