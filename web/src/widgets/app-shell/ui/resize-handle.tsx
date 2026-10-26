import { cn } from "cn";
import { Separator } from "react-resizable-panels";

interface ResizeHandleProps {
  label: string;
  orientation: "vertical" | "horizontal";
}

/**
 * 1px visual line with a wider hit area. Focus it and use the arrow keys to
 * resize; double-click resets the neighbouring panel to its default size.
 */
export function ResizeHandle({ label, orientation }: ResizeHandleProps) {
  return (
    <Separator
      aria-label={label}
      title={`${label}. Drag or use arrow keys; double-click to reset.`}
      className={cn(
        "relative shrink-0 bg-border-default outline-none transition-colors",
        "data-[separator=hover]:bg-border-focus data-[separator=active]:bg-border-focus",
        "data-[separator=focus]:bg-border-focus data-[separator=focus]:ring-2 data-[separator=focus]:ring-border-focus",
        orientation === "vertical" ? "w-px" : "h-px",
      )}
    />
  );
}
