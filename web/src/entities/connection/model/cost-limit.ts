import type { Env } from "@/shared/api";

/** EXPLAIN cost limits per environment when a profile sets no maxCost (internal/conn/profile.go). 0 = none. */
export const DEFAULT_MAX_COST: Record<Env, number> = { local: 0, staging: 1e7, "prod-replica": 1e6 };

/** The cost above which a query needs confirming for this profile; 0 means no limit. */
export function costLimit(profile: { env: Env; maxCost?: number }): number {
  return profile.maxCost ?? DEFAULT_MAX_COST[profile.env];
}
