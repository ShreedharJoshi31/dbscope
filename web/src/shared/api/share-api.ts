import { apiFetch, request } from "@/shared/api/client";

/** How an import combines the file with the connection's state (internal/share). */
export type ShareMode = "merge" | "replace";

export type ShareChange = "added" | "changed" | "unchanged" | "kept" | "removed";

export interface ShareItem {
  /** The layout key, for table positions. */
  layout?: string;
  key: string;
  change: ShareChange;
  detail?: string;
  /** A table or foreign key this database does not have. */
  orphan?: boolean;
}

export interface ShareSection {
  items: ShareItem[];
  counts: Partial<Record<ShareChange, number>>;
  orphans: number;
}

export interface SharePreview {
  mode: ShareMode;
  /** False when the database could not be read, so orphans are not flagged. */
  schemaChecked: boolean;
  tables: ShareSection;
  foreignKeys: ShareSection;
  glossary: ShareSection;
}

const sharedPath = (profileId: string) => `/api/profiles/${encodeURIComponent(profileId)}/shared`;

/** GET …/shared: the connection's layouts, FK decisions and glossary as the team file. */
export async function exportShared(profileId: string): Promise<{ filename: string; text: string }> {
  const response = await request(sharedPath(profileId));
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const filename = /filename="([^"]+)"/.exec(disposition)?.[1] ?? "dbscope.dbscope.json";
  return { filename, text: await response.text() };
}

export function previewShared(profileId: string, file: unknown, mode: ShareMode, signal?: AbortSignal) {
  return apiFetch<SharePreview>(`${sharedPath(profileId)}/preview?mode=${mode}`, {
    method: "POST",
    body: file,
    signal,
  });
}

export function importShared(profileId: string, file: unknown, mode: ShareMode) {
  return apiFetch<SharePreview>(`${sharedPath(profileId)}/import?mode=${mode}`, { method: "POST", body: file });
}
