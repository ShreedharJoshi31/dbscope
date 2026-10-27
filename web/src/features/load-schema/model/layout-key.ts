import type { Model } from "@/shared/api";

/**
 * The canvas layout key the agent stores positions under: the introspected
 * schemas, sorted and joined by ",". Choosing different schemas gives a
 * different saved layout; the same set always finds its own.
 */
export function layoutKeyFor(model: Model): string {
  return [...(model.schemas ?? [])].sort().join(",");
}
