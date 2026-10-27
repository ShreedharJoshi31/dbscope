import { Send } from "lucide-react";
import { SENDS_LABEL, type Turn } from "@/entities/assistant-turn";
import type { AskContext } from "@/shared/api";

const PROVIDER_NAME: Record<string, string> = { anthropic: "Anthropic", openai_compat: "OpenAI-compatible endpoint" };

export function providerLabel(context: AskContext | null, baseUrl?: string): string {
  if (!context) return "your LLM provider";
  if (context.provider === "openai_compat" && baseUrl) return `${context.model} at ${baseUrl}`;
  return `${context.model} (${PROVIDER_NAME[context.provider] ?? context.provider})`;
}

/** What was sent so far: result data only once the summary step ran. */
function sentNow(turn: Turn, context: AskContext): string {
  if (turn.summary?.mode === "rows") return SENDS_LABEL["schema+rows"];
  if (turn.status === "streaming" && context.sends === "schema+rows") {
    return "schema so far (sample rows after the query runs)";
  }
  return SENDS_LABEL.schema;
}

/** "Sent to claude-opus-5-5 (Anthropic): schema only · 12 tables · omitted columns: …" (UX rule 14). */
export function ContextLine({ turn, baseUrl }: { turn: Turn; baseUrl?: string | undefined }) {
  const context = turn.context;
  if (!context) return null;
  const tables = context.tables.length;
  const omitted = context.omittedColumns ?? [];
  return (
    <p data-section="context" data-testid="context-line" className="flex items-start gap-1 text-2xs text-text-muted">
      <Send aria-hidden className="mt-0.5 size-3 shrink-0" />
      <span>
        Sent to {providerLabel(context, baseUrl)}: {sentNow(turn, context)} ·{" "}
        {context.schemaMode === "full" ? `full schema, ${tables} tables` : `${tables} relevant tables`}
        {omitted.length > 0 && (
          <>
            {" "}
            · omitted columns: <span className="font-mono">{omitted.join(", ")}</span>
          </>
        )}
      </span>
    </p>
  );
}
