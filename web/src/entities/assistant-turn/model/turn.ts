import {
  type ApiError,
  type AskAnswer,
  type AskConfirmation,
  type AskContext,
  type AskDone,
  type AskEvent,
  type AskRequest,
  type AskStage,
  type AskSummary,
  type Clarification,
  errorFromBody,
  type QueryResult,
} from "@/shared/api";

export type TurnStatus = "streaming" | "done" | "error" | "confirm" | "stopped";

/** One question and everything the agent streamed back for it. */
export interface Turn {
  id: string;
  question: string;
  /** The body sent; confirmations and re-asks build on it. */
  request: AskRequest;
  status: TurnStatus;
  stage: AskStage | null;
  context: AskContext | null;
  answer: AskAnswer | null;
  /** The SQL of the answer this turn re-asked (an edited assumption), for the diff. */
  previousSql: string | null;
  /** The user's answer to a clarifying question, when this turn sent one. */
  clarification: Clarification | null;
  confirmation: AskConfirmation | null;
  result: QueryResult | null;
  summary: AskSummary | null;
  done: AskDone | null;
  error: ApiError | null;
  startedAt: number;
}

let turnCounter = 0;

export function createTurn(request: AskRequest, extra: Partial<Pick<Turn, "previousSql">> = {}): Turn {
  turnCounter += 1;
  return {
    id: `t${turnCounter}`,
    question: request.question,
    request,
    status: "streaming",
    stage: null,
    context: null,
    answer: null,
    previousSql: extra.previousSql ?? null,
    clarification: request.clarification ?? null,
    confirmation: null,
    result: null,
    summary: null,
    done: null,
    error: null,
    startedAt: Date.now(),
  };
}

/** Applies one SSE event. Pure, so the stream order can be tested without React. */
export function reduceTurn(turn: Turn, event: AskEvent): Turn {
  switch (event.type) {
    case "status":
      return { ...turn, stage: event.stage };
    case "context":
      return { ...turn, context: event.context };
    case "answer":
      return { ...turn, answer: event.answer };
    case "needs_confirmation":
      return { ...turn, status: "confirm", confirmation: event.confirmation, stage: null };
    case "result":
      return { ...turn, result: event.result };
    case "summary":
      return { ...turn, summary: event.summary };
    case "done":
      return { ...turn, status: "done", done: event.done, stage: null };
    case "error":
      return { ...turn, status: "error", error: errorFromBody(event.error, 200), stage: null };
  }
}

/** Resets a turn for a confirmation re-send: same question, same answer, new run. */
export function resendTurn(turn: Turn, request: AskRequest): Turn {
  return {
    ...turn,
    request,
    status: "streaming",
    stage: null,
    confirmation: null,
    result: null,
    summary: null,
    done: null,
    error: null,
  };
}

/** The live region text when a turn ends (UX rule 13: never per token). */
export function turnAnnouncement(turn: Turn): string | null {
  switch (turn.status) {
    case "done":
      return turn.answer?.kind === "clarify" ? "Answer ready: the assistant asks a question" : "Answer ready";
    case "error":
      return "Answer failed";
    case "confirm":
      return "Answer needs your confirmation";
    case "stopped":
      return "Answer stopped";
    default:
      return null;
  }
}

export const STAGE_LABEL: Record<AskStage, string> = {
  retrieving: "Reading the schema…",
  generating: "Writing SQL…",
  checking: "Checking the SQL…",
  running: "Running the query…",
  repairing: "Fixing the SQL after an error…",
  summarizing: "Summarizing the result…",
};

export const SENDS_LABEL: Record<AskContext["sends"], string> = {
  schema: "schema only",
  "schema+rows": "schema + sample rows",
};
