import { waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createTurn, getThread, reduceTurn, useThreadStore } from "@/entities/assistant-turn";
import { useCanvasStore } from "@/entities/schema";
import { answerClarification, askQuestion, confirmTurn, reaskWithAssumptions, stopAsk } from "@/features/ask-assistant";
import type { AskRequest } from "@/shared/api";
import { useAnnouncer } from "@/shared/lib/announcer";
import { signIn } from "@/test/render";
import { server } from "@/test/server";
import { ANSWER, CONTEXT, DONE, type Frame, fullAnswer, sseResponse } from "@/test/sse";

const P = "p1";
const thread = () => getThread(P);

function serve(script: (body: AskRequest, call: number) => Frame[]) {
  const bodies: AskRequest[] = [];
  server.use(
    http.post(`/api/profiles/${P}/ask`, async ({ request }) => {
      const body = (await request.json()) as AskRequest;
      bodies.push(body);
      return sseResponse(script(body, bodies.length)).response;
    }),
  );
  return bodies;
}

beforeEach(() => signIn());
afterEach(() => {
  useThreadStore.setState({ threads: {} });
  useCanvasStore.getState().clearOverlay();
});

describe("ask flow over SSE", () => {
  it("applies status → context → answer → result → summary → done to one turn and keeps the conversation", async () => {
    const bodies = serve(() => fullAnswer());
    await askQuestion(P, "  Top customers last month?  ");
    const [turn] = thread().turns;
    expect(bodies[0]).toEqual({ question: "Top customers last month?" });
    expect(turn).toMatchObject({
      status: "done",
      stage: null,
      context: CONTEXT,
      answer: ANSWER,
      summary: { mode: "profile" },
      done: DONE,
      error: null,
    });
    expect(turn?.result?.rowCount).toBe(2);
    expect(thread().conversationId).toBe("conv-1");
    // The answer highlights its tables; the result's plan goes to the canvas overlay.
    expect([...(useCanvasStore.getState().highlight ?? [])]).toEqual(["shop.orders", "shop.customers"]);
    expect(useCanvasStore.getState().overlay["shop.orders"]).toMatchObject({ tone: "success" });
    expect(useAnnouncer.getState().message).toBe("Answer ready");

    await askQuestion(P, "And the month before?");
    expect(bodies[1]).toEqual({ question: "And the month before?", conversationId: "conv-1" });
  });

  it("keeps the latest stage while streaming", () => {
    let turn = createTurn({ question: "q" });
    for (const stage of ["retrieving", "generating", "checking"] as const)
      turn = reduceTurn(turn, { type: "status", stage });
    expect(turn).toMatchObject({ status: "streaming", stage: "checking" });
    turn = reduceTurn(turn, { type: "error", error: { code: "llm", message: "x", detail: "refusal" } });
    expect(turn.status).toBe("error");
    expect(turn.error).toMatchObject({ code: "llm", detail: "refusal" });
  });

  it("a clarification answer is sent with saveToGlossary, the question and the conversation", async () => {
    const bodies = serve((body) =>
      body.clarification
        ? fullAnswer()
        : [
            ["context", CONTEXT],
            [
              "answer",
              {
                ...ANSWER,
                kind: "clarify",
                sql: "",
                clarifyingQuestion: {
                  question: "What is an active customer?",
                  options: ["Ordered in 30 days", "Signed in"],
                },
              },
            ],
            ["done", { ...DONE, conversationId: "conv-c" }],
          ],
    );
    await askQuestion(P, "How many active customers?");
    const asked = thread().turns[0];
    expect(asked?.answer?.kind).toBe("clarify");
    expect(useAnnouncer.getState().message).toBe("Answer ready: the assistant asks a question");

    if (!asked) throw new Error("no turn");
    await answerClarification(P, asked, "Ordered in 30 days", true);
    expect(bodies[1]).toEqual({
      question: "How many active customers?",
      conversationId: "conv-c",
      clarification: { question: "What is an active customer?", answer: "Ordered in 30 days", saveToGlossary: true },
    });
    await answerClarification(P, asked, "Signed in", false);
    expect(bodies[2]?.clarification).toEqual({ question: "What is an active customer?", answer: "Signed in" });
  });

  it("re-asking with an edited assumption sends assumptionOverrides and remembers the previous SQL for the diff", async () => {
    const bodies = serve((body) =>
      fullAnswer(body.assumptionOverrides ? { ...ANSWER, sql: ANSWER.sql.replace("30 days", "7 days") } : ANSWER),
    );
    await askQuestion(P, "Top customers?");
    const first = thread().turns[0];
    if (!first) throw new Error("no turn");
    const overrides = [
      { label: "Last 7 days", sqlFragment: "o.placed_at >= now() - interval '7 days'" },
      { label: "Every order counts", sqlFragment: "" },
    ];
    await reaskWithAssumptions(P, first, overrides);
    expect(bodies[1]).toEqual({ question: "Top customers?", conversationId: "conv-1", assumptionOverrides: overrides });
    const second = thread().turns[1];
    expect(second?.previousSql).toBe(ANSWER.sql);
    expect(second?.answer?.sql).toContain("7 days");
  });

  it.each([
    [
      "cost",
      { reason: "cost", estimatedCost: 3e6, estimatedRows: 1e6, conversationId: "conv-x" },
      { confirmCost: true },
    ],
    ["rows", { reason: "rows", conversationId: "conv-x" }, { confirmRows: true }],
  ] as const)(
    "needs_confirmation (%s) stops, then re-sends the same question and conversation with the flag",
    async (_, confirmation, flag) => {
      const bodies = serve((body) =>
        body.confirmCost || body.confirmRows
          ? fullAnswer()
          : [
              ["context", CONTEXT],
              ["answer", ANSWER],
              ["needs_confirmation", confirmation],
            ],
      );
      await askQuestion(P, "Everything please");
      const turn = thread().turns[0];
      expect(turn).toMatchObject({ status: "confirm", confirmation, result: null });
      expect(thread().conversationId).toBe("conv-x");
      if (!turn) throw new Error("no turn");
      await confirmTurn(P, turn);
      expect(bodies[1]).toEqual({ question: "Everything please", conversationId: "conv-x", ...flag });
      expect(thread().turns).toHaveLength(1); // the same card continues
      expect(thread().turns[0]).toMatchObject({ status: "done", confirmation: null });
      expect(thread().turns[0]?.result?.rowCount).toBe(2);
    },
  );

  it("a second confirmation keeps the first flag", async () => {
    const bodies = serve((body) =>
      body.confirmRows
        ? fullAnswer()
        : body.confirmCost
          ? [["needs_confirmation", { reason: "rows", conversationId: "c" }]]
          : [["needs_confirmation", { reason: "cost", estimatedCost: 1, estimatedRows: 1, conversationId: "c" }]],
    );
    await askQuestion(P, "q");
    const first = () => {
      const turn = thread().turns[0];
      if (!turn) throw new Error("no turn");
      return turn;
    };
    await confirmTurn(P, first());
    await confirmTurn(P, first());
    expect(bodies[2]).toEqual({ question: "q", conversationId: "c", confirmCost: true, confirmRows: true });
  });

  it("Stop aborts the stream: the turn ends stopped and later frames are ignored", async () => {
    const encoder = new TextEncoder();
    let push: ((event: string, data: unknown) => void) | undefined;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        push = (event, data) =>
          controller.enqueue(encoder.encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`));
        push("status", { stage: "retrieving" });
        push("context", CONTEXT);
        push("status", { stage: "generating" });
      },
    });
    server.use(
      http.post(
        `/api/profiles/${P}/ask`,
        () => new HttpResponse(stream, { headers: { "Content-Type": "text/event-stream" } }),
      ),
    );
    const run = askQuestion(P, "slow one");
    await waitFor(() => expect(thread().turns[0]?.stage).toBe("generating"));
    expect(stopAsk(P)).toBe(true);
    await run;
    expect(thread().turns[0]).toMatchObject({ status: "stopped", stage: null });
    expect(useAnnouncer.getState().message).toBe("Answer stopped");
    try {
      push?.("answer", ANSWER);
    } catch {
      // the stream may already be closed by the abort
    }
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(thread().turns[0]?.answer).toBeNull();
    expect(stopAsk(P)).toBe(false);
  });

  it("errors before the stream (409 llm_not_configured) and llm errors in it end the turn with an error", async () => {
    server.use(
      http.post(`/api/profiles/${P}/ask`, () =>
        HttpResponse.json({ error: { code: "llm_not_configured", message: "choose a provider" } }, { status: 409 }),
      ),
    );
    await askQuestion(P, "q");
    expect(thread().turns[0]).toMatchObject({ status: "error", error: { code: "llm_not_configured" } });
    serve(() => [["error", { code: "llm", message: "invalid x-api-key", detail: "auth" }]]);
    await askQuestion(P, "q2");
    expect(thread().turns[1]).toMatchObject({ status: "error", error: { code: "llm", detail: "auth" } });
    expect(useAnnouncer.getState().message).toBe("Answer failed");
  });

  it("a stream that closes without done is reported as cut off", async () => {
    serve(() => [["context", CONTEXT]]);
    await askQuestion(P, "q");
    expect(thread().turns[0]?.status).toBe("error");
    expect(thread().turns[0]?.error?.message).toMatch(/ended before/);
  });
});
