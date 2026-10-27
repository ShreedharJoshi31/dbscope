import { expect, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { tableNode, waitForCanvas } from "./support/canvas";
import { ANSWER_SQL, clarifyAsk, rowsConfirmAsk, slowAsk } from "./support/phase23-stub";
import { ANTHROPIC, assistant, dock } from "./support/query";

async function ask(page: import("@playwright/test").Page, question: string) {
  const composer = assistant(page).getByTestId("assistant-composer");
  await composer.fill(question);
  await composer.press("Enter");
}

test.describe("assistant", () => {
  test("configure a provider, ask, and get a streamed answer card in UX rule 5 order", async ({ page }) => {
    const { phase23 } = await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await waitForCanvas(page);

    // Empty state → settings.
    await assistant(page).getByRole("button", { name: "Choose a model…" }).click();
    const dialog = page.getByRole("dialog", { name: "Assistant model" });
    await dialog.getByRole("radio", { name: "Anthropic" }).click();
    await expect(dialog.getByRole("region", { name: /What is sent to Anthropic/ })).toContainText(
      "only the schema. dbscope counts the result summary itself",
    );
    await dialog.getByLabel("API key").fill("sk-ant-test-key");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog.getByTestId("api-key-saved")).toBeVisible();
    await expect(dialog.getByLabel("API key")).toHaveValue("");
    expect(phase23.llmPuts[0]).toMatchObject({
      provider: "anthropic",
      model: "claude-opus-5-5",
      apiKey: "sk-ant-test-key",
    });
    await dialog.getByRole("button", { name: "Test" }).click();
    await expect(dialog.getByTestId("llm-test-result")).toContainText("Connected.");
    await page.keyboard.press("Escape");

    await ask(page, "Who ordered most in the last 30 days?");
    const card = assistant(page).getByTestId("answer-card");
    // Streamed: progress appears before the answer, which arrives in pieces.
    await expect(card.getByTestId("turn-progress")).toBeVisible();
    await expect(card).toHaveAttribute("data-status", "done", { timeout: 10_000 });

    const order = await card
      .locator("[data-section]")
      .evaluateAll((els) => els.map((e) => e.getAttribute("data-section")));
    expect(order).toEqual(["question", "context", "sql", "explanation", "assumptions", "facts", "result", "summary"]);
    await expect(card.getByTestId("context-line")).toHaveText(
      "Sent to claude-opus-5-5 (Anthropic): schema only · full schema, 3 tables · omitted columns: app.api_keys.token_hash",
    );
    await expect(card.getByRole("figure", { name: "Generated SQL" })).toContainText("GROUP BY c.name");
    await expect(card.getByText("Reads only")).toBeVisible();
    await expect(card.getByText(/Plan estimate: cost 48,211/)).toBeVisible();
    await expect(page.getByTestId("live-region")).toHaveText("Answer ready");
    expect(phase23.asks?.calls[0]).toEqual({ question: "Who ordered most in the last 30 days?" });

    // Answers push their plan overlay to the canvas.
    await page.getByRole("button", { name: "Fit all tables in view" }).click();
    await expect(tableNode(page, "shop.orders").getByTestId("overlay-badge")).toHaveText(/Problem/);

    // Open in editor.
    await card.getByRole("button", { name: "Open in editor" }).click();
    await expect(page.getByRole("tab", { name: "query-1.sql" })).toHaveAttribute("aria-selected", "true");
    await expect(page.getByRole("tabpanel").locator(".cm-content")).toContainText("GROUP BY c.name");

    // Open in results.
    await card.getByRole("button", { name: "Open in results" }).click();
    await expect(dock(page).getByTestId("result-summary")).toContainText("3 rows");
    await expect(dock(page).getByTestId("result-summary")).toContainText("from the assistant");

    // Edit an assumption → re-ask with assumptionOverrides → diff.
    await card.getByRole("button", { name: /Edit assumption: Last 30 days/ }).click();
    const form = card.getByRole("form", { name: "Edit assumption: Last 30 days" });
    await form.getByLabel("Assumption").fill("Last 7 days");
    await form.getByLabel("SQL fragment").fill("o.placed_at >= now() - interval '7 days'");
    await form.getByRole("button", { name: "Re-ask with this" }).click();
    const second = assistant(page).getByTestId("answer-card").nth(1);
    await expect(second).toHaveAttribute("data-status", "done", { timeout: 10_000 });
    expect(phase23.asks?.calls[1]).toEqual({
      question: "Who ordered most in the last 30 days?",
      conversationId: "conv-1",
      assumptionOverrides: [
        { label: "Last 7 days", sqlFragment: "o.placed_at >= now() - interval '7 days'" },
        { label: "Ties broken arbitrarily", sqlFragment: "ORDER BY orders DESC" },
      ],
    });
    const diff = second.getByTestId("sql-diff");
    await expect(diff.locator("del")).toContainText("'30");
    await expect(diff.locator("ins")).toContainText("'7");
  });

  test("clarify path: option buttons, Other…, and Remember this answer saves to the glossary", async ({ page }) => {
    const { phase23 } = await stubAgent(page, { profiles: [stubProfile()], llm: ANTHROPIC, ask: clarifyAsk });
    await openApp(page);
    await ask(page, "How many active customers?");
    const card = assistant(page).getByTestId("answer-card");
    await expect(card).toHaveAttribute("data-status", "done");
    const clarify = card.locator('[data-section="clarify"]');
    await expect(clarify).toContainText("What makes a customer active?");
    await expect(clarify.getByRole("button", { name: "Other…" })).toBeVisible();
    await expect(clarify.getByRole("checkbox", { name: /Remember this answer/ })).toBeChecked();
    await clarify.getByRole("button", { name: "Ordered in the last 30 days" }).click();

    const answered = assistant(page).getByTestId("answer-card").nth(1);
    await expect(answered).toHaveAttribute("data-status", "done", { timeout: 10_000 });
    await expect(answered).toContainText(
      "You answered “What makes a customer active?” with “Ordered in the last 30 days”.",
    );
    await expect(answered.getByText("Saved to glossary")).toBeVisible();
    expect(phase23.asks?.calls[1]).toEqual({
      question: "How many active customers?",
      conversationId: "conv-1",
      clarification: {
        question: "What makes a customer active?",
        answer: "Ordered in the last 30 days",
        saveToGlossary: true,
      },
    });
  });

  test("prod-replica rows mode asks before sample rows leave the machine", async ({ page }) => {
    const { phase23 } = await stubAgent(page, {
      profiles: [stubProfile({ name: "reports", env: "prod-replica", summaryMode: "rows", maxRowsToLLM: 20 })],
      llm: ANTHROPIC,
      ask: rowsConfirmAsk,
    });
    await openApp(page);
    // The canvas announces its layout in the same live region; let it finish first.
    await waitForCanvas(page);
    await ask(page, "Revenue by plan");
    const box = assistant(page).getByRole("region", { name: "Confirmation needed" });
    await expect(box).toContainText(
      "Send sample rows from this prod-replica connection to claude-opus-5-5 (Anthropic)?",
    );
    await expect(box).toContainText("up to 20 rows");
    await expect(page.getByTestId("live-region")).toHaveText("Answer needs your confirmation");
    await box.getByRole("button", { name: "Send rows" }).click();
    const card = assistant(page).getByTestId("answer-card");
    await expect(card).toHaveAttribute("data-status", "done", { timeout: 10_000 });
    await expect(card.getByTestId("context-line")).toContainText("schema + sample rows");
    await expect(card).toContainText("Written by the model from sample rows.");
    expect(phase23.asks?.calls[1]).toEqual({
      question: "Revenue by plan",
      conversationId: "conv-rows",
      confirmRows: true,
    });
  });

  test("Stop aborts a streaming answer", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()], llm: ANTHROPIC, ask: slowAsk });
    await openApp(page);
    await waitForCanvas(page);
    await ask(page, "Slow question");
    const card = assistant(page).getByTestId("answer-card");
    await expect(card.getByTestId("turn-progress")).toContainText("Writing SQL…");
    await assistant(page).getByRole("button", { name: "Stop the answer" }).click();
    await expect(card).toHaveAttribute("data-status", "stopped");
    await expect(page.getByTestId("live-region")).toHaveText("Answer stopped");
    await expect(assistant(page).getByRole("button", { name: "Send question" })).toBeVisible();
  });

  test("Cmd/Ctrl+Alt+A and the palette focus the composer; New conversation clears the thread", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()], llm: ANTHROPIC });
    await openApp(page);
    const composer = assistant(page).getByTestId("assistant-composer");
    await expect(composer).toBeVisible();
    await page.keyboard.press("ControlOrMeta+Alt+A");
    await expect(composer).toBeFocused();
    await page.getByRole("tab", { name: "ERD" }).focus();
    await page.keyboard.press("ControlOrMeta+K");
    await page.getByRole("option", { name: /Ask the assistant/ }).click();
    await expect(composer).toBeFocused();
    await ask(page, "q");
    await expect(assistant(page).getByTestId("answer-card")).toHaveAttribute("data-status", "done");
    await assistant(page).getByRole("button", { name: "New conversation" }).click();
    await expect(assistant(page).getByTestId("answer-card")).toHaveCount(0);
  });

  test("glossary editor from the palette saves edits", async ({ page }) => {
    const { phase23 } = await stubAgent(page, {
      profiles: [stubProfile()],
      llm: ANTHROPIC,
      glossary: [{ term: "active user", definition: "Signed in within 30 days", sql: "" }],
    });
    await openApp(page);
    await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();
    await page.keyboard.press("ControlOrMeta+K");
    await page.getByRole("option", { name: /Edit the glossary/ }).click();
    const dialog = page.getByRole("dialog", { name: /Glossary for orders-dev/ });
    await expect(dialog.getByTestId("glossary-row")).toHaveCount(1);
    await dialog.getByRole("button", { name: "Add term" }).click();
    await dialog.getByTestId("glossary-row").nth(1).getByLabel("Term", { exact: true }).fill("churned");
    await dialog.getByRole("button", { name: "Save glossary" }).click();
    await expect(dialog).toHaveCount(0);
    expect(phase23.glossaryPuts[0]).toEqual([
      { term: "active user", definition: "Signed in within 30 days", sql: "" },
      { term: "churned", definition: "", sql: "" },
    ]);
  });

  test("answer SQL Re-run goes through the guarded executor and opens the dock", async ({ page }) => {
    const { phase23 } = await stubAgent(page, { profiles: [stubProfile()], llm: ANTHROPIC });
    await openApp(page);
    await ask(page, "q");
    const card = assistant(page).getByTestId("answer-card");
    await expect(card).toHaveAttribute("data-status", "done");
    await card.getByRole("button", { name: "Re-run" }).click();
    await expect(dock(page).getByTestId("result-summary")).toBeVisible();
    expect(phase23.queries[0]?.sql).toBe(ANSWER_SQL);
  });
});
