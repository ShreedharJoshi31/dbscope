import type { ClientProblem, MigrationSource } from "@/entities/migration-finding";
import { MIGRATION_LIMITS } from "@/shared/api";

/**
 * Turns dropped or picked files into what the agent analyzes. Directories are
 * walked (sorted by natural name, so V2 comes before V10), relative paths are
 * kept so framework detection works (Prisma needs `migrations/<dir>/migration.sql`),
 * and top-level items keep the order they were dropped in.
 */

/** What the agent reads: SQL and CQL files, Flyway `<file>.sql.conf` and Atlas `atlas.sum`. */
export function isMigrationFile(name: string): boolean {
  const base = name.slice(name.lastIndexOf("/") + 1).toLowerCase();
  return isSqlFile(base) || base.endsWith(".sql.conf") || base === "atlas.sum";
}

/** A statement file: .sql, .cql for Cassandra and ScyllaDB tools, or .http for elasticsearch-evolution. */
export function isSqlFile(name: string): boolean {
  const lower = name.toLowerCase();
  return lower.endsWith(".sql") || lower.endsWith(".cql") || lower.endsWith(".http");
}

/** A file and the relative path it was found under. */
export interface PickedFile {
  path: string;
  file: File;
}

export interface CollectedFiles {
  picked: PickedFile[];
  /** Names that are not migration files (not sent). */
  ignored: string[];
}

const natural = new Intl.Collator("en", { numeric: true, sensitivity: "base" });

function readAllEntries(reader: FileSystemDirectoryReader): Promise<FileSystemEntry[]> {
  // readEntries returns batches (100 in Chromium) until an empty one.
  return new Promise((resolve, reject) => {
    const all: FileSystemEntry[] = [];
    const next = () =>
      reader.readEntries((batch) => {
        if (batch.length === 0) resolve(all);
        else {
          all.push(...batch);
          next();
        }
      }, reject);
    next();
  });
}

function entryFile(entry: FileSystemFileEntry): Promise<File> {
  return new Promise((resolve, reject) => entry.file(resolve, reject));
}

const MAX_WALK = 5000;

async function walkEntry(entry: FileSystemEntry, prefix: string, out: PickedFile[], ignored: string[]) {
  if (out.length + ignored.length > MAX_WALK) return;
  const path = prefix ? `${prefix}/${entry.name}` : entry.name;
  if (entry.isFile) {
    if (isMigrationFile(path)) out.push({ path, file: await entryFile(entry as FileSystemFileEntry) });
    else ignored.push(path);
    return;
  }
  if (!entry.isDirectory || entry.name.startsWith(".")) return;
  const children = await readAllEntries((entry as FileSystemDirectoryEntry).createReader());
  children.sort((a, b) => natural.compare(a.name, b.name));
  for (const child of children) await walkEntry(child, path, out, ignored);
}

/**
 * Reads a drop. Call synchronously from the drop handler: DataTransfer items
 * are only readable during the event, so entries are taken before any await.
 */
export function collectDrop(data: DataTransfer): Promise<CollectedFiles> {
  const items = Array.from(data.items ?? []).filter((item) => item.kind === "file");
  const roots = items.map((item) => ({ entry: item.webkitGetAsEntry?.() ?? null, file: item.getAsFile() }));
  const fallback = roots.length === 0 ? Array.from(data.files ?? []) : [];
  return (async () => {
    const picked: PickedFile[] = [];
    const ignored: string[] = [];
    for (const root of roots) {
      if (root.entry) await walkEntry(root.entry, "", picked, ignored);
      else if (root.file && isMigrationFile(root.file.name)) picked.push({ path: root.file.name, file: root.file });
      else if (root.file) ignored.push(root.file.name);
    }
    for (const file of fallback) {
      if (isMigrationFile(file.name)) picked.push({ path: file.name, file });
      else ignored.push(file.name);
    }
    return { picked, ignored };
  })();
}

/** Files from an `<input type="file" multiple>` (with webkitdirectory they carry a relative path). */
export function collectFileList(list: FileList | readonly File[]): CollectedFiles {
  const picked: PickedFile[] = [];
  const ignored: string[] = [];
  const files = Array.from(list);
  const fromDirectory = files.some((f) => f.webkitRelativePath);
  if (fromDirectory) files.sort((a, b) => natural.compare(a.webkitRelativePath, b.webkitRelativePath));
  for (const file of files) {
    const path = file.webkitRelativePath || file.name;
    if (isMigrationFile(path)) picked.push({ path, file });
    else ignored.push(path);
  }
  return { picked, ignored };
}

/** The File System Access API directory handle, as far as this code uses it. */
export interface DirectoryHandleLike {
  kind: "directory";
  name: string;
  values: () => AsyncIterable<FileHandleLike | DirectoryHandleLike>;
}

export interface FileHandleLike {
  kind: "file";
  name: string;
  getFile: () => Promise<File>;
}

/** Walks a folder chosen with showDirectoryPicker. The folder's own name is the path prefix. */
export async function collectDirectory(handle: DirectoryHandleLike): Promise<CollectedFiles> {
  const picked: PickedFile[] = [];
  const ignored: string[] = [];
  async function walk(dir: DirectoryHandleLike, prefix: string) {
    const children: (FileHandleLike | DirectoryHandleLike)[] = [];
    for await (const child of dir.values()) children.push(child);
    children.sort((a, b) => natural.compare(a.name, b.name));
    for (const child of children) {
      if (picked.length + ignored.length > MAX_WALK) return;
      const path = `${prefix}/${child.name}`;
      if (child.kind === "directory") {
        if (!child.name.startsWith(".")) await walk(child, path);
      } else if (isMigrationFile(path)) picked.push({ path, file: await child.getFile() });
      else ignored.push(path);
    }
  }
  await walk(handle, handle.name);
  return { picked, ignored };
}

type DirectoryPicker = (options?: { mode?: "read"; id?: string }) => Promise<DirectoryHandleLike>;

/** showDirectoryPicker where the browser has it (Chromium); elsewhere callers fall back to a multi-file input. */
export function directoryPicker(): DirectoryPicker | null {
  const picker = (window as unknown as { showDirectoryPicker?: DirectoryPicker }).showDirectoryPicker;
  return typeof picker === "function" ? picker.bind(window) : null;
}

const encoder = new TextEncoder();

export function utf8Length(text: string): number {
  return encoder.encode(text).length;
}

/** Reads the picked files as text. Unreadable files are reported, not silently dropped. */
export async function readPicked(
  picked: readonly PickedFile[],
): Promise<{ sources: MigrationSource[]; failed: string[] }> {
  const failed: string[] = [];
  const sources: MigrationSource[] = [];
  for (const p of picked) {
    try {
      sources.push({ name: p.path, sql: await p.file.text() });
    } catch {
      failed.push(p.path);
    }
  }
  return { sources, failed };
}

/** Checks the limits the agent enforces before sending anything (docs/API.md Phase 4). */
export function validateSources(sources: readonly MigrationSource[], ignored: readonly string[]): ClientProblem | null {
  if (!sources.some((s) => isSqlFile(s.name))) return { code: "no_files", ignored: [...ignored] };
  if (sources.length > MIGRATION_LIMITS.files) {
    return { code: "too_many", count: sources.length, limit: MIGRATION_LIMITS.files };
  }
  const badNames = sources
    .map((s) => s.name)
    .filter((n) => n.trim() === "" || n.length > MIGRATION_LIMITS.nameLength || n.includes("\0"));
  if (badNames.length > 0) return { code: "bad_name", names: badNames };
  const large = sources
    .map((s) => ({ name: s.name, bytes: utf8Length(s.sql) }))
    .filter((f) => f.bytes > MIGRATION_LIMITS.fileBytes);
  if (large.length > 0) return { code: "too_large", files: large, limit: MIGRATION_LIMITS.fileBytes };
  const body = utf8Length(JSON.stringify({ files: sources }));
  if (body > MIGRATION_LIMITS.bodyBytes)
    return { code: "body_too_large", bytes: body, limit: MIGRATION_LIMITS.bodyBytes };
  return null;
}

/** "0042_orders.sql" for one file, "3 migration files" for more (counting SQL files only). */
export function reviewTitle(sources: readonly Pick<MigrationSource, "name">[]): string {
  const sql = sources.filter((s) => isSqlFile(s.name));
  if (sql.length === 1 && sql[0]) return sql[0].name.slice(sql[0].name.lastIndexOf("/") + 1);
  return `${sql.length} migration files`;
}
