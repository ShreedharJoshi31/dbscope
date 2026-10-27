import type { QueryClient } from "@tanstack/react-query";
import { create } from "zustand";
import { exportShared, importShared, type Profile, type ShareMode, type SharePreview } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { flushLayoutSaves, useLayoutEpoch } from "@/shared/lib/layout-sync";

interface ImportDialogState {
  open: boolean;
  setOpen: (open: boolean) => void;
}

export const useImportSharedDialog = create<ImportDialogState>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
}));

/** Palette: "Import team file…". */
export function openImportShared(): void {
  useImportSharedDialog.getState().setOpen(true);
}

/** Downloads the connection's team file, named after it. */
export async function downloadShared(profile: Profile): Promise<void> {
  try {
    const { filename, text } = await exportShared(profile.id);
    const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    // Revoked later: some browsers start the download after click returns.
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
    announce(`Exported ${filename}. Commit it to the repository to share it.`);
  } catch (error) {
    announce(`Could not export the team file: ${error instanceof Error ? error.message : "unknown error"}`);
  }
}

/** Reads a chosen file as JSON, or explains why it is not one. */
export async function readTeamFile(file: File): Promise<{ data: unknown } | { error: string }> {
  if (file.size > 8 << 20) return { error: `${file.name} is larger than 8 MiB.` };
  try {
    return { data: JSON.parse(await file.text()) as unknown };
  } catch {
    return { error: `${file.name} is not JSON. Choose a file exported with “Export team file”.` };
  }
}

/**
 * Imports data and refreshes everything that shows it. Pending canvas saves go
 * out first and canvases then stop saving, so they cannot overwrite the import;
 * they remount afterwards, also when the import fails, to read the saved layout.
 * With snapshot, the state before the import is returned for Undo.
 */
export async function applyImport(
  client: QueryClient,
  profileId: string,
  data: unknown,
  mode: ShareMode,
  options: { snapshot?: boolean } = {},
): Promise<{ result: SharePreview; previous?: unknown }> {
  await flushLayoutSaves();
  try {
    const previous = options.snapshot ? (JSON.parse((await exportShared(profileId)).text) as unknown) : undefined;
    const result = await importShared(profileId, data, mode);
    await client.invalidateQueries({ queryKey: ["profiles", profileId] });
    return { result, previous };
  } finally {
    useLayoutEpoch.getState().bump();
  }
}
