import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { check } from "../src/check";
import { problems, type Report, repoRoot } from "../src/report";

const repo = path.resolve(__dirname, "../..");
const fixture = path.join(__dirname, "fixture/db");
const risky = path.join(fixture, "migrations/0002_risky.sql");

describe("problems", () => {
  const report = JSON.parse(readFileSync(path.join(__dirname, "report.json"), "utf8")) as Report;
  const list = problems(report, repo);

  it("makes one problem per finding, at the statement, against the repo root", () => {
    expect(list.map((p) => [path.relative(fixture, p.file), p.line, p.code, p.severity])).toEqual([
      ["migrations/0002_risky.sql", 0, "DBS-TYPE-REWRITE", "error"],
      ["migrations/0002_risky.sql", 0, "DBS-TYPE-NARROWING", "warning"],
      ["migrations/0002_risky.sql", 0, "DBS-NO-LOCK-TIMEOUT", "error"],
      ["migrations/0002_risky.sql", 1, "DBS-INDEX-NOT-CONCURRENT", "error"],
      ["migrations/0002_risky.sql", 1, "DBS-NO-LOCK-TIMEOUT", "error"],
    ]);
  });

  it("appends the safer alternative", () => {
    expect(list[3]?.message).toBe(
      "CREATE INDEX on public.orders holds SHARE (blocks writes) for the whole build. Safer: CREATE INDEX CONCURRENTLY outside a transaction block",
    );
  });

  it("reports unanalyzable statements and keeps absolute paths", () => {
    const out = problems({ statements: [], unanalyzable: [{ file: risky, line: 3, message: "syntax error" }] }, "/elsewhere");
    expect(out).toEqual([
      { file: risky, line: 2, column: 0, endLine: 2, severity: "warning", code: "DBS-UNANALYZABLE", message: "Not analyzed: syntax error" },
    ]);
  });
});

describe("repoRoot", () => {
  it("finds the nearest folder holding .git", () => {
    expect(repoRoot(fixture)).toBe(repo);
  });

  it("falls back to the folder itself", () => {
    const dir = mkdtempSync(path.join(tmpdir(), "dbscope-root-"));
    mkdirSync(path.join(dir, "a"));
    expect(repoRoot(path.join(dir, "a"))).toBe(path.join(dir, "a"));
  });
});

describe("check, against the real CLI", () => {
  const binary = path.join(repo, "bin/dbscope");

  it("returns a report whose problems land on the fixture's files", async () => {
    const { report, root } = await check({
      binary,
      dir: path.join(fixture, "migrations"),
      cwd: fixture,
      schemaDump: path.join(fixture, "schema.sql"),
      dsn: undefined,
    });
    const files = new Set(problems(report, root).map((p) => p.file));
    expect([...files]).toEqual([risky]);
  });

  it("rejects with the CLI's message when the base schema cannot be read", async () => {
    await expect(
      check({ binary, dir: path.join(fixture, "migrations"), cwd: fixture, schemaDump: path.join(fixture, "missing.sql") }),
    ).rejects.toThrow(/missing\.sql/);
  });

  it("names a binary that is not there", async () => {
    await expect(check({ binary: "/nonexistent/dbscope", dir: fixture, cwd: fixture, schemaDump: "x" })).rejects.toThrow(
      "/nonexistent/dbscope was not found",
    );
  });
});

describe("check, against a SQLite file", () => {
  it("passes --sqlite and maps SQLite findings", async () => {
    const dir = mkdtempSync(path.join(tmpdir(), "dbscope-sqlite-"));
    const migrations = path.join(dir, "migrations");
    mkdirSync(migrations);
    writeFileSync(path.join(migrations, "1.sql"), "VACUUM;\n");
    const db = path.join(dir, "app.db");
    writeFileSync(db, "");
    const { report, root } = await check({ binary: path.join(repo, "bin/dbscope"), dir: migrations, cwd: dir, sqlite: db });
    expect(problems(report, root).map((p) => [p.code, p.severity, p.line])).toEqual([["DBS-SQLITE-VACUUM", "error", 0]]);
  });
});

describe("check, as MySQL without a server", () => {
  it("passes --mysql-version and reports MySQL findings", async () => {
    const dir = mkdtempSync(path.join(tmpdir(), "dbscope-mysql-"));
    const migrations = path.join(dir, "migrations");
    mkdirSync(migrations);
    writeFileSync(path.join(migrations, "1.sql"), "ALTER TABLE orders MODIFY total BIGINT;\n");
    const { report, root } = await check({ binary: path.join(repo, "bin/dbscope"), dir: migrations, cwd: dir, mysqlVersion: "8.4" });
    expect(problems(report, root).map((p) => [p.code, p.severity])).toEqual([
      ["DBS-MYSQL-BLOCKS-WRITES", "error"],
      ["DBS-MYSQL-LOCK-WAIT", "warning"],
    ]);
  });
});
