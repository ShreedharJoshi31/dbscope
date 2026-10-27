import { LoaderCircle } from "lucide-react";
import { STAGE_LABEL, type Turn } from "@/entities/assistant-turn";
import type { useAssistantActions } from "@/features/ask-assistant";
import { useLlmSettings } from "@/shared/api";
import { AnswerSql } from "@/widgets/assistant-panel/ui/answer-sql";
import { AssumptionChips } from "@/widgets/assistant-panel/ui/assumption-chips";
import { ClarifyQuestion } from "@/widgets/assistant-panel/ui/clarify-question";
import { ConfirmBox } from "@/widgets/assistant-panel/ui/confirm-box";
import { ContextLine, providerLabel } from "@/widgets/assistant-panel/ui/context-line";
import { FactBadges } from "@/widgets/assistant-panel/ui/fact-badges";
import { TurnError } from "@/widgets/assistant-panel/ui/turn-error";
import { TurnResult } from "@/widgets/assistant-panel/ui/turn-result";

type Actions = ReturnType<typeof useAssistantActions>;

function Question({ turn }: { turn: Turn }) {
  const overrides = turn.request.assumptionOverrides;
  return (
    <header data-section="question" className="flex flex-col gap-0.5">
      <p className="text-sm font-medium whitespace-pre-wrap text-text-primary">
        <span className="sr-only">You asked: </span>
        {turn.question}
      </p>
      {turn.clarification && (
        <p className="text-xs text-text-muted">
          You answered “{turn.clarification.question}” with “{turn.clarification.answer}”.
        </p>
      )}
      {overrides && overrides.length > 0 && (
        <p className="text-xs text-text-muted">
          Asked again with your assumptions: {overrides.map((o) => o.label).join("; ")}.
        </p>
      )}
    </header>
  );
}

function Progress({ turn }: { turn: Turn }) {
  if (turn.status !== "streaming") return null;
  return (
    <p data-testid="turn-progress" className="flex items-center gap-1.5 text-xs text-text-muted">
      <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
      {turn.stage ? STAGE_LABEL[turn.stage] : "Sending the question…"}
    </p>
  );
}

function Steps({ steps }: { steps: readonly string[] }) {
  if (steps.length === 0) return null;
  return (
    <section data-section="explanation" aria-label="How the SQL answers the question">
      <ol className="flex list-decimal flex-col gap-0.5 pl-5 text-xs text-text-primary">
        {steps.map((step, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: steps are an ordered list of plain strings that can repeat.
          <li key={i} className="whitespace-pre-wrap">
            {step}
          </li>
        ))}
      </ol>
    </section>
  );
}

function Summary({ turn }: { turn: Turn }) {
  if (!turn.summary) return null;
  return (
    <section data-section="summary" aria-label="Summary" className="flex flex-col gap-0.5">
      <p className="text-sm whitespace-pre-wrap text-text-primary">{turn.summary.text}</p>
      <p className="text-2xs text-text-muted">
        {turn.summary.mode === "rows"
          ? "Written by the model from sample rows."
          : "Counted by dbscope from the result; nothing was sent to the model."}
      </p>
    </section>
  );
}

/**
 * One answer card, in the order of UX rule 5: question → SQL → explanation →
 * assumptions → clarifying question → fact badges, then result and summary.
 * Model text is rendered as React text only (no markdown, links or images).
 */
export function TurnCard({ turn, actions }: { turn: Turn; actions: Actions }) {
  const settings = useLlmSettings();
  const answer = turn.answer;
  const busy = turn.status === "streaming";
  return (
    <article
      data-testid="answer-card"
      data-status={turn.status}
      aria-label={`Answer to: ${turn.question}`}
      aria-busy={busy}
      className="flex flex-col gap-2 rounded-md border border-border-default bg-surface-raised p-3"
    >
      <Question turn={turn} />
      <ContextLine turn={turn} baseUrl={settings.data?.baseUrl} />
      {answer?.kind === "sql" && answer.sql && (
        <AnswerSql sql={answer.sql} previousSql={turn.previousSql} busy={busy} />
      )}
      {answer && <Steps steps={answer.explanationSteps} />}
      {answer && (
        <AssumptionChips
          assumptions={answer.assumptions}
          disabled={busy}
          onReask={(overrides) => void actions.reask(turn, overrides)}
        />
      )}
      {answer?.kind === "clarify" && answer.clarifyingQuestion && (
        <ClarifyQuestion
          question={answer.clarifyingQuestion.question}
          options={answer.clarifyingQuestion.options}
          disabled={busy}
          onAnswer={(text, remember) => void actions.clarify(turn, text, remember)}
        />
      )}
      <FactBadges turn={turn} />
      <Progress turn={turn} />
      {turn.status === "confirm" && (
        <ConfirmBox
          turn={turn}
          destination={providerLabel(turn.context, settings.data?.baseUrl)}
          onConfirm={() => void actions.confirm(turn)}
          onDecline={() => actions.decline(turn)}
        />
      )}
      <TurnResult turn={turn} />
      <Summary turn={turn} />
      <TurnError turn={turn} />
      {turn.status === "stopped" && <p className="text-xs text-text-muted">Stopped. Nothing more was run.</p>}
    </article>
  );
}
