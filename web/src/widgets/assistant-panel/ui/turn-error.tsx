import { Settings2 } from "lucide-react";
import { describeAskError, type Turn } from "@/entities/assistant-turn";
import { useActiveConnection } from "@/entities/connection";
import { QueryErrorView } from "@/entities/query";
import { openLlmSettings } from "@/features/llm-settings";
import { Button } from "@/shared/ui/button";
import { StatusIcon } from "@/shared/ui/status-icon";

/** Provider failures in plain words with a settings link; SQL failures shown like the Messages tab. */
export function TurnError({ turn }: { turn: Turn }) {
  const { profile } = useActiveConnection();
  const error = turn.error;
  if (!error) return null;
  const copy = describeAskError(error);
  if (copy.kind === "query") {
    return (
      <section data-section="error" aria-label="Error" className="rounded-md border border-status-danger-fg p-2">
        <QueryErrorView
          error={error}
          submittedSql={turn.answer?.sql ?? ""}
          statementTimeoutMs={profile?.statementTimeoutMs}
        />
      </section>
    );
  }
  return (
    <section
      data-section="error"
      role="alert"
      data-error-kind={error.detail ?? error.code}
      className="flex flex-col gap-1 rounded-md border border-status-danger-fg p-2 text-sm"
    >
      <p className="flex items-start gap-1.5 font-medium text-status-danger-fg">
        <StatusIcon tone="danger" className="mt-0.5" />
        {copy.title}
      </p>
      <p className="text-xs text-text-muted">{copy.hint}</p>
      {copy.kind === "llm" && <p className="font-mono text-2xs break-words whitespace-pre-wrap">{error.message}</p>}
      {copy.settings && (
        <Button variant="outline" size="xs" className="self-start" onClick={openLlmSettings}>
          <Settings2 aria-hidden />
          Open assistant settings
        </Button>
      )}
    </section>
  );
}
