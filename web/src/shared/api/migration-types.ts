/**
 * Wire types for the Phase 4 migration API (docs/API.md "Phase 4"). They
 * mirror internal/migrate.Report, apply.Diff and impact.Item.
 */

export type Rating = "low" | "medium" | "high" | "critical";
export type TxMode = "auto" | "file" | "statement" | "none";

export interface AnalyzeFile {
  /** As given: a file name or a relative path (Prisma needs `migrations/<dir>/migration.sql`). */
  name: string;
  sql: string;
}

export interface AnalyzeRequest {
  /** 1..200 files, each at most 1 MiB; analyzed in this order after framework ordering. */
  files: AnalyzeFile[];
  txMode?: TxMode;
  /** The deploy tool sets lock_timeout itself. */
  assumeLockTimeout?: boolean;
}

export type StatementTx = "none" | "implicit" | "explicit" | "framework";
export type StatementWork = "none" | "scan" | "index_build" | "rewrite";

export interface StatementLock {
  /** "schema.name". */
  relation: string;
  /** PostgreSQL lock mode name, e.g. "ACCESS EXCLUSIVE". */
  mode: string;
}

export interface MigrationFinding {
  ruleId: string;
  rating: Rating;
  message: string;
  safeAlternative?: string;
}

export interface AnalyzedStatement {
  file: string;
  /** 1-based, in the file as sent (after CRLF normalization). */
  line: number;
  column: number;
  endLine: number;
  sql: string;
  /** e.g. "ALTER TABLE ADD COLUMN"; "unparseable" when the PostgreSQL 17 grammar rejected it. */
  kind: string;
  tx: StatementTx;
  locks: StatementLock[];
  work: StatementWork;
  /** Largest locked relation, when known. */
  sizeBytes?: number;
  lockTimeout: boolean;
  score: number;
  rating: Rating;
  findings: MigrationFinding[];
}

export type ColumnChangeKind = "added" | "dropped" | "altered" | "renamed";

export interface ColumnChange {
  relation: string;
  column: string;
  change: ColumnChangeKind;
  /** The new name, for renames. */
  to?: string;
}

export interface MigrationDiff {
  added: string[];
  dropped: string[];
  altered: string[];
  columns: ColumnChange[];
}

export type ImpactKind =
  | "fk_child"
  | "view"
  | "matview"
  | "function"
  | "trigger"
  | "policy"
  | "index"
  | "generated_column"
  | "query";

export interface ImpactItem {
  kind: ImpactKind;
  /** fk_child: "s.t (constraint)"; trigger/policy: "name ON s.t"; function: "s.f(args)"; query: a summary. */
  name: string;
  certainty: "catalog" | "heuristic";
  /** kind query: how often it ran, from pg_stat_statements. */
  calls?: number;
}

export interface Unanalyzable {
  file: string;
  line: number;
  message: string;
}

export type MigrationBase = "live" | "stats" | "dump" | "sample";

export interface AnalyzeReport {
  /** Omitted for PostgreSQL. */
  engine?: "sqlite" | "mysql" | "mariadb" | "clickhouse" | "cassandra" | "scylla";
  /** server_version_num used for version-dependent rules, e.g. 170000; SQLITE_VERSION_NUMBER for SQLite. */
  pgVersion: number;
  rating: Rating;
  statements: AnalyzedStatement[];
  diff: MigrationDiff;
  /** Keyed by "schema.table" or "schema.table.column". */
  impact: Record<string, ImpactItem[]>;
  unanalyzable: Unanalyzable[];
  /** Notes about the analysis itself; additive to the contract, may be null. */
  warnings?: string[] | null;
  /** Where the base schema came from; additive to the contract. */
  base?: MigrationBase;
}
