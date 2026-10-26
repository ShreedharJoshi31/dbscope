import { PanelRightClose, Sparkles } from "lucide-react";
import { useActiveConnection } from "@/entities/connection";
import { togglePanel } from "@/features/toggle-panel";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { EmptyState } from "@/shared/ui/empty-state";
import { PanelHeader } from "@/shared/ui/panel-header";

const SUMMARY_LABEL = { off: "schema only", profile: "schema and result profiles", rows: "schema and sample rows" };

export function AssistantPanel() {
  const { profile } = useActiveConnection();
  const hide = () => togglePanel("assistant");
  const combo = SHORTCUTS.toggleAssistant.combo;

  return (
    <>
      <PanelHeader title="Assistant" close={{ label: "Hide assistant", icon: PanelRightClose, combo, onClick: hide }} />
      <EmptyState
        icon={Sparkles}
        title="Ask about your data in plain English"
        description={`The assistant will turn questions into SQL that you review before it runs. It names the model it uses and what it sends${profile ? `; for ${profile.name} that is ${SUMMARY_LABEL[profile.summaryMode]}` : ""}.`}
        action={{ label: "Hide assistant", onClick: hide, combo }}
      />
    </>
  );
}
