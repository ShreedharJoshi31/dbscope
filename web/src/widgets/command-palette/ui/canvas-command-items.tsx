import {
  Activity,
  ChevronsDownUp,
  ChevronsUpDown,
  FlaskConical,
  Focus,
  LayoutGrid,
  LogOut,
  type LucideIcon,
  Map as MapIcon,
  Maximize,
  Network,
  Orbit,
  RotateCcw,
  Search,
  Waypoints,
} from "lucide-react";
import {
  type CanvasCommandId,
  closeSampleWorkspace,
  runCanvasCommand,
  useCanvasCommands,
  useWorkspaceStore,
} from "@/entities/schema";
import { openSample } from "@/features/load-schema";
import { toggleWorkload, useWorkloadView } from "@/features/show-workload";
import { formatCombo, type KeyCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { CommandGroup, CommandItem, CommandShortcut } from "@/shared/ui/command";

interface CanvasCommand {
  id: CanvasCommandId;
  label: string;
  icon: LucideIcon;
  combo?: KeyCombo;
}

const COMMANDS: CanvasCommand[] = [
  {
    id: "focusSearch",
    label: "Find a table or column on the canvas",
    icon: Search,
    combo: SHORTCUTS.canvasSearch.combo,
  },
  { id: "toggleFocus", label: "Focus on the selected table", icon: Focus, combo: SHORTCUTS.canvasFocusMode.combo },
  { id: "fitView", label: "Fit all tables in view", icon: Maximize },
  { id: "layoutLayered", label: "Layout: layered", icon: Network },
  { id: "layoutForce", label: "Layout: force", icon: Orbit },
  { id: "layoutGrid", label: "Layout: grid", icon: LayoutGrid },
  { id: "resetLayout", label: "Reset layout", icon: RotateCcw },
  { id: "collapseAll", label: "Collapse all tables to key columns", icon: ChevronsDownUp },
  { id: "expandAll", label: "Expand all tables", icon: ChevronsUpDown },
  { id: "toggleInferred", label: "Show or hide inferred foreign keys", icon: Waypoints },
  { id: "toggleMinimap", label: "Show or hide the minimap", icon: MapIcon },
];

/** Canvas commands appear only while a canvas is mounted to run them. */
export function CanvasCommandItems({ run }: { run: (action: () => void) => () => void }) {
  const available = useCanvasCommands((s) => s.handlers);
  const sample = useWorkspaceStore((s) => s.source === "sample");
  const workloadOn = useWorkloadView((s) => s.on);

  return (
    <CommandGroup heading="Canvas">
      {COMMANDS.filter((c) => available[c.id]).map((c) => (
        <CommandItem key={c.id} value={`canvas ${c.label}`} onSelect={run(() => runCanvasCommand(c.id))}>
          <c.icon aria-hidden />
          {c.label}
          {c.combo && <CommandShortcut className="font-mono tracking-normal">{formatCombo(c.combo)}</CommandShortcut>}
        </CommandItem>
      ))}
      {!sample && Object.keys(available).length > 0 && (
        <CommandItem value="workload heat pg_stat_statements traffic hot tables" onSelect={run(toggleWorkload)}>
          <Activity aria-hidden />
          {workloadOn ? "Hide the workload on the canvas" : "Show the workload on the canvas"}
        </CommandItem>
      )}
      {sample ? (
        <CommandItem value="exit sample schema" onSelect={run(closeSampleWorkspace)}>
          <LogOut aria-hidden />
          Exit the sample schema
        </CommandItem>
      ) : (
        <CommandItem value="open sample schema" onSelect={run(openSample)}>
          <FlaskConical aria-hidden />
          Open sample schema
        </CommandItem>
      )}
    </CommandGroup>
  );
}
