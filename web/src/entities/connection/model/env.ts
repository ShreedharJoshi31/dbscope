import { FlaskConical, Laptop, type LucideIcon, ShieldAlert } from "lucide-react";
import type { Env } from "@/shared/api";

export interface EnvMeta {
  label: string;
  description: string;
  icon: LucideIcon;
  /** Tailwind text/border classes built only from env tokens. */
  tone: string;
}

export const ENV_META: Record<Env, EnvMeta> = {
  local: {
    label: "local",
    description: "A database on this machine or a disposable dev container.",
    icon: Laptop,
    tone: "text-env-local-fg border-env-local-fg",
  },
  staging: {
    label: "staging",
    description: "A shared, non-production environment.",
    icon: FlaskConical,
    tone: "text-env-staging-fg border-env-staging-fg",
  },
  "prod-replica": {
    label: "prod-replica",
    description: "A read replica of production. dbscope stays read-only and shows a warning banner.",
    icon: ShieldAlert,
    tone: "text-env-prod-fg border-env-prod-fg",
  },
};

export const ENV_ORDER: readonly Env[] = ["local", "staging", "prod-replica"];

const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "::1", "[::1]"]);

export function isLoopbackHost(host: string): boolean {
  return LOOPBACK_HOSTS.has(host.trim().toLowerCase());
}

export function defaultStatementTimeoutMs(env: Env | undefined): number {
  return env === "prod-replica" ? 10_000 : 30_000;
}
