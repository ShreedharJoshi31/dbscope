import { existsSync } from "node:fs";
import path from "node:path";

/** The parts of `dbscope migrate check --format json` the extension reads. */
export interface Report {
  statements: {
    file: string;
    line: number;
    column: number;
    endLine: number;
    findings: { ruleId: string; rating: Rating; message: string; safeAlternative?: string }[];
  }[];
  unanalyzable: { file: string; line: number; message: string }[] | null;
}

export type Rating = "low" | "medium" | "high" | "critical";
export type Severity = "error" | "warning" | "information";

export interface Problem {
  /** Absolute path. */
  file: string;
  /** Zero-based, as editors count. */
  line: number;
  column: number;
  endLine: number;
  severity: Severity;
  code: string;
  message: string;
}

const SEVERITY: Record<Rating, Severity> = {
  critical: "error",
  high: "error",
  medium: "warning",
  low: "information",
};

/** Mirrors ingest.RepoRoot: report paths are relative to the nearest folder holding .git, else the CLI's cwd. */
export function repoRoot(dir: string): string {
  for (let d = path.resolve(dir); ; ) {
    if (existsSync(path.join(d, ".git"))) return d;
    const parent = path.dirname(d);
    if (parent === d) return path.resolve(dir);
    d = parent;
  }
}

export function problems(report: Report, root: string): Problem[] {
  const abs = (f: string) => (path.isAbsolute(f) ? path.normalize(f) : path.join(root, f));
  const out: Problem[] = [];
  for (const s of report.statements) {
    for (const f of s.findings) {
      out.push({
        file: abs(s.file),
        line: s.line - 1,
        column: s.column - 1,
        endLine: s.endLine - 1,
        severity: SEVERITY[f.rating] ?? "warning",
        code: f.ruleId,
        message: f.safeAlternative ? `${f.message}. Safer: ${f.safeAlternative}` : f.message,
      });
    }
  }
  for (const u of report.unanalyzable ?? []) {
    const line = Math.max(0, u.line - 1);
    out.push({
      file: abs(u.file),
      line,
      column: 0,
      endLine: line,
      severity: "warning",
      code: "DBS-UNANALYZABLE",
      message: `Not analyzed: ${u.message}`,
    });
  }
  return out;
}
