/// <reference types="vitest/config" />
import { writeFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// web/embed.go embeds dist/; emptyOutDir deletes the tracked .gitkeep, so put it back.
const keepDist: Plugin = {
  name: "dbscope-keep-dist",
  apply: "build",
  closeBundle() {
    writeFileSync(new URL("./dist/.gitkeep", import.meta.url), "");
  },
};

export default defineConfig({
  plugins: [react(), tailwindcss(), keepDist],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": { target: "http://127.0.0.1:4555", changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // The bundle is embedded in the Go binary and served from 127.0.0.1, so size warnings at 500 kB are noise.
    chunkSizeWarningLimit: 800,
  },
  test: {
    environment: "happy-dom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
    restoreMocks: true,
  },
});
