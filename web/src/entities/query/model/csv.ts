import type { CellValue } from "@/shared/api";

/**
 * CSV per RFC 4180 (CRLF rows, fields with a comma, quote, CR or LF quoted,
 * quotes doubled), with two additions:
 *
 * - NULL vs empty string follows PostgreSQL's `COPY ... CSV`: NULL is an
 *   empty unquoted field, an empty string is `""`.
 * - Formula injection (OWASP "CSV Injection"): a text value starting with
 *   `= + - @`, tab or CR is prefixed with `'` so spreadsheets show it as text.
 *   Plain numbers such as `-12.5` are left alone; they cannot be formulas.
 */

const FORMULA_START = /^[=+\-@\t\r]/;
const PLAIN_NUMBER = /^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$/;
const NEEDS_QUOTES = /[",\r\n]|^\s|\s$/;

export function escapeFormula(text: string): string {
  return FORMULA_START.test(text) && !PLAIN_NUMBER.test(text) ? `'${text}` : text;
}

function quote(text: string): string {
  return `"${text.replaceAll('"', '""')}"`;
}

/** One CSV field. Exported for tests. */
export function csvField(value: CellValue): string {
  if (value === null) return "";
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  const text = escapeFormula(typeof value === "string" ? value : JSON.stringify(value));
  return text === "" || NEEDS_QUOTES.test(text) ? quote(text) : text;
}

export function toCsv(columns: readonly { name: string }[], rows: readonly (readonly CellValue[])[]): string {
  const lines = [columns.map((c) => csvField(c.name)).join(",")];
  for (const row of rows) lines.push(row.map(csvField).join(","));
  return `${lines.join("\r\n")}\r\n`;
}

/** A file name such as "orders-dev-2026-10-27T10-15-00.csv". */
export function csvFileName(prefix: string, at = new Date()): string {
  const stamp = at.toISOString().slice(0, 19).replaceAll(":", "-");
  const safe = prefix.replace(/[^\w.-]+/g, "-").replace(/^-+|-+$/g, "") || "result";
  return `${safe}-${stamp}.csv`;
}
