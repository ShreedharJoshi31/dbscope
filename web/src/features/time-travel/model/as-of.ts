import type { Model, TimeTravelBoundSource, TimeTravelBounds, TimeTravelKind } from "@/shared/api";

/** The IANA zone the browser shows times in, such as "Asia/Kolkata". */
export function localTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

interface WallTime {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
  second: number;
}

const formatters = new Map<string, Intl.DateTimeFormat>();

function formatterFor(tz: string): Intl.DateTimeFormat {
  let f = formatters.get(tz);
  if (!f) {
    f = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
    formatters.set(tz, f);
  }
  return f;
}

function wallTime(ms: number, tz: string): WallTime {
  const parts: Record<string, number> = {};
  for (const p of formatterFor(tz).formatToParts(new Date(ms))) {
    if (p.type !== "literal") parts[p.type] = Number(p.value);
  }
  return {
    year: parts.year ?? 0,
    month: parts.month ?? 1,
    day: parts.day ?? 1,
    hour: (parts.hour ?? 0) % 24,
    minute: parts.minute ?? 0,
    second: parts.second ?? 0,
  };
}

const wallMs = (w: WallTime) => Date.UTC(w.year, w.month - 1, w.day, w.hour, w.minute, w.second);

/** How far the zone's wall clock is ahead of UTC at an instant, in ms. */
function offsetAt(ms: number, tz: string): number {
  return wallMs(wallTime(ms, tz)) - Math.floor(ms / 1000) * 1000;
}

const LOCAL_VALUE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/;

/**
 * A datetime-local value ("2026-11-15T10:00" or "...T10:00:30"), read as wall
 * time in `tz`, as an RFC 3339 UTC instant ending in Z. Null when the value is
 * not a real date and time or the zone is unknown.
 */
export function toAsOf(local: string, tz: string): string | null {
  const m = LOCAL_VALUE.exec(local.trim());
  if (!m) return null;
  const [year, month, day, hour, minute, second] = m.slice(1).map((v) => Number(v ?? 0)) as [
    number,
    number,
    number,
    number,
    number,
    number,
  ];
  const wall = Date.UTC(year, month - 1, day, hour, minute, second);
  const check = new Date(wall);
  if (check.getUTCMonth() !== month - 1 || check.getUTCDate() !== day || hour > 23 || minute > 59 || second > 59) {
    return null;
  }
  try {
    const first = offsetAt(wall, tz);
    let instant = wall - first;
    // Near a DST change the offset at the guess can differ from the first one.
    const corrected = offsetAt(instant, tz);
    if (corrected !== first) instant = wall - corrected;
    return new Date(instant).toISOString().replace(/\.000Z$/, "Z");
  } catch {
    return null;
  }
}

const pad = (n: number, width = 2) => String(n).padStart(width, "0");

/**
 * An RFC 3339 instant as a datetime-local value in `tz`, to whole seconds.
 * "ceil" rounds a fractional second up, for a lower bound the input must not undercut.
 */
export function fromAsOf(asOf: string, tz: string, round: "floor" | "ceil" = "floor"): string {
  const ms = Date.parse(asOf);
  if (Number.isNaN(ms)) return "";
  const whole = round === "ceil" ? Math.ceil(ms / 1000) * 1000 : Math.floor(ms / 1000) * 1000;
  const w = wallTime(whole, tz);
  return `${pad(w.year, 4)}-${pad(w.month)}-${pad(w.day)}T${pad(w.hour)}:${pad(w.minute)}:${pad(w.second)}`;
}

/** "2026-11-15 10:00:00 GMT+5:30": the instant as the user's wall clock shows it, with the zone. */
export function formatAsOf(asOf: string, tz: string): string {
  const local = fromAsOf(asOf, tz);
  if (!local) return asOf;
  let zone = "";
  try {
    zone =
      new Intl.DateTimeFormat("en-US", { timeZone: tz, timeZoneName: "shortOffset" })
        .formatToParts(new Date(Date.parse(asOf)))
        .find((p) => p.type === "timeZoneName")?.value ?? "";
  } catch {
    zone = "";
  }
  return `${local.replace("T", " ")}${zone ? ` ${zone}` : ""}`;
}

/** At most this many tables are asked about when the SQL names none of the versioned ones. */
export const MAX_PICKED_TABLES = 50;

const IDENT_CHAR = "[\\p{L}\\p{N}_$#@]";

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** True when `name` appears in the text as a whole identifier, bare, quoted or after a schema. */
function mentions(sql: string, name: string): boolean {
  return new RegExp(`(?<!${IDENT_CHAR})${escapeRegExp(name)}(?!${IDENT_CHAR})`, "iu").test(sql);
}

export interface PickedTables {
  /** "schema.name" as the model spells them. */
  tables: string[];
  /** True when the tables come from the SQL text; false when none matched and these are the model's versioned tables. */
  matched: boolean;
}

/**
 * The versioned relations (relation.timeTravel set) whose name the SQL text
 * mentions, case-insensitively. This is a text match for showing bounds only;
 * which tables a query really reads is the agent's decision.
 */
export function pickTables(sql: string, model: Model | undefined): PickedTables {
  const versioned = (model?.relations ?? []).filter((r) => r.timeTravel);
  const named = versioned.filter((r) => mentions(sql, r.name));
  const chosen = named.length > 0 ? named : versioned;
  return {
    tables: chosen.slice(0, MAX_PICKED_TABLES).map((r) => `${r.schema}.${r.name}`),
    matched: named.length > 0,
  };
}

export interface AsOfTableRow {
  table: string;
  kind: TimeTravelKind | null;
  available: boolean;
  /** RFC 3339, or null when there is no lower limit or the table cannot be read as of a time. */
  oldest: string | null;
  source: TimeTravelBoundSource;
  note?: string;
}

export interface AsOfRange {
  /** The latest oldest readable time over the available tables; null when none has a lower limit. */
  min: string | null;
  /** The database server's clock when the bounds were read. */
  max: string;
  rows: AsOfTableRow[];
}

/** The range every picked table can be read in, and one row per picked table the bounds describe. */
export function clamp(bounds: TimeTravelBounds, tables: readonly string[]): AsOfRange {
  const byName = new Map(bounds.tables.map((t) => [t.table, t]));
  const rows: AsOfTableRow[] = [];
  for (const name of tables) {
    const t = byName.get(name);
    if (!t) continue;
    rows.push({
      table: t.table,
      kind: t.kind,
      available: t.available,
      oldest: t.oldest,
      source: t.source,
      ...(t.note ? { note: t.note } : {}),
    });
  }
  let min: string | null = null;
  for (const r of rows) {
    if (!r.available || !r.oldest) continue;
    if (min === null || Date.parse(r.oldest) > Date.parse(min)) min = r.oldest;
  }
  return { min, max: bounds.now, rows };
}

/** Why an instant cannot be applied: before the oldest readable time or after the server's clock. */
export function outOfRange(asOf: string, range: AsOfRange | null): "before" | "after" | null {
  if (!range) return null;
  const at = Date.parse(asOf);
  if (range.min && at < Date.parse(range.min)) return "before";
  if (at > Date.parse(range.max)) return "after";
  return null;
}

/** "15 minutes", "3 hours", "2 days": a rounded span for estimates. */
export function formatSpan(ms: number): string {
  const minutes = Math.max(0, Math.round(ms / 60_000));
  const unit = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
  if (minutes < 90) return unit(minutes, "minute");
  const hours = Math.round(minutes / 60);
  if (hours < 48) return unit(hours, "hour");
  return unit(Math.round(hours / 24), "day");
}

/** The "oldest readable time" cell of a table row. An assumed undo retention reads as an estimate. */
export function oldestLabel(row: AsOfTableRow, now: string, tz: string): string {
  if (!row.available) return "Not readable";
  if (!row.oldest) return "No lower limit";
  if (row.source === "assumed_default") {
    return `about ${formatSpan(Date.parse(now) - Date.parse(row.oldest))}, assumed: undo retention could not be read`;
  }
  return formatAsOf(row.oldest, tz);
}
