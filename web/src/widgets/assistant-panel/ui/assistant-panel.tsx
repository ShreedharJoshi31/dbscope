import { BookText, LoaderCircle, MessageSquarePlus, PanelRightClose, Settings2, Sparkles } from "lucide-react";
import { type ReactNode, useEffect, useRef } from "react";
import { useThread } from "@/entities/assistant-turn";
import { engineLabel, useActiveConnection } from "@/entities/connection";
import { useWorkspaceStore } from "@/entities/schema";
import { useAssistantActions } from "@/features/ask-assistant";
import { openGlossary } from "@/features/edit-glossary";
import { openLlmSettings } from "@/features/llm-settings";
import { togglePanel } from "@/features/toggle-panel";
import { type LLMSettings, type Profile, useLlmSettings } from "@/shared/api";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";
import { PanelHeader } from "@/shared/ui/panel-header";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";
import { Composer } from "@/widgets/assistant-panel/ui/composer";
import { TurnCard } from "@/widgets/assistant-panel/ui/turn-card";

const SUMMARY_LABEL = {
  off: "schema only",
  profile: "the schema only (result summaries are counted locally)",
  rows: "schema and sample rows",
};

function HeaderButton({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger render={<Button variant="ghost" size="icon-xs" aria-label={label} onClick={onClick} />}>
        {children}
      </TooltipTrigger>
      <TooltipContent side="bottom">{label}</TooltipContent>
    </Tooltip>
  );
}

function modelLine(settings: LLMSettings): string {
  if (settings.provider === "anthropic") return `${settings.model} via Anthropic`;
  return `${settings.model} at ${settings.baseUrl ?? "an OpenAI-compatible endpoint"}`;
}

function Thread({ profile, settings }: { profile: Profile; settings: LLMSettings }) {
  const thread = useThread(profile.id);
  const actions = useAssistantActions(profile.id);
  const streaming = thread.turns.some((t) => t.status === "streaming");
  const listRef = useRef<HTMLDivElement>(null);
  const count = thread.turns.length;

  // Bring a new question into view; later updates to it do not move the scroll position.
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs when a turn is added, not on every update to one.
  useEffect(() => {
    // Scroll only the list: scrollIntoView would also scroll the page and the panel group.
    const list = listRef.current;
    const last = list?.lastElementChild;
    if (list && last instanceof HTMLElement) list.scrollTop = last.offsetTop - 8;
  }, [count]);

  return (
    <>
      {count === 0 ? (
        <EmptyState
          icon={Sparkles}
          title="Ask about your data in plain English"
          description={`Using ${modelLine(settings)}. The assistant writes one read-only SELECT, explains it and runs it through the same safety checks as the editor. For ${profile.name} it sends ${SUMMARY_LABEL[profile.summaryMode]}.`}
        />
      ) : (
        <div
          ref={listRef}
          role="log"
          aria-label="Conversation"
          className="relative flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-2"
        >
          {thread.turns.map((turn) => (
            <TurnCard key={turn.id} turn={turn} actions={actions} />
          ))}
        </div>
      )}
      <Composer disabled={false} streaming={streaming} onSend={(q) => void actions.ask(q)} onStop={actions.stop} />
    </>
  );
}

function Body() {
  const { profile } = useActiveConnection();
  const sample = useWorkspaceStore((s) => s.source === "sample");
  const settings = useLlmSettings();

  if (sample || !profile) {
    return (
      <EmptyState
        icon={Sparkles}
        title="Connect a database to ask questions"
        description={
          sample
            ? "The sample schema has no data to query. Add a connection, then ask questions in plain English; the assistant writes SQL you can review."
            : "Choose a connection first. Each connection has its own conversation and glossary."
        }
      />
    );
  }
  if (!profile.capabilities.ask) {
    return (
      <EmptyState
        icon={Sparkles}
        title={`Questions are not available for ${engineLabel(profile.engine)} yet`}
        description="Write SQL in a query tab instead; it runs read-only against this database."
      />
    );
  }
  if (settings.isPending) {
    return (
      <p role="status" className="flex items-center gap-2 p-3 text-xs text-text-muted">
        <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
        Loading the assistant settings…
      </p>
    );
  }
  if (settings.isError) {
    return (
      <EmptyState
        icon={Sparkles}
        title={
          settings.error.code === "secrets_locked"
            ? "Unlock the vault to use the assistant"
            : "Assistant settings unavailable"
        }
        description={settings.error.message}
        action={{ label: "Try again", onClick: () => void settings.refetch() }}
      />
    );
  }
  if (settings.data.provider === "none" || (settings.data.provider === "anthropic" && !settings.data.hasApiKey)) {
    return (
      <EmptyState
        icon={Sparkles}
        title="Set up the assistant"
        description="Choose a model: your own Anthropic API key, or an OpenAI-compatible server such as Ollama on this machine. dbscope shows exactly what it sends before you ask."
        action={{ label: "Choose a model…", onClick: openLlmSettings }}
      />
    );
  }
  return <Thread key={profile.id} profile={profile} settings={settings.data} />;
}

export function AssistantPanel() {
  const { profile } = useActiveConnection();
  const actions = useAssistantActions(profile?.id);
  const combo = SHORTCUTS.toggleAssistant.combo;

  return (
    <>
      <PanelHeader
        title="Assistant"
        close={{ label: "Hide assistant", icon: PanelRightClose, combo, onClick: () => togglePanel("assistant") }}
      >
        {profile && (
          <>
            <HeaderButton label="New conversation" onClick={actions.newConversation}>
              <MessageSquarePlus aria-hidden />
            </HeaderButton>
            <HeaderButton label={`Glossary for ${profile.name}`} onClick={openGlossary}>
              <BookText aria-hidden />
            </HeaderButton>
          </>
        )}
        <HeaderButton label="Assistant model settings" onClick={openLlmSettings}>
          <Settings2 aria-hidden />
        </HeaderButton>
      </PanelHeader>
      <div className="flex min-h-0 flex-1 flex-col">
        <Body />
      </div>
    </>
  );
}
