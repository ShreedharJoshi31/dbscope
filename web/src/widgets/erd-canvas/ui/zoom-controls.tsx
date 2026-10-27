import { Panel, useReactFlow } from "@xyflow/react";
import { Maximize, Minus, Plus } from "lucide-react";
import { motionDuration } from "@/shared/lib/reduced-motion";
import { Button } from "@/shared/ui/button";

interface ZoomControlsProps {
  onFit: () => void;
}

export function ZoomControls({ onFit }: ZoomControlsProps) {
  const { zoomIn, zoomOut } = useReactFlow();
  return (
    <Panel
      position="bottom-left"
      className="flex flex-col overflow-hidden rounded-md border border-border-default bg-surface-raised"
    >
      <Button
        variant="ghost"
        size="icon-sm"
        className="rounded-none"
        aria-label="Zoom in"
        title="Zoom in"
        onClick={() => zoomIn({ duration: motionDuration(150) })}
      >
        <Plus aria-hidden />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        className="rounded-none"
        aria-label="Zoom out"
        title="Zoom out"
        onClick={() => zoomOut({ duration: motionDuration(150) })}
      >
        <Minus aria-hidden />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        className="rounded-none"
        aria-label="Fit all tables in view"
        title="Fit view"
        onClick={onFit}
      >
        <Maximize aria-hidden />
      </Button>
    </Panel>
  );
}
