import { Keyboard, ListTree, Network, PanelBottom, Sparkles } from "lucide-react";
import { openShortcutSheet } from "@/features/show-shortcuts";
import { togglePanel, useIsPanelVisible } from "@/features/toggle-panel";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { RailButton } from "@/widgets/app-shell/ui/rail-button";

interface NavRailProps {
  onShowErd: () => void;
}

export function NavRail({ onShowErd }: NavRailProps) {
  const explorer = useIsPanelVisible("explorer");
  const dock = useIsPanelVisible("dock");
  const assistant = useIsPanelVisible("assistant");

  return (
    <nav
      aria-label="Workbench"
      data-region="rail"
      tabIndex={-1}
      className="flex w-12 shrink-0 flex-col items-center gap-1 border-r border-border-default bg-surface-subtle py-2"
    >
      <RailButton icon={Network} label="ERD canvas" current onClick={onShowErd} />
      <RailButton
        icon={ListTree}
        label={SHORTCUTS.toggleExplorer.label}
        combo={SHORTCUTS.toggleExplorer.combo}
        pressed={explorer}
        onClick={() => togglePanel("explorer")}
      />
      <RailButton
        icon={PanelBottom}
        label={SHORTCUTS.toggleDock.label}
        combo={SHORTCUTS.toggleDock.combo}
        pressed={dock}
        onClick={() => togglePanel("dock")}
      />
      <RailButton
        icon={Sparkles}
        label={SHORTCUTS.toggleAssistant.label}
        combo={SHORTCUTS.toggleAssistant.combo}
        pressed={assistant}
        onClick={() => togglePanel("assistant")}
      />
      <div className="mt-auto">
        <RailButton
          icon={Keyboard}
          label={SHORTCUTS.shortcutSheet.label}
          combo={SHORTCUTS.shortcutSheet.combo}
          onClick={openShortcutSheet}
        />
      </div>
    </nav>
  );
}
