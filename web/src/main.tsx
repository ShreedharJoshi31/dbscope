import "@/app/styles.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "@/app/app";
import { applyInitialTheme } from "@/app/theme";
import { bootstrapToken } from "@/shared/api";

bootstrapToken();
applyInitialTheme();

const root = document.getElementById("root");
if (!root) throw new Error("index.html is missing #root");

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
