import { engineLabel } from "@/entities/connection/model/engine";
import type { Env, TestErrorKind, TestResult } from "@/shared/api";
import type { StatusTone } from "@/shared/ui/status-icon";

export interface ChecklistItem {
  id: string;
  tone: StatusTone;
  text: string;
  details?: string[];
}

export const ERROR_KIND_COPY: Record<TestErrorKind, { title: string; hint: string }> = {
  auth: {
    title: "Authentication failed",
    hint: "Check the user name and password. The server's pg_hba.conf must also allow this user from this machine.",
  },
  network: {
    title: "Could not reach the server",
    hint: "Check the host and port, any VPN or SSH tunnel, and that PostgreSQL accepts TCP connections (listen_addresses).",
  },
  tls: {
    title: "TLS handshake failed",
    hint: "Try a different SSL mode. For verify-ca or verify-full, set the root certificate path under Advanced.",
  },
  timeout: {
    title: "Connection timed out",
    hint: "The server did not answer within 10 seconds. A firewall or security group may be dropping traffic to this port.",
  },
  database: {
    title: "Database not available",
    hint: "Check that the database exists and that you are allowed to open it.",
  },
  other: {
    title: "Connection failed",
    hint: "Read the server message above for the cause.",
  },
};

/** Turns a successful TestResult into an ordered, human-readable checklist. */
export function buildChecklist(result: TestResult, env: Env | undefined): ChecklistItem[] {
  const items: ChecklistItem[] = [
    {
      id: "connected",
      tone: "success",
      text: result.latencyMs === undefined ? "Connected" : `Connected in ${result.latencyMs} ms`,
    },
  ];

  if (result.serverVersion) {
    items.push({
      id: "version",
      tone: "info",
      text:
        result.engine === "sqlite"
          ? `SQLite library: ${result.serverVersion} (dbscope's own; your application may link another version)`
          : `Server version: ${engineLabel(result.engine)} ${result.serverVersion}`,
    });
  }

  if (result.isReplica !== undefined) {
    items.push(
      result.isReplica
        ? { id: "replica", tone: "success", text: "Replica: yes, the server is in recovery" }
        : {
            id: "replica",
            tone: env === "prod-replica" ? "warn" : "info",
            text: "Replica: no, this is a primary server",
          },
    );
  }

  if (result.defaultReadOnly !== undefined) {
    items.push(
      result.defaultReadOnly
        ? { id: "read-only", tone: "success", text: "Read-only by default: yes" }
        : { id: "read-only", tone: "info", text: "Read-only by default: no (default_transaction_read_only is off)" },
    );
  }

  if (result.writePrivileges !== undefined) {
    const writes = result.writePrivileges;
    if (writes.length === 0) {
      items.push({ id: "privileges", tone: "success", text: "Write privileges: none, this login can only read" });
    } else if (env === "prod-replica") {
      items.push({
        id: "privileges",
        tone: "danger",
        text: `Write privileges: this login can change data (${writes.length}). dbscope stays read-only on prod-replica, but use a SELECT-only role for production.`,
        details: writes,
      });
    } else {
      items.push({
        id: "privileges",
        tone: "warn",
        text: `Write privileges: this login can change data (${writes.length})`,
        details: writes,
      });
    }
  }

  if (result.pgStatStatements !== undefined) {
    items.push(
      result.pgStatStatements
        ? { id: "pgss", tone: "success", text: "pg_stat_statements: installed and readable" }
        : {
            id: "pgss",
            tone: "info",
            text: "pg_stat_statements: not available, so query statistics will be missing",
          },
    );
  }

  // The server warns only about facts no row above states (the replica and
  // privilege rows come from structured fields).
  result.warnings.forEach((warning, index) => {
    items.push({ id: `warning-${index}`, tone: "warn", text: warning });
  });

  return items;
}

export function summarizeTest(result: TestResult): string {
  if (!result.ok) return `Connection test failed: ${ERROR_KIND_COPY[result.error?.kind ?? "other"].title}.`;
  const warnings = result.warnings.length + (result.writePrivileges?.length ? 1 : 0);
  return warnings > 0
    ? `Connection test passed with ${warnings} warning${warnings === 1 ? "" : "s"}.`
    : "Connection test passed.";
}
