import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { ariaKeyShortcut, formatCombo, type KeyCombo } from "@/shared/config/shortcuts";
import { Button } from "@/shared/ui/button";
import { ShortcutKeys } from "@/shared/ui/shortcut-keys";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";

interface PanelHeaderProps {
  title: ReactNode;
  titleId?: string;
  children?: ReactNode;
  close?: { label: string; icon: LucideIcon; combo: KeyCombo; onClick: () => void };
}

/** 32px header shared by explorer, dock and assistant. */
export function PanelHeader({ title, titleId, children, close }: PanelHeaderProps) {
  return (
    <div className="flex h-8 shrink-0 items-center gap-2 border-b border-border-default px-2">
      <h2 id={titleId} className="truncate text-2xs font-semibold tracking-wide text-text-muted uppercase">
        {title}
      </h2>
      <div className="ml-auto flex items-center gap-1">
        {children}
        {close && (
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label={`${close.label} (${formatCombo(close.combo)})`}
                  aria-keyshortcuts={ariaKeyShortcut(close.combo)}
                  onClick={close.onClick}
                />
              }
            >
              <close.icon aria-hidden />
            </TooltipTrigger>
            <TooltipContent side="bottom" className="gap-2">
              {close.label} <ShortcutKeys combo={close.combo} />
            </TooltipContent>
          </Tooltip>
        )}
      </div>
    </div>
  );
}
