import { cn } from "cn";
import {
  ChevronsDownUp,
  ChevronsUpDown,
  Eraser,
  Focus,
  type LucideIcon,
  Map as MapIcon,
  RefreshCcw,
  RotateCcw,
  Waypoints,
} from "lucide-react";
import type { ReactNode, Ref } from "react";
import { clearCanvasOverlay, type FocusHops, hasCanvasOverlay, useCanvasStore } from "@/entities/schema";
import { ariaKeyShortcut, type KeyCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { announce } from "@/shared/lib/announcer";
import { SegmentedControl } from "@/shared/ui/segmented-control";
import { ShortcutKeys } from "@/shared/ui/shortcut-keys";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";
import { useCanvasPrefs } from "@/widgets/erd-canvas/model/canvas-prefs";
import { LAYOUT_LABELS, type LayoutAlgorithm } from "@/widgets/erd-canvas/model/layout/types";
import { CanvasSearch } from "@/widgets/erd-canvas/ui/canvas-search";

interface ToolButtonProps {
  icon: LucideIcon;
  label: string;
  onClick: () => void;
  pressed?: boolean | undefined;
  disabled?: boolean;
  combo?: KeyCombo;
  children?: ReactNode;
}

function ToolButton({ icon: Icon, label, onClick, pressed, disabled, combo, children }: ToolButtonProps) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            aria-label={label}
            aria-pressed={pressed}
            aria-keyshortcuts={combo ? ariaKeyShortcut(combo) : undefined}
            disabled={disabled}
            onClick={onClick}
            className={cn(
              "flex h-7 shrink-0 items-center gap-1 rounded-md px-1.5 text-xs text-text-muted transition-colors",
              "hover:bg-surface-hover hover:text-text-primary disabled:pointer-events-none disabled:opacity-50",
              pressed && "bg-surface-selected text-text-primary",
            )}
          />
        }
      >
        <Icon aria-hidden className="size-4" strokeWidth={1.75} />
        {children}
      </TooltipTrigger>
      <TooltipContent side="bottom" className="gap-2">
        {label}
        {combo && <ShortcutKeys combo={combo} />}
      </TooltipContent>
    </Tooltip>
  );
}

const Divider = () => <span aria-hidden className="h-4 w-px shrink-0 bg-border-default" />;

const LAYOUT_OPTIONS = (Object.keys(LAYOUT_LABELS) as LayoutAlgorithm[]).map((value) => ({
  value,
  label: LAYOUT_LABELS[value],
}));
const HOP_OPTIONS = ([1, 2, 3] as const).map((n) => ({ value: String(n) as "1" | "2" | "3", label: String(n) }));

export interface CanvasToolbarProps {
  searchRef: Ref<HTMLInputElement>;
  onExitSearch: () => void;
  algorithm: LayoutAlgorithm;
  layoutRunning: boolean;
  onLayout: (algorithm: LayoutAlgorithm) => void;
  onCollapseAll: () => void;
  onExpandAll: () => void;
  onToggleFocus: () => void;
  onResetLayout: () => void;
  inferredCount: number;
}

export function CanvasToolbar(props: CanvasToolbarProps) {
  const focus = useCanvasStore((s) => s.focus);
  const hops = useCanvasStore((s) => s.focusHops);
  const hasSelection = useCanvasStore((s) => s.selectedId !== null);
  const hasOverlay = useCanvasStore(hasCanvasOverlay);
  const { showInferred, minimap, set } = useCanvasPrefs();

  return (
    <div
      role="toolbar"
      aria-label="Canvas tools"
      className="flex shrink-0 flex-wrap items-center gap-1 border-b border-border-default bg-surface-subtle px-2 py-1"
    >
      <CanvasSearch ref={props.searchRef} onExit={props.onExitSearch} />
      <Divider />
      <span id="erd-layout-label" className="sr-only">
        Layout
      </span>
      <SegmentedControl
        aria-labelledby="erd-layout-label"
        options={LAYOUT_OPTIONS}
        value={props.algorithm}
        onValueChange={props.onLayout}
        size="sm"
      />
      <ToolButton
        icon={RefreshCcw}
        label={`Run the ${LAYOUT_LABELS[props.algorithm].toLowerCase()} layout again`}
        onClick={() => props.onLayout(props.algorithm)}
        disabled={props.layoutRunning}
      />
      <Divider />
      <ToolButton icon={ChevronsDownUp} label="Collapse all tables to key columns" onClick={props.onCollapseAll} />
      <ToolButton icon={ChevronsUpDown} label="Expand all tables" onClick={props.onExpandAll} />
      <Divider />
      <ToolButton
        icon={Focus}
        label={focus ? "Leave focus mode" : "Focus on the selected table"}
        pressed={Boolean(focus)}
        disabled={!focus && !hasSelection}
        onClick={props.onToggleFocus}
        combo={SHORTCUTS.canvasFocusMode.combo}
      />
      <span id="erd-hops-label" className="text-2xs text-text-muted">
        Hops
      </span>
      <SegmentedControl
        aria-labelledby="erd-hops-label"
        options={HOP_OPTIONS}
        value={String(hops) as "1" | "2" | "3"}
        onValueChange={(v) => useCanvasStore.getState().setFocusHops(Number(v) as FocusHops)}
        size="sm"
      />
      <Divider />
      <ToolButton
        icon={Waypoints}
        label={showInferred ? "Hide inferred foreign keys" : "Show inferred foreign keys"}
        pressed={showInferred}
        onClick={() => set({ showInferred: !showInferred })}
      >
        <span className="font-mono tabular-nums">{props.inferredCount}</span>
      </ToolButton>
      <ToolButton
        icon={MapIcon}
        label={minimap ? "Hide minimap" : "Show minimap"}
        pressed={minimap}
        onClick={() => set({ minimap: !minimap })}
      />
      <ToolButton icon={RotateCcw} label="Reset layout" onClick={props.onResetLayout} disabled={props.layoutRunning} />
      {hasOverlay && (
        <>
          <Divider />
          <ToolButton
            icon={Eraser}
            label="Clear the query overlay"
            combo={SHORTCUTS.canvasClear.combo}
            onClick={() => {
              clearCanvasOverlay();
              announce("Cleared the query overlay.");
            }}
          >
            Clear overlay
          </ToolButton>
        </>
      )}
    </div>
  );
}
