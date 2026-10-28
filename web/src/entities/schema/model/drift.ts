import type { ColumnChangeKind, Model, Relation } from "@/shared/api";

/** How a relation differs here from the model it is compared with. */
export type DriftChange = "added" | "dropped" | "altered" | "not-visible";

export interface DriftItem {
  /** "schema.name". */
  relation: string;
  column?: string;
  change: DriftChange | ColumnChangeKind;
  /** What changed, such as "int → bigint" or "index by_status added". */
  detail: string;
}

export interface Drift {
  relations: Record<string, DriftChange>;
  columns: Record<string, Record<string, { change: ColumnChangeKind; detail: string }>>;
  items: DriftItem[];
}

const key = (r: Relation) => `${r.schema}.${r.name}`;
const list = (xs: readonly string[] | undefined) => (xs ?? []).join(", ");

/**
 * Compares this connection's model with another (an earlier snapshot, or
 * another connection). "added" is here and not there. A relation in a schema
 * the other side never read is "not-visible", not added or dropped: a user
 * with fewer grants or other selected schemas sees less, and that is not drift.
 * Inferred foreign keys, row counts and comments are not compared.
 */
export function diffModels(here: Model, there: Model): Drift {
  const drift: Drift = { relations: {}, columns: {}, items: [] };
  const ours = new Map((here.relations ?? []).filter((r) => !r.stub).map((r): [string, Relation] => [key(r), r]));
  const theirs = new Map((there.relations ?? []).filter((r) => !r.stub).map((r): [string, Relation] => [key(r), r]));
  const ourSchemas = new Set(here.schemas ?? []);
  const theirSchemas = new Set(there.schemas ?? []);
  const note = (relation: string, change: DriftChange, detail: string) => {
    drift.relations[relation] = change;
    drift.items.push({ relation, change, detail });
  };

  for (const [k, r] of ours) {
    const other = theirs.get(k);
    if (!other) {
      if (theirSchemas.size > 0 && !theirSchemas.has(r.schema))
        note(k, "not-visible", `schema ${r.schema} was not read there`);
      else note(k, "added", `${r.kind.replace("_", " ")} added`);
      continue;
    }
    const before = drift.items.length;
    compareRelation(drift, k, other, r);
    if (drift.items.length > before) drift.relations[k] = "altered";
  }
  for (const [k, r] of theirs) {
    if (ours.has(k)) continue;
    if (ourSchemas.size > 0 && !ourSchemas.has(r.schema)) note(k, "not-visible", `schema ${r.schema} is not read here`);
    else note(k, "dropped", `${r.kind.replace("_", " ")} missing here`);
  }
  drift.items.sort((a, b) => a.relation.localeCompare(b.relation) || (a.column ?? "").localeCompare(b.column ?? ""));
  return drift;
}

function compareRelation(drift: Drift, k: string, was: Relation, now: Relation): void {
  const item = (detail: string, column?: string, change: DriftItem["change"] = "altered") =>
    drift.items.push({ relation: k, change, detail, ...(column ? { column } : {}) });
  const column = (name: string, change: ColumnChangeKind, detail: string) => {
    drift.columns[k] ??= {};
    (drift.columns[k] as Record<string, { change: ColumnChangeKind; detail: string }>)[name] = { change, detail };
    item(detail, name, change);
  };
  if (was.kind !== now.kind) item(`kind ${was.kind} → ${now.kind}`);
  const before = new Map((was.columns ?? []).map((c) => [c.name, c]));
  const after = new Map((now.columns ?? []).map((c) => [c.name, c]));
  for (const [name, c] of after) {
    const old = before.get(name);
    if (!old) {
      column(name, "added", `column added (${c.type})`);
      continue;
    }
    const changes: string[] = [];
    if (old.type !== c.type) changes.push(`${old.type} → ${c.type}`);
    if (old.nullable !== c.nullable) changes.push(c.nullable ? "now nullable" : "now NOT NULL");
    if ((old.default ?? "") !== (c.default ?? ""))
      changes.push(`default ${old.default || "none"} → ${c.default || "none"}`);
    if (changes.length > 0) column(name, "altered", changes.join(", "));
  }
  for (const [name, c] of before) if (!after.has(name)) column(name, "dropped", `column missing here (was ${c.type})`);

  if (list(was.primaryKey) !== list(now.primaryKey))
    item(`primary key (${list(was.primaryKey)}) → (${list(now.primaryKey)})`);
  if ((was.partitionKey ?? "") !== (now.partitionKey ?? ""))
    item(`partition key ${was.partitionKey || "none"} → ${now.partitionKey || "none"}`);
  const indexes = (r: Relation) =>
    new Map((r.indexes ?? []).map((i) => [i.name, `${i.unique ? "unique " : ""}${i.method} (${list(i.columns)})`]));
  const ib = indexes(was);
  const ia = indexes(now);
  for (const [name, def] of ia) {
    if (!ib.has(name)) item(`index ${name} added: ${def}`);
    else if (ib.get(name) !== def) item(`index ${name} changed: ${ib.get(name)} → ${def}`);
  }
  for (const [name] of ib) if (!ia.has(name)) item(`index ${name} missing here`);
  const fks = (r: Relation) => new Set((r.foreignKeys ?? []).filter((f) => !f.inferred).map((f) => f.name));
  const fb = fks(was);
  const fa = fks(now);
  for (const name of fa) if (!fb.has(name)) item(`foreign key ${name} added`);
  for (const name of fb) if (!fa.has(name)) item(`foreign key ${name} missing here`);
  const findings = (r: Relation) =>
    new Map((r.findings ?? []).map((f) => [`${f.ruleId}|${f.column ?? ""}`, f.message]));
  const xb = findings(was);
  const xa = findings(now);
  for (const [id, message] of xa) if (!xb.has(id)) item(`new risk: ${message}`);
  for (const [id, message] of xb) if (!xa.has(id)) item(`risk gone: ${message}`);
}
