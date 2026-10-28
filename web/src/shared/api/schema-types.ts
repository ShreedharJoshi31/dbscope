/**
 * Wire types for the Phase 1 schema API (docs/API.md "Phase 1").
 * The Model mirrors internal/schema/model.go field for field. Go marshals a
 * nil slice as null, so every slice without `omitempty` may arrive as null.
 */

export type RelKind =
  | "table"
  | "partitioned_table"
  | "view"
  | "materialized_view"
  | "foreign_table"
  | "collection"
  | "key_pattern"
  | "node_label"
  | "search_index";

export interface QualifiedName {
  schema: string;
  name: string;
}

export interface Column {
  name: string;
  /** format_type() output, e.g. "character varying(255)". */
  type: string;
  nullable: boolean;
  default?: string;
  identity?: "always" | "by_default";
  /** Expression of a generated column. */
  generated?: string;
  generatedKind?: "stored" | "virtual";
  comment?: string;
}

/** FK actions use the SQL words: "no action", "restrict", "cascade", "set null", "set default". */
export interface ForeignKey {
  name: string;
  columns: string[];
  ref: QualifiedName;
  refColumns: string[];
  onDelete?: string;
  onUpdate?: string;
  validated: boolean;
  /** Guessed from naming, not declared in the catalog. */
  inferred?: boolean;
  /** In (0,1]; accepted inferred keys come back with 1. */
  confidence?: number;
}

export interface Check {
  name: string;
  expr: string;
  validated: boolean;
}

export interface Index {
  name: string;
  /** Expression indexes appear as their expression text. */
  columns: string[];
  include?: string[];
  unique: boolean;
  primary: boolean;
  method: string;
  partial?: string;
  valid: boolean;
}

export interface Enum {
  schema: string;
  name: string;
  values: string[] | null;
}

export interface Relation {
  schema: string;
  name: string;
  kind: RelKind;
  comment?: string;
  /** pg_class.reltuples (summed over partitions); null when never analyzed. */
  rowEstimate: number | null;
  pages: number;
  columns: Column[] | null;
  primaryKey?: string[];
  foreignKeys?: ForeignKey[];
  uniques?: string[][];
  checks?: Check[];
  indexes?: Index[];
  /** pg_get_partkeydef for a partitioned table, e.g. "RANGE (at)". */
  partitionKey?: string;
  partitions?: QualifiedName[];
  /** Outside the selected schemas but referenced by a foreign key: only name, kind and primary key are filled in. */
  stub?: boolean;
  /** Relations a view or matview reads from. */
  dependsOn?: QualifiedName[];
}

export interface Model {
  /** server_version_num, e.g. 170002. */
  serverVersion: number;
  schemas: string[] | null;
  relations: Relation[] | null;
  enums: Enum[] | null;
  /** RFC 3339. */
  introspectedAt: string;
  durationMs: number;
}

export interface Namespace {
  name: string;
  relationCount: number;
}

export type InferredFkStatus = "accepted" | "rejected" | "unset";

export interface InferredFkDecision {
  /** `<schema>.<table>(<col>[,<col>])-><schema>.<table>(<col>[,<col>])`, the inferred key's `name`. */
  key: string;
  status: InferredFkStatus;
}

export interface Point {
  x: number;
  y: number;
}

export interface CanvasLayoutInput {
  key: string;
  positions: Record<string, Point>;
  collapsed: string[];
}

export interface CanvasLayout extends CanvasLayoutInput {
  updatedAt: string;
}
