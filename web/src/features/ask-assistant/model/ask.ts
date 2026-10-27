import { useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import {
  createTurn,
  getThread,
  reduceTurn,
  resendTurn,
  type Turn,
  turnAnnouncement,
  useThreadStore,
} from "@/entities/assistant-turn";
import { nextRunId, useRunStore } from "@/entities/query";
import { highlightTables, showQueryOverlay } from "@/entities/schema";
import {
  ApiError,
  type AskEvent,
  type AskRequest,
  type Assumption,
  askStream,
  isApiError,
  llmQueryKeys,
  queryApiKeys,
} from "@/shared/api";
import { announce } from "@/shared/lib/announcer";

/** One stream per connection; a new question stops the previous one. */
const controllers = new Map<string, AbortController>();

export interface AskHooks {
  /** After every stream: refresh history (asks with SQL are recorded) and the glossary. */
  onSettled?: (profileId: string) => void;
}

function sideEffects(profileId: string, event: AskEvent): void {
  const threads = useThreadStore.getState();
  switch (event.type) {
    case "answer":
      if (event.answer.tables.length > 0) highlightTables(event.answer.tables);
      break;
    case "result":
      showQueryOverlay(event.result.overlay);
      break;
    case "needs_confirmation":
      threads.setConversation(profileId, event.confirmation.conversationId);
      break;
    case "done":
      threads.setConversation(profileId, event.done.conversationId);
      break;
  }
}

/** Streams POST /ask into a turn already in the thread. */
export async function streamTurn(profileId: string, turnId: string, request: AskRequest, hooks: AskHooks = {}) {
  controllers.get(profileId)?.abort();
  const ac = new AbortController();
  controllers.set(profileId, ac);
  const { updateTurn } = useThreadStore.getState();
  const apply = (update: (turn: Turn) => Turn) => updateTurn(profileId, turnId, update);
  try {
    for await (const event of askStream(profileId, request, ac.signal)) {
      apply((turn) => reduceTurn(turn, event));
      sideEffects(profileId, event);
    }
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      apply((turn) => ({ ...turn, status: "stopped", stage: null }));
    } else {
      const apiError = isApiError(error) ? error : new ApiError("internal", String(error), 0);
      apply((turn) => ({ ...turn, status: "error", error: apiError, stage: null }));
    }
  } finally {
    if (controllers.get(profileId) === ac) controllers.delete(profileId);
    // A stream that closed without done, error or a confirmation request was cut off.
    apply((turn) =>
      turn.status === "streaming"
        ? {
            ...turn,
            status: "error",
            stage: null,
            error: new ApiError("internal", "The answer stream ended before the agent finished.", 0),
          }
        : turn,
    );
    const turn = getThread(profileId).turns.find((t) => t.id === turnId);
    const message = turn ? turnAnnouncement(turn) : null;
    if (message) announce(message);
    hooks.onSettled?.(profileId);
  }
}

export function stopAsk(profileId: string): boolean {
  const ac = controllers.get(profileId);
  if (!ac) return false;
  ac.abort();
  return true;
}

function startTurn(profileId: string, request: AskRequest, previousSql: string | null, hooks: AskHooks) {
  const turn = createTurn(request, { previousSql });
  useThreadStore.getState().addTurn(profileId, turn);
  return streamTurn(profileId, turn.id, request, hooks);
}

function withConversation(profileId: string, request: AskRequest): AskRequest {
  const id = getThread(profileId).conversationId;
  return id ? { ...request, conversationId: id } : request;
}

export function askQuestion(profileId: string, question: string, hooks: AskHooks = {}) {
  return startTurn(profileId, withConversation(profileId, { question: question.trim() }), null, hooks);
}

/** Regenerates the answer honouring the user's edited assumption chips. */
export function reaskWithAssumptions(profileId: string, turn: Turn, overrides: Assumption[], hooks: AskHooks = {}) {
  const request = withConversation(profileId, { question: turn.question, assumptionOverrides: overrides });
  return startTurn(profileId, request, turn.answer?.sql ?? null, hooks);
}

/** Answers the assistant's clarifying question; `remember` saves it to the glossary. */
export function answerClarification(
  profileId: string,
  turn: Turn,
  answer: string,
  remember: boolean,
  hooks: AskHooks = {},
) {
  const question = turn.answer?.clarifyingQuestion?.question ?? "";
  const request = withConversation(profileId, {
    question: turn.question,
    clarification: { question, answer: answer.trim(), ...(remember ? { saveToGlossary: true } : {}) },
  });
  return startTurn(profileId, request, null, hooks);
}

/**
 * Re-sends a turn that stopped for a confirmation: the same question and
 * conversation with the confirm flag, so the agent reuses the answer it
 * already showed (no new model call). Earlier confirmations are kept.
 */
export function confirmTurn(profileId: string, turn: Turn, hooks: AskHooks = {}) {
  const c = turn.confirmation;
  if (!c) return Promise.resolve();
  const request: AskRequest = {
    question: turn.question,
    conversationId: c.conversationId,
    ...(turn.request.maxRows ? { maxRows: turn.request.maxRows } : {}),
    ...(turn.request.confirmCost || c.reason === "cost" ? { confirmCost: true } : {}),
    ...(turn.request.confirmRows || c.reason === "rows" ? { confirmRows: true } : {}),
  };
  useThreadStore.getState().updateTurn(profileId, turn.id, (t) => resendTurn(t, request));
  return streamTurn(profileId, turn.id, request, hooks);
}

/** The user declined: the turn ends without running. */
export function declineTurn(profileId: string, turn: Turn) {
  useThreadStore.getState().updateTurn(profileId, turn.id, (t) => ({ ...t, status: "stopped", stage: null }));
  announce("Not run.");
}

/** "Open in results": the turn's full result in the dock, with its plan and executed SQL. */
export function showTurnResult(turn: Turn): void {
  const result = turn.result;
  if (!result) return;
  useRunStore.getState().show({
    meta: {
      id: nextRunId(),
      kind: "query",
      analyze: false,
      sql: turn.answer?.sql ?? result.executedSql,
      tabId: null,
      source: "assistant",
      startedAt: turn.startedAt,
    },
    result,
    executedSql: result.executedSql,
    plan: result.plan,
    overlay: result.overlay,
    warnings: result.warnings,
  });
  showQueryOverlay(result.overlay);
}

/** Ask actions bound to the query client, so history and glossary refresh after each answer. */
export function useAssistantActions(profileId: string | undefined) {
  const client = useQueryClient();
  return useMemo(() => {
    const hooks: AskHooks = {
      onSettled: (id) => {
        void client.invalidateQueries({ queryKey: queryApiKeys.history(id) });
        void client.invalidateQueries({ queryKey: llmQueryKeys.glossary(id) });
      },
    };
    const id = profileId ?? "";
    return {
      ask: (question: string) => askQuestion(id, question, hooks),
      reask: (turn: Turn, overrides: Assumption[]) => reaskWithAssumptions(id, turn, overrides, hooks),
      clarify: (turn: Turn, answer: string, remember: boolean) =>
        answerClarification(id, turn, answer, remember, hooks),
      confirm: (turn: Turn) => confirmTurn(id, turn, hooks),
      decline: (turn: Turn) => declineTurn(id, turn),
      stop: () => stopAsk(id),
      newConversation: () => {
        stopAsk(id);
        useThreadStore.getState().newConversation(id);
        highlightTables(null);
      },
    };
  }, [client, profileId]);
}
