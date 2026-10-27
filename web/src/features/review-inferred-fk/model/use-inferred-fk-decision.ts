import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useActiveConnection } from "@/entities/connection";
import { type FkEdge, relationKey, useWorkspaceStore } from "@/entities/schema";
import { applyDecision, restoreForeignKey } from "@/features/review-inferred-fk/model/apply-decision";
import { type InferredFkStatus, type Model, putInferredFkDecision, schemaQueryKeys } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";

export type ReviewStatus = Exclude<InferredFkStatus, "unset">;

function fkLabel(edge: FkEdge): string {
  return edge.fk.name || `${edge.child} → ${edge.parent}`;
}

/**
 * Accepts or rejects an inferred foreign key: updates the cached schema at
 * once (rejected keys disappear, accepted ones turn solid), then saves the
 * decision with PUT /inferred-fks. A failed save puts the key back and says so.
 * In the sample workspace the decision stays in this tab.
 */
export function useInferredFkDecision() {
  const client = useQueryClient();
  const source = useWorkspaceStore((s) => s.source);
  const { profile } = useActiveConnection();
  const [pending, setPending] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const decide = async (edge: FkEdge, status: ReviewStatus): Promise<boolean> => {
    const queryKey = source === "sample" ? schemaQueryKeys.sample : schemaQueryKeys.schema(profile?.id ?? "");
    const before = client.getQueryData<Model>(queryKey);
    if (!before) return false;
    const childRelation = (before.relations ?? []).find((r) => relationKey(r) === edge.child);
    const index = childRelation?.foreignKeys?.findIndex((fk) => fk.name === edge.fk.name) ?? -1;

    setError(null);
    client.setQueryData<Model>(queryKey, (m) => (m ? applyDecision(m, edge.child, edge.fk.name, status) : m));
    const verb = status === "accepted" ? "Accepted" : "Rejected";

    if (source === "sample" || !profile) {
      announce(`${verb} the inferred key ${fkLabel(edge)}. Sample schema: the decision is kept only in this tab.`);
      return true;
    }

    setPending(edge.id);
    try {
      await putInferredFkDecision(profile.id, { key: edge.fk.name, status });
      announce(`${verb} the inferred key ${fkLabel(edge)}.`);
      return true;
    } catch (cause) {
      client.setQueryData<Model>(queryKey, (m) => (m ? restoreForeignKey(m, edge.child, edge.fk, index) : m));
      const message = cause instanceof Error ? cause.message : "unknown error";
      setError(`Could not save the decision: ${message}`);
      announce(`Could not save the decision on ${fkLabel(edge)}: ${message}`);
      return false;
    } finally {
      setPending(null);
    }
  };

  return { decide, pending, error };
}
