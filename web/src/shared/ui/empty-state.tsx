import type { LucideIcon } from "lucide-react";
import type { KeyCombo } from "@/shared/config/shortcuts";
import { Button } from "@/shared/ui/button";
import { ShortcutKeys } from "@/shared/ui/shortcut-keys";

interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  description: string;
  action?: { label: string; onClick: () => void; combo?: KeyCombo };
}

/** What belongs here, one primary action, and its shortcut (UX rule 8). */
export function EmptyState({ icon: Icon, title, description, action }: EmptyStateProps) {
  return (
    <div className="flex h-full min-h-0 flex-col items-center justify-center gap-3 overflow-auto p-6 text-center">
      <Icon aria-hidden className="size-6 text-text-muted" strokeWidth={1.5} />
      <div className="flex max-w-80 flex-col gap-1">
        <h3 className="text-sm font-semibold text-text-primary">{title}</h3>
        <p className="text-xs text-text-muted">{description}</p>
      </div>
      {action && (
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={action.onClick}>
            {action.label}
          </Button>
          {action.combo && <ShortcutKeys combo={action.combo} />}
        </div>
      )}
    </div>
  );
}
