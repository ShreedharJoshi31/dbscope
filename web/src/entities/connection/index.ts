export { resolveActiveProfile, useActiveConnectionStore } from "@/entities/connection/model/active-connection";
export { costLimit, DEFAULT_MAX_COST } from "@/entities/connection/model/cost-limit";
export {
  defaultStatementTimeoutMs,
  ENV_META,
  ENV_ORDER,
  type EnvMeta,
  isLoopbackHost,
} from "@/entities/connection/model/env";
export {
  buildChecklist,
  type ChecklistItem,
  ERROR_KIND_COPY,
  summarizeTest,
} from "@/entities/connection/model/test-result";
export { useActiveConnection } from "@/entities/connection/model/use-active-connection";
export { EnvBadge } from "@/entities/connection/ui/env-badge";
export { TestResultChecklist } from "@/entities/connection/ui/test-result-checklist";
