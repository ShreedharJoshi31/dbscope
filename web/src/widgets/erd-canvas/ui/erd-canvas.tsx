import { Network } from "lucide-react";
import { useActiveConnection } from "@/entities/connection";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { EmptyState } from "@/shared/ui/empty-state";

interface ErdCanvasProps {
  onOpenCommandPalette: () => void;
}

export function ErdCanvas({ onOpenCommandPalette }: ErdCanvasProps) {
  const { profile } = useActiveConnection();
  return (
    <EmptyState
      icon={Network}
      title="The ERD canvas is empty"
      description={`Every table${profile ? ` in ${profile.database}` : ""} and its foreign keys will be drawn here once dbscope reads the schema.`}
      action={{
        label: "Search or run a command",
        onClick: onOpenCommandPalette,
        combo: SHORTCUTS.commandPalette.combo,
      }}
    />
  );
}
