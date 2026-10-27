import { cn } from "cn";
import type * as React from "react";

/** A styled native <select>: keyboard and screen-reader behaviour come from the platform. */
export function NativeSelect({ className, ...props }: React.ComponentProps<"select">) {
  return (
    <select
      data-slot="native-select"
      className={cn(
        "h-8 w-full min-w-0 rounded-lg border border-border-control bg-surface-canvas px-2 text-sm text-text-primary outline-none",
        "focus-visible:ring-2 focus-visible:ring-border-focus disabled:opacity-50 aria-invalid:border-status-danger-fg",
        className,
      )}
      {...props}
    />
  );
}
