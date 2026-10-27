import { CircleHelp, ScanSearch } from "lucide-react";
import type { ReactNode } from "react";
import { useConnectionDialog } from "@/features/add-connection";
import { useDeleteConnection } from "@/features/delete-connection";
import { SampleBadge } from "@/features/load-schema";
import { openShortcutSheet } from "@/features/show-shortcuts";
import { ConnectionSwitcher } from "@/features/switch-connection";
import { ThemeToggle } from "@/features/toggle-theme";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { Button } from "@/shared/ui/button";
import { ShortcutKeys } from "@/shared/ui/shortcut-keys";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";

export function TopBar({ search }: { search: ReactNode }) {
  const { openCreate, openEdit } = useConnectionDialog();
  const openDelete = useDeleteConnection((s) => s.open);

  return (
    <div className="grid h-10 shrink-0 grid-cols-[1fr_minmax(0,28rem)_1fr] items-center gap-2 border-b border-border-default bg-surface-subtle px-2">
      <div className="flex min-w-0 items-center gap-2">
        <span className="flex items-center gap-1 px-1 text-sm font-semibold tracking-tight">
          <ScanSearch aria-hidden className="size-4 text-accent-fg" />
          dbscope
        </span>
        <span aria-hidden className="h-4 w-px bg-border-default" />
        <ConnectionSwitcher onNew={openCreate} onEdit={openEdit} onDelete={openDelete} />
        <SampleBadge />
      </div>
      <div className="min-w-0">{search}</div>
      <div className="flex items-center justify-end gap-1">
        <ThemeToggle />
        <Tooltip>
          <TooltipTrigger
            render={
              <Button variant="ghost" size="icon-sm" aria-label="Keyboard shortcuts" onClick={openShortcutSheet} />
            }
          >
            <CircleHelp aria-hidden />
          </TooltipTrigger>
          <TooltipContent side="bottom" className="gap-2">
            Keyboard shortcuts <ShortcutKeys combo={SHORTCUTS.shortcutSheet.combo} />
          </TooltipContent>
        </Tooltip>
      </div>
    </div>
  );
}
