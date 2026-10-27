import { expect, type Page, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { layoutMeasures, waitForCanvas } from "./support/canvas";
import { syntheticModel } from "./support/schema-stub";

/**
 * Frame timing under a scripted pan and zoom. The page moves the viewport once
 * per requestAnimationFrame callback and records the callback timestamps; the
 * perf project runs Chromium with vsync and the frame-rate cap off, so an
 * interval is the real cost of a frame (style, layout, paint, composite)
 * rather than a multiple of 16.7 ms.
 */
interface FrameStats {
  frames: number;
  /** Table nodes in the DOM, averaged over sampled frames. */
  avgNodes: number;
  /** Level-of-detail values seen on nodes during the run. */
  details: string[];
  medianMs: number;
  p95Ms: number;
  maxMs: number;
  fps: number;
}

interface Script {
  zoom: { base: number; swing: number };
  /** Pan amplitude in screen pixels; the path is a loop, so the content stays in view. */
  pan: number;
}

async function scriptedPanZoom(page: Page, frames: number, script: Script): Promise<FrameStats> {
  return page.evaluate(
    ({ frames, script }) =>
      new Promise<FrameStats>((resolve) => {
        const probe = window.__dbscopeCanvas;
        if (!probe) throw new Error("canvas probe missing");
        const start = probe.getViewport();
        const pane = document.querySelector('[data-testid="erd-canvas"]')?.getBoundingClientRect();
        const cx = (pane?.width ?? 800) / 2;
        const cy = (pane?.height ?? 600) / 2;
        const stamps: number[] = [];
        const counts: number[] = [];
        const details = new Set<string>();
        let i = 0;
        const step = (t: number) => {
          stamps.push(t);
          if (i >= frames) {
            const intervals = stamps
              .slice(1)
              .map((s, k) => s - (stamps[k] as number))
              .sort((a, b) => a - b);
            const at = (q: number) => intervals[Math.min(intervals.length - 1, Math.floor(q * intervals.length))] ?? 0;
            const median = at(0.5);
            resolve({
              frames: intervals.length,
              avgNodes: Math.round(counts.reduce((a, b) => a + b, 0) / Math.max(1, counts.length)),
              details: [...details].sort(),
              medianMs: Number(median.toFixed(2)),
              p95Ms: Number(at(0.95).toFixed(2)),
              maxMs: Number((intervals.at(-1) ?? 0).toFixed(2)),
              fps: Number((1000 / median).toFixed(1)),
            });
            return;
          }
          if (i % 20 === 10) {
            const nodes = document.querySelectorAll('[data-testid="table-node"]');
            counts.push(nodes.length);
            for (const n of nodes) details.add(n.getAttribute("data-detail") ?? "");
          }
          // Pan along a loop while the zoom breathes around its base, zooming about the pane centre
          // (where fit view put the middle of the schema), so the tables stay on screen.
          const z = script.zoom.base + script.zoom.swing * Math.sin(i / 25);
          const k = z / start.zoom;
          probe.setViewport({
            x: cx - (cx - start.x) * k - script.pan * Math.sin(i / 30),
            y: cy - (cy - start.y) * k - script.pan * 0.6 * Math.cos(i / 30),
            zoom: z,
          });
          i++;
          requestAnimationFrame(step);
        };
        requestAnimationFrame(step);
      }),
    { frames, script },
  );
}

/** Sets the zoom, keeping the point under the pane centre fixed. */
async function zoomAboutCentre(page: Page, zoom: number) {
  await page.evaluate((zoom) => {
    const probe = window.__dbscopeCanvas;
    const pane = document.querySelector('[data-testid="erd-canvas"]')?.getBoundingClientRect();
    if (!probe || !pane) return;
    const v = probe.getViewport();
    const k = zoom / v.zoom;
    const cx = pane.width / 2;
    const cy = pane.height / 2;
    probe.setViewport({ x: cx - (cx - v.x) * k, y: cy - (cy - v.y) * k, zoom });
  }, zoom);
}

async function canvasReadyMs(page: Page): Promise<number> {
  return page.evaluate(() => performance.getEntriesByName("dbscope:canvas-ready")[0]?.duration ?? -1);
}

test.describe("ERD performance", () => {
  test.describe.configure({ mode: "serial" });
  // No animated fit/pan: the script must own the viewport from the first frame.
  test.use({ reducedMotion: "reduce" });

  test("300 tables / 600 FKs: layered layout in the worker and pan/zoom frame times", async ({ page }, info) => {
    test.setTimeout(120_000);
    const model = syntheticModel(300, 600);
    await stubAgent(page, { profiles: [stubProfile()], schema: model });
    await openApp(page);
    await waitForCanvas(page, 90_000);
    const [layout] = await layoutMeasures(page);
    expect(layout).toMatchObject({ algorithm: "layered", nodes: 300, edges: 600 });

    // Detailed zoom just above the level-of-detail threshold: the most column rows on screen at once.
    const stats = await scriptedPanZoom(page, 300, { zoom: { base: 0.6, swing: 0.08 }, pan: 600 });
    await zoomAboutCentre(page, 0.35);
    const headerStats = await scriptedPanZoom(page, 300, { zoom: { base: 0.35, swing: 0.05 }, pan: 400 });

    // Dense worst case: the grid packs tables tightly, so at 0.55 (just above the threshold) the
    // screen is full of cards with every column row drawn.
    await page.getByRole("toolbar", { name: "Canvas tools" }).getByRole("radio", { name: "Grid" }).click();
    await expect.poll(async () => (await layoutMeasures(page)).length).toBe(2);
    await waitForCanvas(page);
    await zoomAboutCentre(page, 0.55);
    const denseStats = await scriptedPanZoom(page, 300, { zoom: { base: 0.55, swing: 0.04 }, pan: 500 });
    const result = {
      tables: 300,
      fks: 600,
      layoutMs: Math.round(layout?.ms ?? -1),
      canvasReadyMs: Math.round(await canvasReadyMs(page)),
      detailed: stats,
      headerOnly: headerStats,
      denseDetailed: denseStats,
    };
    console.log(`PERF ${JSON.stringify(result)}`);
    info.annotations.push({ type: "perf-300", description: JSON.stringify(result) });
    expect(stats.details).toEqual(["full"]);
    expect(stats.avgNodes).toBeGreaterThan(0);
    expect(stats.medianMs).toBeLessThan(16.7);
    expect(denseStats.details).toEqual(["full"]);
    expect(denseStats.medianMs).toBeLessThan(16.7);
  });

  test("1,500 header-only tables: initial render under 3 s and pan frame times", async ({ page }, info) => {
    test.setTimeout(120_000);
    const model = syntheticModel(1500, 3000);
    await stubAgent(page, { profiles: [stubProfile()], schema: model });
    await openApp(page);
    await waitForCanvas(page, 60_000);
    const readyMs = await canvasReadyMs(page);
    const [layout] = await layoutMeasures(page);
    expect(layout).toMatchObject({ algorithm: "grid", nodes: 1500 });

    // Fit view puts 1,500 tables below both thresholds: name-only cards ("far"), one path for all keys.
    const detailsNow = () =>
      page
        .locator('[data-testid="table-node"]')
        .evaluateAll((els) => [...new Set(els.map((el) => el.getAttribute("data-detail")))]);
    const fitZoom = (await page.evaluate(() => window.__dbscopeCanvas?.getViewport().zoom)) ?? 0.1;
    const stats = await scriptedPanZoom(page, 300, { zoom: { base: fitZoom * 1.5, swing: fitZoom * 0.3 }, pan: 300 });

    // Header-only cards (between the thresholds), with per-edge components and viewport culling.
    await zoomAboutCentre(page, 0.35);
    await expect.poll(detailsNow).toEqual(["header"]);
    const headerStats = await scriptedPanZoom(page, 300, { zoom: { base: 0.35, swing: 0.05 }, pan: 400 });
    const result = {
      tables: 1500,
      fks: 3000,
      layoutMs: Math.round(layout?.ms ?? -1),
      initialRenderMs: Math.round(readyMs),
      overview: stats,
      headerOnly: headerStats,
    };
    console.log(`PERF ${JSON.stringify(result)}`);
    info.annotations.push({ type: "perf-1500", description: JSON.stringify(result) });
    expect(stats.details).toEqual(["far"]);
    expect(stats.avgNodes).toBe(1500);
    // 60 fps while panning and zooming the whole schema (24 ms before the canvas minimap).
    expect(stats.medianMs).toBeLessThan(16.7);
    expect(headerStats.details).toEqual(["header"]);
    expect(readyMs).toBeGreaterThan(0);
    expect(readyMs).toBeLessThan(3000);
  });

  test("5,000 tables: overview painted on a canvas, initial render and pan/zoom frame times", async ({
    page,
  }, info) => {
    test.setTimeout(240_000);
    await stubAgent(page, { profiles: [stubProfile()], schema: syntheticModel(5000, 10000) });
    await openApp(page);
    await waitForCanvas(page, 120_000);
    const readyMs = await canvasReadyMs(page);
    // No card per table at overview zoom: one canvas paints them all.
    await expect(page.getByTestId("erd-overview-canvas")).toHaveCount(1);
    await expect(page.locator('[data-testid="table-node"]')).toHaveCount(0);
    const fitZoom = (await page.evaluate(() => window.__dbscopeCanvas?.getViewport().zoom)) ?? 0.05;
    const stats = await scriptedPanZoom(page, 300, { zoom: { base: fitZoom * 1.5, swing: fitZoom * 0.3 }, pan: 300 });
    const result = { tables: 5000, fks: 10000, initialRenderMs: Math.round(readyMs), overview: stats };
    console.log(`PERF ${JSON.stringify(result)}`);
    info.annotations.push({ type: "perf-5000", description: JSON.stringify(result) });
    expect(readyMs).toBeLessThan(4000);
    // The whole run stayed in the painted overview: no cards came back mid-measurement.
    expect(stats.avgNodes).toBe(0);
    expect(stats.details).toEqual([]);
    // Before the canvas overview a zoom step cost 160+ ms at the 95th percentile; the median is the
    // steadier guard (the tail varies by tens of ms between runs).
    expect(stats.medianMs).toBeLessThan(33);
    expect(stats.p95Ms).toBeLessThan(120);
  });
});
