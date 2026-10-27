import type { CellValue } from "@/shared/api";

export type CellKind = "null" | "number" | "boolean" | "json" | "text";

/** format_type() names whose text values are numbers (int8 beyond 2^53 and numeric arrive as strings). */
const NUMERIC_TYPE =
  /^(smallint|integer|bigint|int2|int4|int8|oid|real|double precision|float4|float8|numeric|decimal|money)(\(|$)/i;

export function isNumericType(type: string | undefined): boolean {
  return Boolean(type && NUMERIC_TYPE.test(type.trim()));
}

/** How a value renders: NULL is its own kind, never confused with an empty string (UX rule 7). */
export function cellKind(value: CellValue, type?: string): CellKind {
  if (value === null) return "null";
  if (typeof value === "number") return "number";
  if (typeof value === "boolean") return "boolean";
  if (typeof value === "object") return "json";
  return isNumericType(type) ? "number" : "text";
}

/** Text for display and copying. NULL becomes "NULL"; JSON values are compact JSON. */
export function formatCell(value: CellValue): string {
  if (value === null) return "NULL";
  if (typeof value === "string") return value;
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

/** One row as tab-separated text, for "Copy row". */
export function formatRowTsv(row: readonly CellValue[]): string {
  return row.map((v) => formatCell(v).replace(/[\t\r\n]+/g, " ")).join("\t");
}
