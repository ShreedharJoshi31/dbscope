import { OctagonX } from "lucide-react";
import { describeSchemaError } from "@/features/load-schema/model/describe-error";
import type { ApiError } from "@/shared/api";
import { Button } from "@/shared/ui/button";

interface SchemaLoadErrorProps {
  error: ApiError;
  statementTimeoutMs?: number | undefined;
  onRetry: () => void;
  retrying?: boolean;
  /** Offered as a way forward when the database can't be read. */
  onOpenSample?: (() => void) | undefined;
  compact?: boolean;
}

/** Stays on screen until the user acts; never only a vanishing toast. */
export function SchemaLoadError({
  error,
  statementTimeoutMs,
  onRetry,
  retrying,
  onOpenSample,
  compact,
}: SchemaLoadErrorProps) {
  const copy = describeSchemaError(error, statementTimeoutMs);
  return (
    <div role="alert" className="flex h-full min-h-0 items-center justify-center overflow-auto p-6">
      <div className={compact ? "flex flex-col gap-2" : "flex max-w-md flex-col gap-3"}>
        <p className="flex items-start gap-2 text-sm font-semibold text-status-danger-fg">
          <OctagonX aria-hidden className="mt-0.5 size-4 shrink-0" />
          {copy.title}
        </p>
        {copy.message && <p className="font-mono text-xs break-words text-text-primary">{copy.message}</p>}
        <p className="text-xs text-text-muted">{copy.hint}</p>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={onRetry} disabled={retrying}>
            {retrying ? "Trying again…" : "Try again"}
          </Button>
          {onOpenSample && (
            <Button variant="link" size="sm" onClick={onOpenSample}>
              Open the sample schema instead
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
