import type { Completion } from "@codemirror/autocomplete";
import type { SQLNamespace } from "@codemirror/lang-sql";
import type { Model } from "@/shared/api";

export interface SchemaCompletion {
  namespace: SQLNamespace;
  /** Tables of this schema complete without a prefix (the executor's search_path puts it first). */
  defaultSchema: string | undefined;
}

const KIND_LABEL: Record<string, string> = {
  table: "table",
  partitioned_table: "partitioned table",
  view: "view",
  materialized_view: "materialized view",
  foreign_table: "foreign table",
};

/**
 * Builds @codemirror/lang-sql's schema namespace from the loaded Model:
 * schema → table → columns, with types as details. Type-only import, so this
 * module adds nothing to the shell bundle.
 */
export function schemaCompletion(model: Model | undefined): SchemaCompletion {
  if (!model) return { namespace: {}, defaultSchema: undefined };
  const namespace: Record<string, Record<string, SQLNamespace>> = {};
  for (const r of model.relations ?? []) {
    if (r.stub) continue;
    const columns: Completion[] = (r.columns ?? []).map((c) => ({
      label: c.name,
      type: "property",
      detail: c.type,
      ...(c.comment ? { info: c.comment } : {}),
    }));
    const self: Completion = {
      label: r.name,
      type: r.kind === "view" || r.kind === "materialized_view" ? "class" : "type",
      detail: KIND_LABEL[r.kind] ?? r.kind,
      ...(r.comment ? { info: r.comment } : {}),
    };
    namespace[r.schema] ??= {};
    (namespace[r.schema] as Record<string, SQLNamespace>)[r.name] = { self, children: columns };
  }
  const schemas = Object.keys(namespace).sort();
  const defaultSchema = schemas.includes("public") ? "public" : schemas[0];
  return { namespace, defaultSchema };
}
