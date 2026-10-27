import { cn } from "cn";
import { memo } from "react";
import { cellKind, formatCell } from "@/entities/query/model/value-format";
import type { CellValue } from "@/shared/api";

const KIND_CLASS = {
  null: "italic text-text-muted",
  number: "font-mono tabular-nums",
  boolean: "font-mono",
  json: "font-mono",
  text: "",
} as const;

/** One result value. NULL is italic "NULL"; an empty string stays empty (UX rule 7). */
function CellValueViewImpl({ value, type, className }: { value: CellValue; type?: string; className?: string }) {
  const kind = cellKind(value, type);
  const text = formatCell(value);
  return (
    <span
      data-kind={kind}
      data-empty={text === "" || undefined}
      title={text === "" ? "Empty string" : undefined}
      className={cn("block truncate", KIND_CLASS[kind], className)}
    >
      {text}
    </span>
  );
}

export const CellValueView = memo(CellValueViewImpl);
