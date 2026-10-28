import { describe, expect, it } from "vitest";
import {
  collectDirectory,
  collectFileList,
  type DirectoryHandleLike,
  isMigrationFile,
  reviewTitle,
  validateSources,
} from "@/features/analyze-migration/model/read-files";
import { MIGRATION_LIMITS } from "@/shared/api";

function dir(name: string, children: (DirectoryHandleLike | { kind: "file"; name: string; text?: string })[]) {
  const handle: DirectoryHandleLike = {
    kind: "directory",
    name,
    values: async function* () {
      for (const c of children) {
        if (c.kind === "directory") yield c;
        else yield { kind: "file", name: c.name, getFile: async () => new File([c.text ?? "SELECT 1;"], c.name) };
      }
    },
  };
  return handle;
}

describe("reading migration files", () => {
  it("keeps SQL, Flyway .sql.conf and atlas.sum, and ignores the rest", () => {
    expect(
      ["a.sql", "V1__x.SQL", "V1__x.sql.conf", "m/atlas.sum", "002_users.cql", "V1_1__fields.http"].every(
        isMigrationFile,
      ),
    ).toBe(true);
    expect(["README.md", "a.sql.bak", "schema.json"].some(isMigrationFile)).toBe(false);
    const files = [new File(["x"], "0002_b.sql"), new File(["x"], "notes.txt"), new File(["x"], "0001_a.sql")];
    const { picked, ignored } = collectFileList(files);
    expect(picked.map((p) => p.path)).toEqual(["0002_b.sql", "0001_a.sql"]); // drop order kept
    expect(ignored).toEqual(["notes.txt"]);
  });

  it("walks a folder in natural order with relative paths (Prisma needs them)", async () => {
    const root = dir("migrations", [
      dir("20261010_b", [{ kind: "file", name: "migration.sql" }]),
      { kind: "file", name: "V10__late.sql" },
      { kind: "file", name: "V2__early.sql" },
      { kind: "file", name: ".DS_Store" },
      dir(".git", [{ kind: "file", name: "x.sql" }]),
    ]);
    const { picked, ignored } = await collectDirectory(root);
    expect(picked.map((p) => p.path)).toEqual([
      "migrations/20261010_b/migration.sql",
      "migrations/V2__early.sql",
      "migrations/V10__late.sql",
    ]);
    expect(ignored).toEqual(["migrations/.DS_Store"]);
  });

  it("validates the agent's limits before sending (400s explained up front)", () => {
    expect(validateSources([{ name: "a.sql.conf", sql: "" }], ["x.txt"])).toEqual({
      code: "no_files",
      ignored: ["x.txt"],
    });
    const many = Array.from({ length: MIGRATION_LIMITS.files + 1 }, (_, i) => ({ name: `${i}.sql`, sql: "" }));
    expect(validateSources(many, [])).toMatchObject({ code: "too_many", count: 201, limit: 200 });
    const big = "é".repeat(MIGRATION_LIMITS.fileBytes / 2 + 1); // 2 bytes each in UTF-8
    expect(validateSources([{ name: "big.sql", sql: big }], [])).toMatchObject({
      code: "too_large",
      files: [{ name: "big.sql", bytes: MIGRATION_LIMITS.fileBytes + 2 }],
    });
    expect(validateSources([{ name: "a.sql", sql: "SELECT 1;" }], [])).toBeNull();
  });

  it("titles the tab by file name or count", () => {
    expect(reviewTitle([{ name: "migrations/0042_orders.sql" }])).toBe("0042_orders.sql");
    expect(reviewTitle([{ name: "a.sql" }, { name: "b.sql" }, { name: "a.sql.conf" }, { name: "c.sql" }])).toBe(
      "3 migration files",
    );
  });
});
