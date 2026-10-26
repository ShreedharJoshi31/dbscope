import { create } from "zustand";
import { safeLocalStorage } from "@/shared/lib/storage";
import { useMediaQuery } from "@/shared/lib/use-media-query";

export type ThemePreference = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

const THEME_KEY = "dbscope.theme";
export const DARK_QUERY = "(prefers-color-scheme: dark)";

function readPreference(): ThemePreference {
  const stored = safeLocalStorage.getItem(THEME_KEY);
  return stored === "light" || stored === "dark" ? stored : "system";
}

interface ThemeState {
  preference: ThemePreference;
  setPreference: (preference: ThemePreference) => void;
}

export const useThemeStore = create<ThemeState>((set) => ({
  preference: readPreference(),
  setPreference: (preference) => {
    if (preference === "system") safeLocalStorage.removeItem(THEME_KEY);
    else safeLocalStorage.setItem(THEME_KEY, preference);
    set({ preference });
  },
}));

export function resolveTheme(preference: ThemePreference, systemPrefersDark: boolean): ResolvedTheme {
  if (preference === "system") return systemPrefersDark ? "dark" : "light";
  return preference;
}

export function useResolvedTheme(): ResolvedTheme {
  const preference = useThemeStore((s) => s.preference);
  return resolveTheme(preference, useMediaQuery(DARK_QUERY));
}

/** Flips between light and dark from whatever is showing now. */
export function toggleTheme(): void {
  const { preference, setPreference } = useThemeStore.getState();
  const current = resolveTheme(preference, window.matchMedia(DARK_QUERY).matches);
  setPreference(current === "dark" ? "light" : "dark");
}
