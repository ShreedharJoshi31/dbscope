import {
  pgMajor,
  pgVersionLabel,
  RATINGS,
  sqliteVersionLabel,
  worstRating,
} from "@/entities/migration-finding/model/rating";
import type {
  AnalyzedStatement,
  AnalyzeReport,
  ColumnChangeKind,
  ImpactItem,
  MigrationDiff,
  Rating,
} from "@/shared/api";

/** A file as the user supplied it. `sql` is sent unchanged; the agent normalizes line endings itself. */
export interface MigrationSource {
  name: string;
  sql: string;
}

export interface ReviewStatement extends AnalyzedStatement {
  /** Position in report order (execution order after framework ordering). */
  index: number;
  fileIndex: number;
  /** Locked relations, first occurrence order. */
  relations: string[];
  /** The PostgreSQL 17 grammar could not parse it (DBS-PARSE). */
  unparseable: boolean;
}

export interface ReviewFile {
  name: string;
  /** Normalized text (BOM stripped, LF line ends) so report line numbers match. */
  text: string;
  /** Statement indices in this file, in order. */
  statements: number[];
  /** The text was rebuilt from the statements because the source file was not available. */
  synthesized: boolean;
}

export interface UnanalyzableView {
  file: string;
  line: number;
  message: string;
  /** Why, in plain words (e.g. PostgreSQL 18 syntax and the PostgreSQL 17 grammar). */
  explanation: string;
  statementIndex: number | null;
}

export interface ReviewModel {
  report: AnalyzeReport;
  pgVersion: number;
  pgLabel: string;
  rating: Rating;
  counts: Record<Rating, number>;
  findingCount: number;
  statements: ReviewStatement[];
  files: ReviewFile[];
  unanalyzable: UnanalyzableView[];
  warnings: string[];
  diff: MigrationDiff;
}

/** Strips a UTF-8 BOM and turns CRLF and lone CR into LF, as internal/migrate/ingest.Normalize does. */
export function normalizeSql(text: string): string {
  return text.replace(/^\uFEFF/, "").replace(/\r\n?/g, "\n");
}

function isSqlFile(name: string): boolean {
  return name.toLowerCase().endsWith(".sql");
}

/** Places each statement at its reported line when the original file text is missing. */
function synthesize(statements: readonly AnalyzedStatement[]): string {
  const lines: string[] = [];
  for (const s of statements) {
    while (lines.length < s.line - 1) lines.push("");
    const sqlLines = `${s.sql};`.split("\n");
    sqlLines.forEach((text, i) => {
      lines[s.line - 1 + i] = text;
    });
  }
  return lines.join("\n");
}

export function explainUnanalyzable(message: string, statement: AnalyzedStatement | undefined, pgVersion: number) {
  if (statement?.kind !== "unparseable") return message;
  const major = pgMajor(pgVersion);
  if (major >= 18) {
    return `PostgreSQL ${major} syntax cannot be analyzed with the PostgreSQL 17 grammar (${message}). Review this statement by hand.`;
  }
  return `The PostgreSQL 17 grammar cannot parse this statement, and the target is PostgreSQL ${major}, so the server will reject it too (${message}).`;
}

export function emptyCounts(): Record<Rating, number> {
  return { critical: 0, high: 0, medium: 0, low: 0 };
}

/** Maps the agent's report (plus the files sent) to what the review UI renders. */
export function toReviewModel(report: AnalyzeReport, sources: readonly MigrationSource[]): ReviewModel {
  const statementsIn = report.statements ?? [];
  const fileNames: string[] = [];
  const fileIndex = new Map<string, number>();
  for (const s of statementsIn) {
    if (!fileIndex.has(s.file)) {
      fileIndex.set(s.file, fileNames.length);
      fileNames.push(s.file);
    }
  }
  // Files without statements (empty, comments only, or skipped such as .down.sql) follow, in the order sent.
  for (const src of sources) {
    if (isSqlFile(src.name) && !fileIndex.has(src.name)) {
      fileIndex.set(src.name, fileNames.length);
      fileNames.push(src.name);
    }
  }

  const statements: ReviewStatement[] = statementsIn.map((s, index) => ({
    ...s,
    locks: s.locks ?? [],
    findings: s.findings ?? [],
    index,
    fileIndex: fileIndex.get(s.file) ?? 0,
    relations: [...new Set((s.locks ?? []).map((l) => l.relation))],
    unparseable: s.kind === "unparseable",
  }));

  const sourceByName = new Map(sources.map((s) => [s.name, s.sql]));
  const files: ReviewFile[] = fileNames.map((name) => {
    const own = statements.filter((s) => s.file === name);
    const sql = sourceByName.get(name);
    return {
      name,
      text: sql === undefined ? synthesize(own) : normalizeSql(sql),
      statements: own.map((s) => s.index),
      synthesized: sql === undefined,
    };
  });

  const counts = emptyCounts();
  let findingCount = 0;
  for (const s of statements) {
    counts[s.rating] = (counts[s.rating] ?? 0) + 1;
    findingCount += s.findings.length;
  }

  const unanalyzable = (report.unanalyzable ?? []).map((u) => {
    const statement = statements.find(
      (s) => s.file === u.file && s.unparseable && u.line >= s.line && u.line <= Math.max(s.endLine, s.line),
    );
    return {
      ...u,
      explanation: explainUnanalyzable(u.message, statement, report.pgVersion),
      statementIndex: statement?.index ?? null,
    };
  });

  return {
    report,
    pgVersion: report.pgVersion,
    pgLabel: report.engine === "sqlite" ? sqliteVersionLabel(report.pgVersion) : pgVersionLabel(report.pgVersion),
    rating: statements.length > 0 ? report.rating : worstRating([]),
    counts,
    findingCount,
    statements,
    files,
    unanalyzable,
    warnings: report.warnings ?? [],
    diff: {
      added: report.diff?.added ?? [],
      dropped: report.diff?.dropped ?? [],
      altered: report.diff?.altered ?? [],
      columns: report.diff?.columns ?? [],
    },
  };
}

/** The first statement with the report's worst rating, so the review opens on what matters most. */
export function initialSelection(model: ReviewModel): number {
  for (const rating of RATINGS) {
    const s = model.statements.find((st) => st.rating === rating);
    if (s) return s.index;
  }
  return 0;
}

/** Statement whose lines include `line` in `fileIndex`. */
export function statementAtLine(model: ReviewModel, fileIndex: number, line: number): number | null {
  const file = model.files[fileIndex];
  if (!file) return null;
  for (const i of file.statements) {
    const s = model.statements[i];
    if (s && line >= s.line && line <= Math.max(s.endLine, s.line)) return i;
  }
  return null;
}

// ---- canvas diff and impact ------------------------------------------------

export type RelationChange = "added" | "altered" | "dropped";

export interface ColumnDiffMark {
  change: ColumnChangeKind;
  to?: string | undefined;
}

export interface ImpactLink {
  /** The canvas node it lands on, when the dependent is a relation (fk child, view, a trigger's table). */
  nodeId: string | null;
  kind: ImpactItem["kind"];
  name: string;
  certainty: ImpactItem["certainty"];
  /** The changed relation or column it depends on: "shop.customers" or "shop.customers.email". */
  via: string;
  /** A query's calls, from pg_stat_statements. */
  calls?: number | undefined;
}

export function relationChanges(diff: MigrationDiff): Record<string, RelationChange> {
  const out: Record<string, RelationChange> = {};
  for (const key of diff.altered) out[key] = "altered";
  for (const key of diff.added) out[key] = "added";
  for (const key of diff.dropped) out[key] = out[key] === "added" ? "altered" : "dropped";
  return out;
}

export function columnChanges(diff: MigrationDiff): Record<string, Record<string, ColumnDiffMark>> {
  const out: Record<string, Record<string, ColumnDiffMark>> = {};
  for (const c of diff.columns) {
    const byColumn = out[c.relation] ?? {};
    byColumn[c.column] = c.to ? { change: c.change, to: c.to } : { change: c.change };
    out[c.relation] = byColumn;
  }
  return out;
}

/** The relation an impact item lives on, when it is one (docs/API.md Phase 4 name formats). */
export function impactNodeId(item: ImpactItem): string | null {
  switch (item.kind) {
    case "fk_child": {
      const i = item.name.indexOf(" (");
      return i === -1 ? item.name : item.name.slice(0, i);
    }
    case "view":
    case "matview":
      return item.name;
    case "trigger":
    case "policy": {
      const i = item.name.lastIndexOf(" ON ");
      return i === -1 ? null : item.name.slice(i + 4);
    }
    case "generated_column": {
      const i = item.name.lastIndexOf(".");
      return i === -1 ? null : item.name.slice(0, i);
    }
    default:
      return null;
  }
}

/** Impact grouped by changed relation: its own dependents plus those of its changed columns. */
export function impactByRelation(report: Pick<AnalyzeReport, "impact" | "diff">): Record<string, ImpactLink[]> {
  const columnKeys = new Map<string, string>();
  for (const c of report.diff?.columns ?? []) columnKeys.set(`${c.relation}.${c.column}`, c.relation);
  const out: Record<string, ImpactLink[]> = {};
  for (const [key, items] of Object.entries(report.impact ?? {})) {
    const relation = columnKeys.get(key) ?? key;
    const list = out[relation] ?? [];
    for (const item of items ?? []) {
      list.push({
        nodeId: impactNodeId(item),
        kind: item.kind,
        name: item.name,
        certainty: item.certainty,
        via: key,
        calls: item.calls,
      });
    }
    out[relation] = list;
  }
  return out;
}

/** The relation "Show on canvas" selects for a statement: a changed one it locks first, else its first lock. */
export function canvasRelationFor(
  statement: Pick<ReviewStatement, "relations">,
  changed: Readonly<Record<string, RelationChange>>,
  onCanvas: (key: string) => boolean,
): string | null {
  const visible = statement.relations.filter(onCanvas);
  return visible.find((r) => changed[r]) ?? visible[0] ?? null;
}
