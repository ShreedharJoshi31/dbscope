import { screen, waitFor, within } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { useThreadStore } from "@/entities/assistant-turn";
import { useSqlTabs } from "@/entities/query";
import { useCanvasStore } from "@/entities/schema";
import { useLlmSettingsDialog } from "@/features/llm-settings";
import type { AskRequest, LLMSettings, Profile } from "@/shared/api";
import { makeProfile, renderWithProviders } from "@/test/render";
import { server } from "@/test/server";
import { ANSWER, CONTEXT, DONE, type Frame, fullAnswer, sseResponse } from "@/test/sse";
import { AssistantPanel } from "@/widgets/assistant-panel";

const ANTHROPIC: LLMSettings = {
  provider: "anthropic",
  model: "claude-opus-5-5",
  summaryModel: "claude-haiku-5-5",
  effort: "medium",
  hasApiKey: true,
};

function setup(script: (body: AskRequest) => Frame[], options: { profile?: Profile; llm?: LLMSettings } = {}) {
  const profile = options.profile ?? makeProfile({ id: "p1" });
  const bodies: AskRequest[] = [];
  server.use(
    http.get("/api/profiles", () => HttpResponse.json([profile])),
    http.get("/api/llm/settings", () => HttpResponse.json(options.llm ?? ANTHROPIC)),
    http.post(`/api/profiles/${profile.id}/ask`, async ({ request }) => {
      const body = (await request.json()) as AskRequest;
      bodies.push(body);
      return sseResponse(script(body)).response;
    }),
  );
  return { bodies, ...renderWithProviders(<AssistantPanel />) };
}

async function ask(user: ReturnType<typeof setup>["user"], question: string) {
  const composer = await screen.findByTestId("assistant-composer");
  await user.type(composer, question);
  await user.keyboard("{Enter}");
}

beforeEach(() => {
  useSqlTabs.setState({ tabs: [], next: 1, active: "erd", scope: null, reveal: null });
});
afterEach(() => {
  useThreadStore.setState({ threads: {} });
  useCanvasStore.getState().clearOverlay();
  useLlmSettingsDialog.setState({ open: false });
});

describe("AssistantPanel", () => {
  it("renders the answer card in the UX rule 5 order, as plain text, and announces completion", async () => {
    const { user, bodies } = setup(() => fullAnswer());
    await ask(user, "Top customers last month?");
    const card = await screen.findByTestId("answer-card");
    await waitFor(() => expect(card).toHaveAttribute("data-status", "done"));

    const order = [...card.querySelectorAll("[data-section]")].map((el) => el.getAttribute("data-section"));
    expect(order).toEqual(["question", "context", "sql", "explanation", "assumptions", "facts", "result", "summary"]);
    expect(bodies).toEqual([{ question: "Top customers last month?" }]);

    expect(within(card).getByTestId("context-line")).toHaveTextContent(
      "Sent to claude-opus-5-5 (Anthropic): schema only · 2 relevant tables · omitted columns: app.users.password_hash",
    );
    expect(within(card).getByRole("figure", { name: "Generated SQL" })).toHaveTextContent(ANSWER.sql);
    expect(within(card).getByText("Reads only")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: /2 tables/ })).toBeInTheDocument();
    expect(within(card).getByText(/Plan estimate: cost/)).toHaveTextContent("Plan estimate: cost 120, ~2 rows");
    // Model and database text never becomes markup: no links or images.
    expect(within(card).getByText(/\*\*Ada\*\* leads/)).toBeInTheDocument();
    expect(within(card).getByText("<img src=x onerror=alert(1)>")).toBeInTheDocument();
    expect(card.querySelector("a, img")).toBeNull();
    expect(screen.getByTestId("live-region")).toHaveTextContent("Answer ready");
  });

  it("Shift+Enter adds a line and Enter sends; Open in editor and the tables badge act on the workbench", async () => {
    const { user, bodies } = setup(() => fullAnswer());
    const composer = await screen.findByTestId("assistant-composer");
    await user.type(composer, "line one{Shift>}{Enter}{/Shift}line two");
    expect(bodies).toHaveLength(0);
    await user.keyboard("{Enter}");
    await waitFor(() => expect(bodies[0]?.question).toBe("line one\nline two"));
    const card = await screen.findByTestId("answer-card");
    await waitFor(() => expect(card).toHaveAttribute("data-status", "done"));

    await user.click(within(card).getByRole("button", { name: "Open in editor" }));
    expect(useSqlTabs.getState().tabs[0]).toMatchObject({ title: "query-1.sql", sql: ANSWER.sql });
    useCanvasStore.getState().setHighlight(null);
    await user.click(within(card).getByRole("button", { name: /2 tables/ }));
    expect([...(useCanvasStore.getState().highlight ?? [])]).toEqual(ANSWER.tables);
    expect(useSqlTabs.getState().active).toBe("erd");
  });

  it("editing an assumption chip re-asks with assumptionOverrides and shows the SQL diff", async () => {
    const { user, bodies } = setup((body) =>
      fullAnswer(body.assumptionOverrides ? { ...ANSWER, sql: ANSWER.sql.replace("'30 days'", "'7 days'") } : ANSWER),
    );
    await ask(user, "Top customers?");
    const first = await screen.findByTestId("answer-card");
    await waitFor(() => expect(first).toHaveAttribute("data-status", "done"));

    await user.click(within(first).getByRole("button", { name: /Edit assumption: Last 30 days/ }));
    const form = within(first).getByRole("form", { name: "Edit assumption: Last 30 days" });
    const label = within(form).getByLabelText("Assumption");
    await user.clear(label);
    await user.type(label, "Last 7 days");
    const fragment = within(form).getByLabelText("SQL fragment");
    await user.clear(fragment);
    await user.type(fragment, "o.placed_at >= now() - interval '7 days'");
    await user.click(within(form).getByRole("button", { name: "Re-ask with this" }));

    await waitFor(() => expect(bodies).toHaveLength(2));
    expect(bodies[1]).toEqual({
      question: "Top customers?",
      conversationId: "conv-1",
      assumptionOverrides: [
        { label: "Last 7 days", sqlFragment: "o.placed_at >= now() - interval '7 days'" },
        { label: "Every order counts", sqlFragment: "" },
      ],
    });
    const cards = await screen.findAllByTestId("answer-card");
    const second = cards[1] as HTMLElement;
    await waitFor(() => expect(second).toHaveAttribute("data-status", "done"));
    const diff = within(second).getByTestId("sql-diff");
    expect(diff.querySelector("del")).toHaveTextContent("'30");
    expect(diff.querySelector("ins")).toHaveTextContent("'7");
  });

  it("a clarifying question offers its options, Other… and Remember this answer", async () => {
    const { user, bodies } = setup((body) =>
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
                assumptions: [],
                explanationSteps: [],
                clarifyingQuestion: {
                  question: "Which customers count as active?",
                  options: ["Ordered in 30 days", "Signed in"],
                },
              },
            ],
            ["done", DONE],
          ],
    );
    await ask(user, "Active customers?");
    const card = await screen.findByTestId("answer-card");
    await waitFor(() => expect(card).toHaveAttribute("data-status", "done"));
    expect(within(card).queryByRole("figure", { name: "Generated SQL" })).not.toBeInTheDocument();
    expect(within(card).getByRole("checkbox", { name: "Remember this answer in the glossary" })).toBeChecked();

    await user.click(within(card).getByRole("button", { name: "Other…" }));
    await user.type(within(card).getByLabelText("Your answer"), "Paid in the last quarter");
    await user.click(within(card).getByRole("button", { name: "Answer" }));
    await waitFor(() => expect(bodies).toHaveLength(2));
    expect(bodies[1]?.clarification).toEqual({
      question: "Which customers count as active?",
      answer: "Paid in the last quarter",
      saveToGlossary: true,
    });

    await user.click(within(card).getByRole("checkbox", { name: "Remember this answer in the glossary" }));
    await user.click(within(card).getByRole("button", { name: "Signed in" }));
    await waitFor(() => expect(bodies).toHaveLength(3));
    expect(bodies[2]?.clarification).toEqual({ question: "Which customers count as active?", answer: "Signed in" });
  });

  it("asks before sending prod-replica rows to the provider, then re-sends with confirmRows", async () => {
    const profile = makeProfile({
      id: "pr",
      name: "reports",
      env: "prod-replica",
      summaryMode: "rows",
      maxRowsToLLM: 25,
    });
    const { user, bodies } = setup(
      (body) =>
        body.confirmRows
          ? fullAnswer()
          : [
              ["context", { ...CONTEXT, sends: "schema+rows" }],
              ["answer", ANSWER],
              ["needs_confirmation", { reason: "rows", conversationId: "conv-r" }],
            ],
      { profile },
    );
    await ask(user, "Revenue by plan");
    const box = await screen.findByRole("region", { name: "Confirmation needed" });
    expect(box).toHaveTextContent("Send sample rows from this prod-replica connection to claude-opus-5-5 (Anthropic)?");
    expect(box).toHaveTextContent("up to 25 rows");
    await user.click(within(box).getByRole("button", { name: "Send rows" }));
    await waitFor(() => expect(bodies).toHaveLength(2));
    expect(bodies[1]).toEqual({ question: "Revenue by plan", conversationId: "conv-r", confirmRows: true });
    await waitFor(() => expect(screen.getByTestId("answer-card")).toHaveAttribute("data-status", "done"));
  });

  it("shows the cost confirmation with the estimate and declines without running", async () => {
    const profile = makeProfile({ id: "pc", env: "staging" });
    const { user, bodies } = setup(
      () => [
        ["context", CONTEXT],
        ["answer", ANSWER],
        [
          "needs_confirmation",
          { reason: "cost", estimatedCost: 25_000_000, estimatedRows: 4_000_000, conversationId: "c" },
        ],
      ],
      { profile },
    );
    await ask(user, "Everything");
    const box = await screen.findByRole("region", { name: "Confirmation needed" });
    expect(box).toHaveTextContent("cost of 25,000,000 (about 4M rows), over the limit of 10,000,000");
    await user.click(within(box).getByRole("button", { name: "Don't run" }));
    expect(screen.getByTestId("answer-card")).toHaveAttribute("data-status", "stopped");
    expect(bodies).toHaveLength(1);
  });

  it("explains provider failures in plain words and links to the settings", async () => {
    const { user } = setup(() => [["error", { code: "llm", message: "invalid x-api-key", detail: "auth" }]]);
    await ask(user, "q");
    const error = await screen.findByRole("alert");
    expect(error).toHaveTextContent("The provider rejected the API key");
    await user.click(within(error).getByRole("button", { name: "Open assistant settings" }));
    expect(useLlmSettingsDialog.getState().open).toBe(true);
    expect(screen.getByTestId("live-region")).toHaveTextContent("Answer failed");
  });

  it("shows a guard rejection of generated SQL like the Messages tab", async () => {
    const { user } = setup(() => [
      ["answer", { ...ANSWER, sql: "SELECT * FROM t FOR UPDATE" }],
      ["error", { code: "rejected", message: "FOR UPDATE locks rows", detail: "locking_clause" }],
    ]);
    await ask(user, "q");
    expect(await screen.findByTestId("query-error")).toHaveTextContent("Row locks are not allowed");
  });

  it("offers setup when no provider is configured", async () => {
    const { user } = setup(() => [], { llm: { provider: "none", model: "", summaryModel: "", hasApiKey: false } });
    await user.click(await screen.findByRole("button", { name: "Choose a model…" }));
    expect(useLlmSettingsDialog.getState().open).toBe(true);
    expect(screen.queryByTestId("assistant-composer")).not.toBeInTheDocument();
  });
});
