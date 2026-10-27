import type { Model } from "@/shared/api";
import raw from "@/test/fixtures/sample-model.json";

/** A fresh deep copy of the hand-written sample Model (42 relations, 55 FKs, 2 inferred). */
export function sampleModel(): Model {
  return structuredClone(raw) as Model;
}
