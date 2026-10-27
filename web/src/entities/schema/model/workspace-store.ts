import { create } from "zustand";
import { safeSessionStorage } from "@/shared/lib/storage";

export type SchemaSource = "profile" | "sample";

const SOURCE_KEY = "dbscope.schemaSource";

interface WorkspaceState {
  /**
   * Where the canvas and explorer read the schema from. "sample" is the
   * built-in read-only sample (GET /api/sample/schema) and works with no
   * connection. Kept per tab, so a reload stays in the sample.
   */
  source: SchemaSource;
  setSource: (source: SchemaSource) => void;
}

export const useWorkspaceStore = create<WorkspaceState>((set) => ({
  source: safeSessionStorage.getItem(SOURCE_KEY) === "sample" ? "sample" : "profile",
  setSource: (source) => {
    if (source === "sample") safeSessionStorage.setItem(SOURCE_KEY, source);
    else safeSessionStorage.removeItem(SOURCE_KEY);
    set({ source });
  },
}));

export function openSampleWorkspace(): void {
  useWorkspaceStore.getState().setSource("sample");
}

export function closeSampleWorkspace(): void {
  useWorkspaceStore.getState().setSource("profile");
}
