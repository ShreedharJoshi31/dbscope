import { cn } from "cn";
import type { LucideIcon } from "lucide-react";
import { ariaKeyShortcut, type KeyCombo } from "@/shared/config/shortcuts";
import { ShortcutKeys } from "@/shared/ui/shortcut-keys";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";

interface RailButtonProps {
  icon: LucideIcon;
  label: string;
  combo?: KeyCombo;
  pressed?: boolean;
  current?: boolean;
  onClick: () => void;
}

export function RailButton({ icon: Icon, label, combo, pressed, current, onClick }: RailButtonProps) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            aria-label={label}
            aria-pressed={pressed}
            aria-current={current ? "page" : undefined}
            aria-keyshortcuts={combo ? ariaKeyShortcut(combo) : undefined}
            onClick={onClick}
            className={cn(
              "relative flex size-9 items-center justify-center rounded-md text-text-muted transition-colors",
              "hover:bg-surface-hover hover:text-text-primary",
              (pressed || current) && "text-text-primary",
              current && "bg-surface-selected",
              pressed &&
                "before:absolute before:top-2 before:bottom-2 before:-left-1.5 before:w-0.5 before:rounded-full before:bg-accent-fg",
            )}
          />
        }
      >
        <Icon aria-hidden className="size-4" strokeWidth={1.75} />
      </TooltipTrigger>
      <TooltipContent side="right" className="gap-2">
        {label}
        {combo && <ShortcutKeys combo={combo} />}
      </TooltipContent>
    </Tooltip>
  );
}
