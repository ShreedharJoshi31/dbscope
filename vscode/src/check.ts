import { execFile } from "node:child_process";
import { type Report, repoRoot } from "./report";

export interface CheckOptions {
  binary: string;
  /** The folder of migrations to check, in order. */
  dir: string;
  cwd: string;
  schemaDump?: string;
  stats?: string;
  /** A SQLite database file; when set the PostgreSQL options are not used. */
  sqlite?: string;
  /** Check as MySQL or MariaDB: a version for --mysql-version, used unless DBSCOPE_MYSQL_DSN is set. */
  mysqlVersion?: string;
  /** The database URL, from the environment variable the user named. */
  dsn?: string;
}

export interface CheckResult {
  report: Report;
  root: string;
}

/** Runs `dbscope migrate check --format json`. Exit 1 only means findings at or above --fail-on, so it still yields a report. */
export function check(o: CheckOptions): Promise<CheckResult> {
  const args = ["migrate", "check", o.dir, "--format", "json"];
  const env = { ...process.env };
  delete env.DBSCOPE_DSN;
  if (!o.mysqlVersion || o.sqlite) delete env.DBSCOPE_MYSQL_DSN;
  if (o.sqlite) args.push("--sqlite", o.sqlite);
  else if (o.mysqlVersion) {
    // The CLI reads DBSCOPE_MYSQL_DSN itself; the URL never appears in its arguments.
    if (!process.env.DBSCOPE_MYSQL_DSN) args.push("--mysql-version", o.mysqlVersion);
  }
  else if (o.dsn) env.DBSCOPE_DSN = o.dsn;
  else if (o.schemaDump) {
    args.push("--schema-dump", o.schemaDump);
    if (o.stats) args.push("--stats", o.stats);
  }
  return new Promise((resolve, reject) => {
    execFile(o.binary, args, { cwd: o.cwd, env, maxBuffer: 64 << 20 }, (err, stdout, stderr) => {
      const code = err && typeof err.code === "number" ? err.code : err ? -1 : 0;
      if (code !== 0 && code !== 1) {
        const why = stderr.trim() || (err?.code === "ENOENT" ? `${o.binary} was not found` : String(err?.message));
        reject(new Error(why));
        return;
      }
      try {
        resolve({ report: JSON.parse(stdout) as Report, root: repoRoot(o.cwd) });
      } catch {
        reject(new Error(`dbscope printed no JSON report: ${stderr.trim() || stdout.slice(0, 200)}`));
      }
    });
  });
}
