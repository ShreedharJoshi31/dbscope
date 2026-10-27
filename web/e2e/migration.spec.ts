import { expect, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { centreOn, tableNode, waitForCanvas } from "./support/canvas";
import { dropFiles, marker, review } from "./support/migration";
import { MIGRATION_FILES } from "./support/migration-stub";
import { dock } from "./support/query";

const PROFILE = stubProfile();

test.describe("migration review", () => {
  test("drop two files → tab, rating, detail, canvas diff + impact, Risk tab, keyboard, Markdown", async ({
    page,
    context,
  }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    const { migrations } = await stubAgent(page, { profiles: [PROFILE] });
    await openApp(page);
    await waitForCanvas(page);

    await dropFiles(page, MIGRATION_FILES);
    const tab = page.getByRole("tab", { name: "2 migration files" });
    await expect(tab).toHaveAttribute("aria-selected", "true");
    // Sent in drop order, as dropped, to the active connection.
    expect(migrations.requests).toHaveLength(1);
    expect(migrations.requests[0]?.path).toBe(`/api/profiles/${PROFILE.id}/migrations/analyze`);
    expect(migrations.requests[0]?.body.files.map((f) => f.name)).toEqual(["0042_orders.sql", "0043_cleanup.sql"]);
    expect(migrations.requests[0]?.body.files[0]?.sql).toBe(MIGRATION_FILES[0]?.sql);
    expect(migrations.requests[0]?.body.txMode).toBe("auto");

    // Header: overall rating, counts, PG version, unanalyzable.
    await expect(review(page).getByTestId("overall-rating")).toHaveText("Critical");
    await expect(review(page).getByTestId("count-critical")).toContainText("Critical2");
    await expect(review(page).getByTestId("count-medium")).toContainText("Medium3");
    await expect(review(page).getByTestId("pg-version")).toHaveText("PostgreSQL 17.2 rules");
    await expect(review(page).getByTestId("unanalyzable")).toContainText("0043_cleanup.sql:3");
    await expect(review(page).getByTestId("unanalyzable")).toContainText("PostgreSQL 17 grammar cannot parse");
    await expect(review(page).getByTestId("new-relations")).toContainText("shop.parcel_events");

    // Each statement has a gutter marker with icon + text; the worst one is selected first.
    await expect(page.getByTestId("migration-file")).toHaveCount(2);
    await expect(marker(page, 5)).toHaveAttribute("data-selected", "true");
    await expect(marker(page, 5)).toHaveText("Critical");
    await expect(marker(page, 5).locator("svg")).toHaveAttribute("data-shape", "octagon-filled");
    await expect(marker(page, 0).locator("svg")).toHaveAttribute("data-shape", "triangle");
    const detail = page.getByTestId("statement-detail");
    await expect(detail.getByRole("heading", { level: 3 })).toHaveText("DROP TABLE");
    const willFail = detail.locator('[data-rule="DBS-WILL-FAIL"]');
    await expect(willFail).toContainText("fails without CASCADE");
    await expect(willFail.getByTestId("safe-alternative")).toHaveText(
      "Drop or change the dependent objects first, or use CASCADE knowingly",
    );

    // Select another statement by clicking its marker.
    await marker(page, 2).click();
    await expect(detail.getByRole("heading", { level: 3 })).toHaveText("ALTER TABLE RENAME COLUMN");
    await expect(detail.getByTestId("statement-lock")).toHaveText(
      "shop.customers: ACCESS EXCLUSIVE — blocks reads and writes",
    );
    await expect(detail).toContainText("No lock_timeout: while it waits for its lock");
    await expect(detail.locator('[data-rule="DBS-RENAME"]').getByTestId("safe-alternative")).toContainText(
      "Expand and contract",
    );
    await detail.locator('[data-rule="DBS-RENAME"]').getByRole("button", { name: "Copy the safe alternative" }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toMatch(/^Expand and contract/);

    // Clicking a line inside a multi-line statement selects it.
    await page.getByTestId("migration-file").first().locator(".cm-line", { hasText: "happened_at" }).click();
    await expect(detail.getByRole("heading", { level: 3 })).toHaveText("CREATE TABLE");
    await marker(page, 2).click();

    // Show on canvas: diff colours with text labels, impact of the changed relation.
    await detail.getByRole("button", { name: "Show on canvas" }).click();
    await expect(page.getByRole("tab", { name: "ERD" })).toHaveAttribute("aria-selected", "true");
    await expect(tableNode(page, "shop.customers")).toHaveAttribute("data-mark", "selected");
    await expect(tableNode(page, "shop.customers")).toHaveAttribute("data-diff", "altered");
    await expect(tableNode(page, "shop.customers").getByTestId("diff-badge")).toContainText("altered");
    await expect(tableNode(page, "shop.customers").locator('[data-column="email"]')).toHaveAttribute(
      "data-diff",
      "renamed",
    );
    await expect(tableNode(page, "shop.customers").locator('[data-column="email"]')).toContainText("email_address");
    await expect(tableNode(page, "shop.addresses")).toHaveAttribute("data-mark", "impact");
    await expect(tableNode(page, "shop.addresses").getByTestId("impact-chip")).toHaveText(/catalog/);
    // Everything that does not depend on it is dimmed (off-screen tables are not rendered).
    await expect(page.locator('[data-testid="table-node"][data-dim="true"]').first()).toBeVisible();
    await expect(tableNode(page, "shop.addresses")).not.toHaveAttribute("data-dim", "true");
    const legend = page.getByTestId("migration-legend");
    await expect(legend).toContainText("Not on the canvas: shop.parcel_events (added)");
    await expect(legend.getByTestId("impact-list")).toContainText("Depends on shop.customers (6)");
    await expect(legend.locator('[data-testid="impact-item"][data-certainty="heuristic"]')).toContainText(
      "shop.order_summaries",
    );
    await expect(page.locator('[data-node-id="shop.parcel_events"]')).toHaveCount(0);

    // A heuristic table dependent, a dropped table and column marks.
    await centreOn(page, "shop.orders");
    await tableNode(page, "shop.orders").locator('[data-column="id"]').click();
    await expect(tableNode(page, "shop.orders")).toHaveAttribute("data-mark", "selected");
    await expect(tableNode(page, "shop.orders").locator('[data-column="currency"]')).toHaveAttribute(
      "data-diff",
      "altered",
    );
    // Added columns hang below the card (outside it, so the node keeps its size).
    await expect(page.locator('.react-flow__node[data-id="shop.orders"] [data-testid="added-columns"]')).toContainText(
      "tracking_code",
    );
    await centreOn(page, "shop.refunds");
    await expect(tableNode(page, "shop.refunds")).toHaveAttribute("data-mark", "impact-heuristic");
    await expect(tableNode(page, "shop.refunds").getByTestId("impact-chip")).toHaveText(/heuristic/);
    await centreOn(page, "shop.shipments");
    await expect(tableNode(page, "shop.shipments")).toHaveAttribute("data-diff", "dropped");
    await expect(tableNode(page, "shop.shipments").getByTestId("diff-badge")).toContainText("dropped");

    // Risk tab: findings, sortable; a click jumps back to the statement.
    await expect(dock(page).getByRole("tab", { name: "Risk" })).toHaveAttribute("aria-selected", "true");
    const rows = dock(page).getByTestId("risk-row");
    await expect(rows).toHaveCount(10);
    await expect(rows.first()).toHaveAttribute("data-rule", "DBS-DROP-TABLE");
    await dock(page).getByRole("button", { name: /^Rule/ }).click();
    await expect(dock(page).locator("th").nth(1)).toHaveAttribute("aria-sort", "ascending");
    await expect(rows.first()).toHaveAttribute("data-rule", "DBS-DROP-TABLE");
    await dock(page).getByRole("button", { name: /^Rule/ }).click();
    await expect(rows.first()).toHaveAttribute("data-rule", "DBS-WILL-FAIL");
    await dock(page)
      .getByRole("button", { name: /^Open 0043_cleanup\.sql line 4: High, DBS-TYPE-REWRITE/ })
      .click();
    await expect(tab).toHaveAttribute("aria-selected", "true");
    await expect(marker(page, 7)).toBeFocused();
    await expect(detail.getByRole("heading", { level: 3 })).toHaveText("ALTER TABLE ALTER COLUMN TYPE");

    // Keyboard: k / j move between statements; Enter opens the detail.
    await page.keyboard.press("k");
    await expect(marker(page, 6)).toBeFocused();
    await expect(detail.getByRole("heading", { level: 3 })).toHaveText("Unparseable statement");
    await expect(detail).toContainText("the server will reject it too");
    await page.keyboard.press("k");
    await page.keyboard.press("k");
    await expect(marker(page, 4)).toBeFocused();
    await page.keyboard.press("k");
    await expect(marker(page, 3)).toBeFocused(); // crossed into the first file
    await page.keyboard.press("j");
    await expect(marker(page, 4)).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(detail.getByRole("heading", { level: 3 })).toBeFocused();
    await expect(detail.getByRole("heading", { level: 3 })).toHaveText("SET");

    // Copy as Markdown: a PR-comment-ready summary.
    await review(page).getByRole("button", { name: "Copy as Markdown" }).click();
    const md = await page.evaluate(() => navigator.clipboard.readText());
    expect(md).toMatch(/^## dbscope migration review: Critical\n/);
    expect(md).toContain("| Critical | `DBS-WILL-FAIL` | `0043_cleanup.sql:2` | `shop.shipments` |");
    expect(md).toContain("against orders-dev with PostgreSQL 17.2 rules (base: the live schema)");

    // Download JSON, then clear the overlay.
    const download = page.waitForEvent("download");
    await review(page).getByRole("button", { name: "Download JSON" }).click();
    expect((await download).suggestedFilename()).toBe("migrations-dbscope-report.json");
    await review(page).getByRole("button", { name: "Clear migration overlay" }).click();
    await page.getByRole("tab", { name: "ERD" }).click();
    await expect(page.getByTestId("migration-legend")).toHaveCount(0);
    await expect(page.locator("[data-diff]")).toHaveCount(0);
  });

  test("options re-run the analysis; paste from the palette; clear from the palette", async ({ page }) => {
    const { migrations } = await stubAgent(page, { profiles: [PROFILE] });
    await openApp(page);
    await waitForCanvas(page);
    await page.keyboard.press("ControlOrMeta+K");
    await page.getByRole("option", { name: "Paste migration SQL" }).click();
    const dialog = page.getByRole("dialog", { name: "Paste migration SQL" });
    await dialog.getByLabel("File name").fill("V42__orders");
    await dialog.getByTestId("paste-migration-sql").fill("DROP TABLE shop.shipments;");
    await dialog.getByLabel("Transactions").selectOption("file");
    await dialog.getByRole("checkbox", { name: /My deploy tool sets lock_timeout/ }).click();
    await dialog.getByRole("button", { name: "Analyze" }).click();
    await expect(page.getByRole("tab", { name: "V42__orders.sql" })).toHaveAttribute("aria-selected", "true");
    expect(migrations.requests[0]?.body).toEqual({
      files: [{ name: "V42__orders.sql", sql: "DROP TABLE shop.shipments;" }],
      txMode: "file",
      assumeLockTimeout: true,
    });
    await expect(page.getByTestId("statement-detail")).toContainText("lock_timeout is in effect");

    // Changing an option in the header analyzes the same files again.
    await review(page).getByLabel("Transactions").selectOption("statement");
    await expect.poll(() => migrations.requests.length).toBe(2);
    expect(migrations.requests[1]?.body.txMode).toBe("statement");

    await review(page).getByRole("button", { name: "Show on canvas" }).first().click();
    await expect(page.getByTestId("migration-legend")).toBeVisible();
    await page.keyboard.press("ControlOrMeta+K");
    await page.getByRole("option", { name: "Clear migration overlay" }).click();
    await expect(page.getByTestId("migration-legend")).toHaveCount(0);

    // Closing the tab forgets the report.
    await page.getByRole("button", { name: "Close V42__orders.sql" }).click();
    await expect(page.getByRole("tab", { name: "V42__orders.sql" })).toHaveCount(0);
    await expect(page.getByRole("tab", { name: "ERD" })).toHaveAttribute("aria-selected", "true");
  });

  test("errors say what happened and what to do", async ({ page }) => {
    let answer: { status: number; body: unknown } = {
      status: 423,
      body: { error: { code: "secrets_locked", message: "the secrets vault is locked" } },
    };
    const { migrations } = await stubAgent(page, { profiles: [PROFILE], analyze: () => answer });
    await openApp(page);
    await waitForCanvas(page);

    await dropFiles(page, MIGRATION_FILES.slice(0, 1));
    const error = page.getByTestId("review-error");
    await expect(error).toHaveAttribute("data-code", "secrets_locked");
    await expect(error).toContainText("The secrets vault is locked");
    await expect(error.getByRole("button", { name: "Unlock the vault" })).toBeVisible();

    answer = {
      status: 502,
      body: {
        error: { code: "upstream", message: "dial tcp 10.0.0.5:5432: connect: connection refused", detail: "network" },
      },
    };
    await error.getByRole("button", { name: "Try again" }).click();
    await expect(error).toHaveAttribute("data-code", "upstream");
    await expect(error).toContainText("Could not read the base schema: could not reach the server");
    await expect(error).toContainText("HTTP 502");
    await expect(error).toContainText("connection refused");

    answer = {
      status: 504,
      body: { error: { code: "timeout", message: "canceling statement due to statement timeout" } },
    };
    await error.getByRole("button", { name: "Try again" }).click();
    await expect(error).toContainText("Reading the base schema timed out");
    await expect(error).toContainText("statement timeout (30 s)");

    answer = { status: 400, body: { error: { code: "validation", message: "files[0] (a.sql) is larger than 1 MiB" } } };
    await error.getByRole("button", { name: "Try again" }).click();
    await expect(error).toContainText("The agent rejected the files");
    await expect(error).toContainText("files[0] (a.sql) is larger than 1 MiB");
    expect(migrations.requests).toHaveLength(4);

    // Over the limits: explained before anything is sent.
    const many = Array.from({ length: 201 }, (_, i) => ({
      name: `${String(i).padStart(4, "0")}_m.sql`,
      sql: "SELECT 1;",
    }));
    await dropFiles(page, many);
    await expect(page.getByRole("tab", { name: "201 migration files" })).toHaveAttribute("aria-selected", "true");
    await expect(page.getByTestId("review-error").last()).toContainText("Too many files: 201");
    await dropFiles(page, [{ name: "notes.txt", sql: "hello" }]);
    await expect(page.getByTestId("review-error").last()).toContainText("No SQL migration files");
    await expect(page.getByTestId("review-error").last()).toContainText("Ignored: notes.txt");
    expect(migrations.requests).toHaveLength(4);
  });

  test("the sample workspace analyzes against the sample schema; the Risk tab explains itself when empty", async ({
    page,
  }) => {
    const { migrations } = await stubAgent(page, { profiles: [] });
    await openApp(page);
    await page.getByRole("button", { name: "Open sample schema" }).first().click();
    await waitForCanvas(page);
    await page.keyboard.press("ControlOrMeta+J");
    await dock(page).getByRole("tab", { name: "Risk" }).click();
    await expect(dock(page)).toContainText("No migration analyzed");
    await dropFiles(page, MIGRATION_FILES);
    await expect(review(page).getByTestId("overall-rating")).toHaveText("Critical");
    expect(migrations.requests[0]?.path).toBe("/api/sample/migrations/analyze");
    await expect(review(page)).toContainText("against the sample schema");
  });
});
