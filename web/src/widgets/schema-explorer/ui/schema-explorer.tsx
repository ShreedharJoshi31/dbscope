import { ListTree, PanelLeftClose } from "lucide-react";
import { summarizeTest, useActiveConnection } from "@/entities/connection";
import { togglePanel } from "@/features/toggle-panel";
import { useRetestSavedProfile } from "@/shared/api";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { announce } from "@/shared/lib/announcer";
import { EmptyState } from "@/shared/ui/empty-state";
import { PanelHeader } from "@/shared/ui/panel-header";

export function SchemaExplorer() {
  const { profile } = useActiveConnection();
  const retest = useRetestSavedProfile();

  const testConnection = () => {
    if (!profile) return;
    retest.mutate(profile.id, {
      onSuccess: (result) => announce(summarizeTest(result)),
      onError: (error) => announce(`Connection test could not run: ${error.message}`),
    });
  };

  return (
    <>
      <PanelHeader
        title="Schema explorer"
        close={{
          label: "Hide schema explorer",
          icon: PanelLeftClose,
          combo: SHORTCUTS.toggleExplorer.combo,
          onClick: () => togglePanel("explorer"),
        }}
      />
      <EmptyState
        icon={ListTree}
        title="No schema loaded yet"
        description={`Schemas, tables, views and their columns${profile ? ` in ${profile.database}` : ""} will be listed here once dbscope reads the schema. Meanwhile you can check that the connection works.`}
        action={{ label: retest.isPending ? "Testing…" : "Test connection", onClick: testConnection }}
      />
    </>
  );
}
