import { FlaskConical, LogOut } from "lucide-react";
import { closeSampleWorkspace, useWorkspaceStore } from "@/entities/schema";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";

/** Top-bar label for the read-only sample workspace, with the way out. */
export function SampleBadge() {
  const sample = useWorkspaceStore((s) => s.source === "sample");
  if (!sample) return null;
  return (
    <span className="flex items-center gap-1">
      <span
        data-testid="sample-badge"
        title="Built-in sample schema. Nothing here touches a database, and changes stay in this browser."
        className="inline-flex h-5 items-center gap-1 rounded-sm border border-status-info-fg bg-surface-canvas px-1 font-mono text-2xs font-semibold tracking-wide text-status-info-fg uppercase"
      >
        <FlaskConical aria-hidden className="size-3" />
        Sample
        <span className="sr-only"> schema, read-only</span>
      </span>
      <Button
        variant="ghost"
        size="xs"
        onClick={() => {
          closeSampleWorkspace();
          announce("Closed the sample schema.");
        }}
      >
        <LogOut aria-hidden />
        Exit sample
      </Button>
    </span>
  );
}
