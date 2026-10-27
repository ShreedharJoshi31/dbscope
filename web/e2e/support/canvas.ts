import { expect, type Locator, type Page } from "@playwright/test";

interface CanvasProbe {
  ready: boolean;
  tables: number;
  getViewport: () => { x: number; y: number; zoom: number };
  setViewport: (viewport: { x: number; y: number; zoom: number }) => void;
  positionOf: (id: string) => { x: number; y: number } | null;
}

declare global {
  interface Window {
    __dbscopeCanvas?: CanvasProbe;
  }
}

/** Waits until the canvas has laid out its tables and no layout is running. */
export async function waitForCanvas(page: Page, timeout = 30_000) {
  await expect.poll(() => page.evaluate(() => window.__dbscopeCanvas?.ready ?? false), { timeout }).toBe(true);
  await expect(page.getByTestId("layout-status")).toHaveCount(0, { timeout });
}

export function tableNode(page: Page, id: string): Locator {
  return page.locator(`[data-testid="table-node"][data-node-id="${id}"]`);
}

/** A table's canvas position (also for off-screen tables, which are not in the DOM). */
export async function nodePosition(page: Page, id: string): Promise<{ x: number; y: number }> {
  const p = await page.evaluate((nodeId) => window.__dbscopeCanvas?.positionOf(nodeId) ?? null, id);
  if (!p) throw new Error(`no position for ${id}`);
  return p;
}

/** Fits every table in view, so all of them are rendered (onlyRenderVisibleElements). */
export async function fitAll(page: Page) {
  await page.getByRole("button", { name: "Fit all tables in view" }).click();
}

export async function viewport(page: Page) {
  return page.evaluate(() => window.__dbscopeCanvas?.getViewport() ?? { x: 0, y: 0, zoom: 0 });
}

/** Centres the viewport on a node at the given zoom, without animation. */
export async function centreOn(page: Page, id: string, zoom = 1) {
  const p = await nodePosition(page, id);
  const box = await page.getByTestId("erd-canvas").boundingBox();
  if (!box) throw new Error("canvas not visible");
  await page.evaluate(({ x, y, zoom }) => window.__dbscopeCanvas?.setViewport({ x, y, zoom }), {
    x: box.width / 2 - (p.x + 140) * zoom,
    y: box.height / 3 - p.y * zoom,
    zoom,
  });
}

export async function layoutMeasures(page: Page) {
  return page.evaluate(() =>
    performance.getEntriesByName("dbscope:layout").map((e) => ({
      ms: e.duration,
      ...((e as PerformanceMeasure).detail as { algorithm: string; nodes: number; edges: number }),
    })),
  );
}

/** document.fonts.ready resolves before lazily requested faces start loading; request them explicitly. */
export async function loadFonts(page: Page) {
  await page.evaluate(async () => {
    await Promise.all([
      document.fonts.load("13px 'Geist Variable'"),
      document.fonts.load("13px 'Geist Mono Variable'"),
    ]);
    await document.fonts.ready;
  });
}
