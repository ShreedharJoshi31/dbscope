import { defineConfig } from "@playwright/test";

const PORT = 4173;
const isCI = Boolean(process.env.CI);

export default defineConfig({
  testDir: "e2e",
  // Baselines are per platform: font rasterisation differs, and docs/ARCHITECTURE.md pins CI baselines to Linux.
  snapshotPathTemplate: "{testDir}/__screenshots__/{projectName}-{platform}/{arg}{ext}",
  fullyParallel: true,
  forbidOnly: isCI,
  retries: isCI ? 1 : 0,
  reporter: isCI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: "retain-on-failure",
  },
  expect: {
    toHaveScreenshot: { animations: "disabled", caret: "hide", maxDiffPixelRatio: 0.002 },
  },
  projects: [
    {
      name: "chromium",
      // No device preset: its Windows user agent would make the app expect Ctrl while
      // ControlOrMeta sends Cmd on macOS hosts. The real platform keeps the two in step.
      use: { browserName: "chromium", viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 },
    },
  ],
  webServer: {
    // Build first so the preview never serves a stale bundle; the Go backend is not needed (e2e stubs /api).
    command: "pnpm exec vite build && pnpm preview",
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: !isCI,
    timeout: 120_000,
  },
});
