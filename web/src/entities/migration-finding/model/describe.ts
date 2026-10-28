import type { MigrationBase, StatementTx, StatementWork } from "@/shared/api";

export type LockTone = "info" | "warn" | "danger";

interface LockMode {
  /** What the lock blocks, in plain words (PostgreSQL "Table-Level Lock Modes" conflict table). */
  words: string;
  tone: LockTone;
}

const LOCK_MODES: Record<string, LockMode> = {
  "ACCESS SHARE": {
    words: "blocks only ACCESS EXCLUSIVE (DROP TABLE, most ALTER TABLE); reads and writes continue",
    tone: "info",
  },
  "ROW SHARE": { words: "blocks EXCLUSIVE and ACCESS EXCLUSIVE; reads and writes continue", tone: "info" },
  "ROW EXCLUSIVE": {
    words: "blocks SHARE and stronger locks (CREATE INDEX, most ALTER TABLE); reads and writes continue",
    tone: "info",
  },
  "SHARE UPDATE EXCLUSIVE": {
    words: "blocks VACUUM and other schema changes; reads and writes continue",
    tone: "info",
  },
  SHARE: { words: "blocks writes; reads continue", tone: "warn" },
  "SHARE ROW EXCLUSIVE": { words: "blocks writes and other SHARE ROW EXCLUSIVE locks; reads continue", tone: "warn" },
  EXCLUSIVE: { words: "blocks writes and SELECT … FOR UPDATE; plain reads continue", tone: "warn" },
  "ACCESS EXCLUSIVE": { words: "blocks reads and writes", tone: "danger" },
  // MySQL and MariaDB: an online ALTER holds the metadata lock exclusively only at its start and end.
  "METADATA EXCLUSIVE": {
    words:
      "brief at the start and end, but it waits behind open transactions on the table, and every later query waits behind it",
    tone: "warn",
  },
  "SHARED (BLOCKS WRITES)": { words: "blocks writes for the whole operation; reads continue", tone: "danger" },
  // SQLite locks the whole database file, not a table.
  "DATABASE WRITE": {
    words:
      "blocks every other writer to the database; in WAL mode readers continue, in rollback-journal mode they wait while it commits",
    tone: "warn",
  },
};

export function lockModeWords(mode: string): string {
  return LOCK_MODES[mode.toUpperCase()]?.words ?? "a lock mode dbscope has no description for";
}

export function lockModeTone(mode: string): LockTone {
  return LOCK_MODES[mode.toUpperCase()]?.tone ?? "warn";
}

/** "ACCESS EXCLUSIVE — blocks reads and writes". */
export function lockSentence(mode: string): string {
  return `${mode} — ${lockModeWords(mode)}`;
}

const WORK: Record<StatementWork, string> = {
  none: "Catalog only: no scan or rewrite",
  scan: "Scans the whole table while it holds the lock",
  index_build: "Builds an index, reading the whole table",
  rewrite: "Rewrites the table and its indexes",
};

// Cassandra and ScyllaDB: schema changes propagate to every node; builds run in the background.
const CQL_WORK: Partial<Record<StatementWork, string>> = {
  none: "Schema only: propagates to every node, no data is read",
  index_build: "Builds in the background on every node, reading the whole table; reads and writes continue",
};

/** Engines whose schema changes take no table lock describe their work in their own terms. */
const ENGINE_WORK: Record<string, Partial<Record<StatementWork, string>>> = {
  // ClickHouse: a mutation over the table's parts.
  clickhouse: {
    none: "Metadata only: existing parts are not rewritten",
    rewrite: "A mutation rewrites parts in the background; reads and inserts continue",
  },
  cassandra: CQL_WORK,
  scylla: CQL_WORK,
};

// Elasticsearch and OpenSearch: mapping and setting changes apply at once; copies and rewrites run as tasks.
const SEARCH_WORK: Partial<Record<StatementWork, string>> = {
  none: "Cluster state only: no documents are read",
  rewrite: "Reads and indexes documents again as a task; searches and writes continue",
};
ENGINE_WORK.elasticsearch = SEARCH_WORK;
ENGINE_WORK.opensearch = SEARCH_WORK;

export function workWords(work: StatementWork, engine?: string): string {
  return (engine ? ENGINE_WORK[engine]?.[work] : undefined) ?? WORK[work] ?? work;
}

const TX: Record<StatementTx, string> = {
  none: "Autocommit: runs on its own, outside a transaction",
  implicit: "Implicit transaction: the file is sent as one multi-statement query",
  explicit: "Explicit transaction: inside BEGIN … COMMIT in the file",
  framework: "The migration tool's transaction, which wraps the whole file",
};

export function txWords(tx: StatementTx): string {
  return TX[tx] ?? tx;
}

const BYTE_UNITS = ["bytes", "KiB", "MiB", "GiB", "TiB"];

/** 164429824 → "157 MiB". */
export function formatBytes(bytes: number): string {
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 || value >= 100 ? 0 : value >= 10 ? 1 : 2;
  const text = value
    .toFixed(digits)
    .replace(/(\.\d*?)0+$/, "$1")
    .replace(/\.$/, "");
  return `${text} ${BYTE_UNITS[unit]}`;
}

export function sizeWords(sizeBytes: number | undefined, lockCount: number): string {
  if (sizeBytes !== undefined) return `${formatBytes(sizeBytes)} (largest locked relation)`;
  if (lockCount === 0) return "No relation is locked";
  return "Unknown: no size statistics, so it is scored as 1-10 GB";
}

export interface LockTimeoutCopy {
  tone: "success" | "warn" | "info";
  text: string;
}

export function lockTimeoutCopy(lockTimeout: boolean, lockCount: number): LockTimeoutCopy {
  if (lockTimeout) {
    return {
      tone: "success",
      text: "lock_timeout is in effect: if the lock is not granted in time, the statement fails instead of making later queries wait",
    };
  }
  if (lockCount === 0) return { tone: "info", text: "No lock_timeout, but this statement takes no table locks" };
  return {
    tone: "warn",
    text: "No lock_timeout: while it waits for its lock, later queries on the table queue behind it",
  };
}

const BASE: Record<MigrationBase, string> = {
  live: "the live schema",
  stats: "a stats export",
  dump: "a schema dump",
  sample: "the sample schema",
};

export function baseWords(base: MigrationBase | undefined): string | undefined {
  return base ? BASE[base] : undefined;
}
