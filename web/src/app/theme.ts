import { useLayoutEffect } from "react";
import { DARK_QUERY, type ResolvedTheme, resolveTheme, useResolvedTheme, useThemeStore } from "@/features/toggle-theme";

/** tokens.css reads `data-theme` to pin `color-scheme`; without it the OS setting applies. */
export function applyTheme(theme: ResolvedTheme): void {
  document.documentElement.dataset.theme = theme;
}

/** Called before the first render so the first paint already has the right theme. */
export function applyInitialTheme(): void {
  applyTheme(resolveTheme(useThemeStore.getState().preference, window.matchMedia(DARK_QUERY).matches));
}

export function useThemeSync(): void {
  const resolved = useResolvedTheme();
  useLayoutEffect(() => applyTheme(resolved), [resolved]);
}
