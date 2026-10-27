import type { AnalyzeReport, Model } from "@/shared/api";
import report from "@/test/fixtures/migration-report.json";
import report18 from "@/test/fixtures/migration-report-pg18.json";
import orders from "@/test/fixtures/migrations/0042_orders.sql?raw";
import cleanup from "@/test/fixtures/migrations/0043_cleanup.sql?raw";
import virtual from "@/test/fixtures/migrations/V7__virtual.sql?raw";
import raw from "@/test/fixtures/sample-model.json";

/** A fresh deep copy of the hand-written sample Model (42 relations, 55 FKs, 2 inferred). */
export function sampleModel(): Model {
  return structuredClone(raw) as Model;
}

/**
 * A real POST /api/sample/migrations/analyze response for 0042_orders.sql and
 * 0043_cleanup.sql (8 statements: 2 critical, 2 high, 3 medium, 1 low; one
 * DBS-PARSE), recorded from the agent on 2026-10-27.
 */
export function migrationReport(): AnalyzeReport {
  return structuredClone(report) as AnalyzeReport;
}

export function migrationSources(): { name: string; sql: string }[] {
  return [
    { name: "0042_orders.sql", sql: orders },
    { name: "0043_cleanup.sql", sql: cleanup },
  ];
}

/** `dbscope migrate check --pg-version 18` on V7__virtual.sql: a PostgreSQL 18 VIRTUAL column the 17 grammar rejects. */
export function migrationReportPg18(): AnalyzeReport {
  return structuredClone(report18) as AnalyzeReport;
}

export function migrationSourcesPg18(): { name: string; sql: string }[] {
  return [{ name: "V7__virtual.sql", sql: virtual }];
}
