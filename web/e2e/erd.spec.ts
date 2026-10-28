import { expect, type Page, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { centreOn, fitAll, layoutMeasures, nodePosition, tableNode, viewport, waitForCanvas } from "./support/canvas";
import { syntheticModel } from "./support/schema-stub";

const TABLES = 42;
const EDGES = 56; // 55 foreign keys; the composite one draws two column pairs
const EVENTS_KEY = "app.events(user_id)->app.users(id)";

async function openSample(page: Page) {
  await stubAgent(page);
  await openApp(page);
  await page.getByRole("button", { name: "Open sample schema" }).click();
  await waitForCanvas(page);
}

async function openProfile(page: Page) {
  const stub = await stubAgent(page, { profiles: [stubProfile()] });
  await openApp(page);
  await waitForCanvas(page);
  return stub;
}

const explorer = (page: Page) => page.getByRole("tree", { name: "Schemas, tables and columns" });

test.describe("ERD canvas", () => {
  test("Open sample schema draws every table and relationship, labelled SAMPLE", async ({ page }) => {
    await openSample(page);
    await expect(page.getByTestId("sample-badge")).toHaveText(/Sample/);
    await expect(page.getByTestId("table-node")).toHaveCount(TABLES);
    await expect(page.locator(".react-flow__edge")).toHaveCount(EDGES);
    await expect(page.getByRole("contentinfo")).toContainText("Sample schema");
    await expect(page.getByRole("contentinfo")).toContainText("schema snapshot");
    // The stub, the partitioned table, the view and the matview all render with their kind.
    await expect(tableNode(page, "geo.countries")).toHaveAttribute("data-kind", "stub");
    await expect(tableNode(page, "app.audit_log")).toHaveAttribute("data-kind", "partitioned_table");
    await expect(tableNode(page, "shop.order_summaries")).toHaveAttribute("data-kind", "view");
    await expect(tableNode(page, "billing.mrr_by_plan")).toHaveAttribute("data-kind", "materialized_view");
    expect((await layoutMeasures(page))[0]).toMatchObject({ algorithm: "layered", nodes: TABLES });

    await page.getByRole("button", { name: "Exit sample" }).click();
    await expect(page.getByRole("heading", { name: "Connect a database" })).toBeVisible();
  });

  test("the minimap draws the tables and a click there moves the canvas", async ({ page }) => {
    await openSample(page);
    const minimap = page.getByTestId("erd-minimap");
    await expect(minimap).toBeVisible();
    // The table layer has drawn something besides its background.
    const drawn = await minimap
      .locator("canvas")
      .first()
      .evaluate((c: HTMLCanvasElement) => {
        const ctx = c.getContext("2d");
        if (!ctx) return 0;
        const { data } = ctx.getImageData(0, 0, c.width, c.height);
        const colours = new Set<number>();
        for (let i = 0; i < data.length; i += 4) colours.add((data[i] << 16) | (data[i + 1] << 8) | (data[i + 2] ?? 0));
        return colours.size;
      });
    expect(drawn).toBeGreaterThan(1);

    const before = await viewport(page);
    const box = await minimap.boundingBox();
    if (!box) throw new Error("minimap not laid out");
    await page.mouse.click(box.x + 15, box.y + 15);
    await expect.poll(async () => (await viewport(page)).x).not.toBe(before.x);

    await page.getByRole("toolbar", { name: "Canvas tools" }).getByRole("button", { name: "Hide minimap" }).click();
    await expect(minimap).toHaveCount(0);
  });

  test("a very large schema paints its overview on a canvas, and clicking a table there selects it", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    const model = syntheticModel(2500, 3000);
    await stubAgent(page, { profiles: [stubProfile()], schema: model });
    await openApp(page);
    await waitForCanvas(page, 90_000);
    await expect(page.getByTestId("erd-overview-canvas")).toHaveCount(1);
    await expect(page.locator('[data-testid="table-node"]')).toHaveCount(0);

    // A table well inside the pane, wherever the layout put it.
    const ids = (model.relations ?? []).map((r) => `${r.schema}.${r.name}`);
    const target = await page.evaluate((ids) => {
      const probe = window.__dbscopeCanvas;
      const v = probe?.getViewport();
      const pane = document.querySelector('[data-testid="erd-canvas"]')?.getBoundingClientRect();
      if (!probe || !v || !pane) return null;
      for (const id of ids) {
        const p = probe.positionOf(id);
        if (!p) continue;
        // The middle of the card: at overview zoom a screen pixel is 4 to 20 flow units.
        const x = pane.left + v.x + (p.x + 140) * v.zoom;
        const y = pane.top + v.y + (p.y + 30) * v.zoom;
        if (x > pane.left + 60 && x < pane.right - 260 && y > pane.top + 60 && y < pane.bottom - 200)
          return { id, x, y };
      }
      return null;
    }, ids);
    if (!target) throw new Error("no table inside the pane");
    const { id } = target;
    const tools = page.getByRole("toolbar", { name: "Canvas tools" });
    await expect(tools.getByRole("button", { name: "Focus on the selected table" })).toBeDisabled();
    await page.mouse.click(target.x, target.y);
    await expect(tools.getByRole("button", { name: "Focus on the selected table" })).toBeEnabled();

    // Double-clicking a painted table opens its details, as on a card.
    await page.mouse.dblclick(target.x, target.y);
    await expect(page.getByTestId("table-details")).toBeVisible();
    await page.getByRole("button", { name: "Close details" }).click();

    // Fit view still sees the hidden tables: from somewhere else it returns to the first fit.
    const fitted = await viewport(page);
    await page.evaluate(() => window.__dbscopeCanvas?.setViewport({ x: 5000, y: 5000, zoom: 0.07 }));
    await fitAll(page);
    await expect.poll(async () => Math.round((await viewport(page)).x)).toBe(Math.round(fitted.x));
    // Zooming in brings the cards back, selection kept.
    await centreOn(page, id, 0.6);
    await expect(tableNode(page, id)).toHaveAttribute("data-mark", "selected");
    await expect(page.getByTestId("erd-overview-canvas")).toHaveCount(0);
  });

  test("search finds a column, Enter pans to it and Esc clears", async ({ page }) => {
    await openSample(page);
    const before = await viewport(page);
    await page.keyboard.press("/");
    const search = page.getByRole("searchbox", { name: "Search tables and columns" });
    await expect(search).toBeFocused();
    await search.fill("tracking");
    await expect(page.locator("#erd-search-status")).toHaveText("1 matches");
    await search.press("Enter");
    await expect(page.locator("#erd-search-status")).toHaveText("1 of 1");

    // The current match is also selected, so F and Enter act on it.
    const shipments = tableNode(page, "shop.shipments");
    await expect(shipments).toHaveAttribute("data-mark", "selected");
    await expect(shipments).toHaveAttribute("data-detail", "full");
    await expect(shipments.locator('[data-column="tracking_number"]')).toHaveAttribute("data-mark", "current-match");
    await expect(shipments).toBeInViewport();
    expect(await viewport(page)).not.toEqual(before);
    await fitAll(page);
    await expect(page.locator('[data-testid="table-node"][data-dim]')).toHaveCount(41);

    await search.focus();
    await search.press("Escape");
    await expect(search).toHaveValue("");
    await expect(page.getByTestId("erd-canvas")).toBeFocused();
    await expect(page.locator('[data-testid="table-node"][data-dim]')).toHaveCount(0);
  });

  test("focus mode dims everything beyond N hops of the selected table", async ({ page }) => {
    await openSample(page);
    await explorer(page)
      .getByRole("treeitem", { name: /^refunds,/ })
      .click();
    await expect(tableNode(page, "shop.refunds")).toHaveAttribute("data-mark", "selected");
    await page.keyboard.press("f");
    await expect(page.getByTestId("live-region")).toContainText("Focus mode: 3 tables within 1 hop of shop.refunds");
    await fitAll(page);

    const dimmed = page.locator('[data-testid="table-node"][data-dim]');
    await expect(dimmed).toHaveCount(TABLES - 3);
    for (const id of ["shop.refunds", "shop.orders", "shop.payments"]) {
      await expect(tableNode(page, id)).not.toHaveAttribute("data-dim", "true");
    }
    await page.getByRole("toolbar", { name: "Canvas tools" }).getByRole("radio", { name: "2" }).click();
    await expect(tableNode(page, "shop.customers")).not.toHaveAttribute("data-dim", "true");
    await expect(dimmed).not.toHaveCount(TABLES - 3);
    await page.keyboard.press("f");
    await expect(dimmed).toHaveCount(0);
  });

  test("collapse one table, collapse all and expand all", async ({ page }) => {
    await openSample(page);
    await centreOn(page, "shop.orders", 1);
    const orders = tableNode(page, "shop.orders");
    await expect(orders.locator("[data-column]")).toHaveCount(7);
    await orders.getByRole("button", { name: "Collapse orders" }).click();
    await expect(orders).toHaveAttribute("data-collapsed", "true");
    // PK and FK columns stay: id, customer_id, billing_address_id, shipping_address_id.
    await expect(orders.locator("[data-column]")).toHaveCount(4);
    await expect(orders).toContainText("+3 more columns");

    const tools = page.getByRole("toolbar", { name: "Canvas tools" });
    await tools.getByRole("button", { name: "Expand all tables" }).click();
    await expect(page.locator('[data-testid="table-node"][data-collapsed]')).toHaveCount(0);
    await tools.getByRole("button", { name: "Collapse all tables to key columns" }).click();
    await fitAll(page);
    await expect(page.locator('[data-testid="table-node"][data-collapsed]')).toHaveCount(TABLES - 1); // not the stub
    await tools.getByRole("button", { name: "Expand all tables" }).click();
    await expect(page.locator('[data-testid="table-node"][data-collapsed]')).toHaveCount(0);
  });

  test("switching layout runs grid and force in the worker and moves the tables", async ({ page }) => {
    await openSample(page);
    const layered = await nodePosition(page, "shop.orders");
    const tools = page.getByRole("toolbar", { name: "Canvas tools" });

    await tools.getByRole("radio", { name: "Grid" }).click();
    await expect.poll(async () => (await layoutMeasures(page)).map((m) => m.algorithm)).toEqual(["layered", "grid"]);
    await waitForCanvas(page);
    const grid = await nodePosition(page, "shop.orders");
    expect(grid).not.toEqual(layered);

    await tools.getByRole("radio", { name: "Force" }).click();
    await expect
      .poll(async () => (await layoutMeasures(page)).map((m) => m.algorithm))
      .toEqual(["layered", "grid", "force"]);
    await waitForCanvas(page);
    expect(await nodePosition(page, "shop.orders")).not.toEqual(grid);
    await expect(page.getByTestId("table-node")).toHaveCount(TABLES);
  });

  test("accepting an inferred foreign key saves it and draws it solid; rejecting removes it", async ({ page }) => {
    test.slow(); // two decisions, a reload and a second layout
    const { schema } = await openProfile(page);
    await centreOn(page, "app.events", 0.8);

    const events = page.locator(`[data-testid="inferred-fk-label"][data-fk="${EVENTS_KEY}"]`);
    await expect(events).toHaveText("FK? 80%");
    await expect(events).toHaveAttribute("title", /confidence 80%/);
    await events.click();
    await page.getByRole("dialog").getByRole("button", { name: "Accept" }).click();
    await expect(events).toHaveText("accepted");
    expect(schema.decisionRequests).toEqual([{ key: EVENTS_KEY, status: "accepted" }]);
    await expect(page.locator(`.react-flow__edge[data-id="app.events|${EVENTS_KEY}#0"] path.erd-edge`)).not.toHaveClass(
      /erd-edge-inferred/,
    );
    await expect(page.locator(`.react-flow__edge[data-id="app.events|${EVENTS_KEY}#0"] path.erd-edge`)).toHaveClass(
      /erd-edge-accepted/,
    );

    const refunds = page.locator(
      '[data-testid="inferred-fk-label"][data-fk="shop.refunds(payment_id)->shop.payments(id)"]',
    );
    await centreOn(page, "shop.refunds", 0.8);
    await refunds.click();
    await page.getByRole("dialog").getByRole("button", { name: "Reject" }).click();
    await expect(refunds).toHaveCount(0);
    await fitAll(page);
    await expect(page.locator(".react-flow__edge")).toHaveCount(EDGES - 1);

    // The agent applies stored decisions on the next read.
    await page.reload();
    await waitForCanvas(page);
    await fitAll(page);
    await expect(page.locator(".react-flow__edge")).toHaveCount(EDGES - 1);
    await centreOn(page, "app.events", 0.8);
    await expect(events).toHaveText("accepted");
  });

  test("dragged positions and collapsed tables are saved and restored after a reload", async ({ page }) => {
    const { schema } = await openProfile(page);
    await centreOn(page, "shop.orders", 1);
    const before = await nodePosition(page, "shop.orders");
    const header = tableNode(page, "shop.orders").getByText("orders", { exact: true });
    const box = await header.boundingBox();
    if (!box) throw new Error("orders header not visible");
    await page.mouse.move(box.x + 10, box.y + 5);
    await page.mouse.down();
    await page.mouse.move(box.x + 110, box.y + 65, { steps: 8 });
    await page.mouse.up();
    const after = await nodePosition(page, "shop.orders");
    await centreOn(page, "app.users", 1);
    await tableNode(page, "app.users").getByRole("button", { name: "Collapse users" }).click();
    // React Flow starts the drag after a small threshold, so the node moves a little less than the pointer.
    expect(after.x - before.x).toBeGreaterThan(60);
    expect(after.y - before.y).toBeGreaterThan(30);

    // One debounced PUT (1 s) carries both changes.
    await expect.poll(() => schema.layoutPuts.at(-1)?.collapsed ?? [], { timeout: 5000 }).toEqual(["app.users"]);
    const saved = schema.layoutPuts.at(-1);
    expect(saved?.key).toBe("app,billing,shop");
    expect(saved?.positions["shop.orders"]).toEqual({ x: Math.round(after.x), y: Math.round(after.y) });

    await page.reload();
    await waitForCanvas(page);
    expect(await nodePosition(page, "shop.orders")).toEqual({ x: Math.round(after.x), y: Math.round(after.y) });
    await expect(tableNode(page, "app.users")).toHaveAttribute("data-collapsed", "true");
    expect(await layoutMeasures(page)).toEqual([]); // restored, not re-laid out
  });

  test("keyboard: arrows move between tables, Enter opens details, Tab follows a foreign key", async ({ page }) => {
    await openSample(page);
    const canvas = page.getByTestId("erd-canvas");
    await canvas.focus();
    await page.keyboard.press("ArrowRight");
    const selected = page.locator('[data-testid="table-node"][data-mark="selected"]');
    await expect(selected).toHaveCount(1);
    const first = await selected.getAttribute("data-node-id");
    await page.keyboard.press("ArrowDown");
    await expect(selected).toHaveCount(1);
    expect(await selected.getAttribute("data-node-id")).not.toBe(first);

    // Start from a known table: select it in the explorer, then return to the canvas.
    await explorer(page)
      .getByRole("treeitem", { name: /^orders,/ })
      .click();
    await canvas.focus();
    await page.keyboard.press("Tab");
    await expect(tableNode(page, "shop.addresses")).toHaveAttribute("data-mark", "selected");
    await expect(page.getByTestId("live-region")).toContainText(
      "Followed billing_address_id (1 of 3) to shop.addresses",
    );
    await expect(canvas).toBeFocused();
    await page.keyboard.press("Shift+Tab");
    await expect(tableNode(page, "shop.orders")).toHaveAttribute("data-mark", "selected");
    await page.keyboard.press("Tab");
    await expect(tableNode(page, "shop.customers")).toHaveAttribute("data-mark", "selected");

    await page.keyboard.press("Enter");
    const details = page.getByTestId("table-details");
    await expect(details.getByRole("heading", { name: "shop.customers" })).toBeFocused();
    await expect(details).toContainText("Referenced by");
    await page.keyboard.press("Escape");
    await expect(details).toHaveCount(0);
    await expect(canvas).toBeFocused();
    await page.keyboard.press("Escape"); // the followed relationship's highlight
    await page.keyboard.press("Escape"); // the selection
    await expect(selected).toHaveCount(0);
    // With nothing selected Tab leaves the canvas instead of trapping focus.
    await page.keyboard.press("Tab");
    await expect(canvas).not.toBeFocused();
  });

  test("selecting a column in the explorer selects and reveals it on the canvas", async ({ page }) => {
    await openSample(page);
    await explorer(page)
      .getByRole("treeitem", { name: /^payments,/ })
      .click();
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("Enter");
    const payments = tableNode(page, "shop.payments");
    await expect(payments).toHaveAttribute("data-mark", "selected");
    await expect(payments).toHaveAttribute("data-detail", "full");
    await expect(payments).toBeInViewport();
  });

  test("an introspection failure names the cause, offers a retry and the sample", async ({ page }) => {
    await stubAgent(page, {
      profiles: [stubProfile()],
      schemaError: { status: 502, code: "upstream", message: "dial tcp 10.0.0.5:5432: i/o timeout", detail: "network" },
    });
    await openApp(page);
    const alert = page.getByRole("main").getByRole("alert");
    await expect(alert).toContainText("Could not reach the server");
    await expect(alert).toContainText("dial tcp 10.0.0.5:5432: i/o timeout");
    await expect(alert).toContainText("listen_addresses");
    await expect(page.getByRole("contentinfo")).toContainText("Schema not read");
    await alert.getByRole("button", { name: "Open the sample schema instead" }).click();
    await waitForCanvas(page);
    await expect(page.getByTestId("sample-badge")).toBeVisible();
  });

  test("the schema picker saves the chosen schemas to the profile and reads the schema again", async ({ page }) => {
    const { schema } = await openProfile(page);
    const puts: { schemas: string[] }[] = [];
    page.on("request", (r) => {
      if (r.method() === "PUT" && /\/api\/profiles\/[^/]+$/.test(new URL(r.url()).pathname))
        puts.push(r.postDataJSON());
    });
    const before = schema.schemaRequests;
    await page.getByRole("button", { name: "Choose schemas (now: All schemas)" }).click();
    const dialog = page.getByRole("dialog", { name: "Schemas to show" });
    await dialog.getByRole("checkbox", { name: "shop" }).click();
    await dialog.getByRole("checkbox", { name: "app" }).click();
    await dialog.getByRole("button", { name: "Apply and reload" }).click();
    await expect(dialog).toBeHidden();
    expect(puts.at(-1)?.schemas).toEqual(["app", "shop"]);
    expect(puts.at(-1)).not.toHaveProperty("password");
    await expect(page.getByRole("button", { name: "Choose schemas (now: 2 schemas)" })).toBeVisible();
    await expect.poll(() => schema.schemaRequests).toBeGreaterThan(before);
  });
});
