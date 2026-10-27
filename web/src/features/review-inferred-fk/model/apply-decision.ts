import { relationKey } from "@/entities/schema";
import type { ForeignKey, InferredFkStatus, Model, Relation } from "@/shared/api";

/** Replaces one relation, sharing every other relation object so unaffected nodes keep their identity. */
function withRelation(model: Model, key: string, update: (r: Relation) => Relation): Model {
  const relations = model.relations ?? [];
  const index = relations.findIndex((r) => relationKey(r) === key);
  const current = relations[index];
  if (!current) return model;
  const next = relations.slice();
  next[index] = update(current);
  return { ...model, relations: next };
}

/**
 * What the agent will return for this decision on the next introspection:
 * rejected inferred keys are omitted, accepted ones come back with
 * confidence 1. Applied locally so the canvas updates without re-reading the
 * catalog. "unset" needs the original confidence, so callers refetch instead.
 */
export function applyDecision(model: Model, child: string, fkName: string, status: InferredFkStatus): Model {
  if (status === "unset") return model;
  return withRelation(model, child, (r) => {
    const fks = r.foreignKeys ?? [];
    if (status === "rejected") return { ...r, foreignKeys: fks.filter((fk) => fk.name !== fkName) };
    return { ...r, foreignKeys: fks.map((fk) => (fk.name === fkName ? { ...fk, confidence: 1 } : fk)) };
  });
}

/** Puts the original key back after a failed save. */
export function restoreForeignKey(model: Model, child: string, original: ForeignKey, index: number): Model {
  return withRelation(model, child, (r) => {
    const fks = (r.foreignKeys ?? []).filter((fk) => fk.name !== original.name);
    fks.splice(Math.min(index, fks.length), 0, original);
    return { ...r, foreignKeys: fks };
  });
}
