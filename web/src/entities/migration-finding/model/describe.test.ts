import { describe, expect, it } from "vitest";
import {
  formatBytes,
  lockModeTone,
  lockModeWords,
  lockSentence,
  lockTimeoutCopy,
  sizeWords,
} from "@/entities/migration-finding/model/describe";
import {
  mysqlVersionLabel,
  pgVersionLabel,
  ratingMeta,
  ratingRank,
  sqliteVersionLabel,
  worstRating,
} from "@/entities/migration-finding/model/rating";

describe("lock modes in plain words", () => {
  it("names what each PostgreSQL lock mode blocks", () => {
    expect(lockSentence("ACCESS EXCLUSIVE")).toBe("ACCESS EXCLUSIVE — blocks reads and writes");
    expect(lockModeWords("SHARE")).toBe("blocks writes; reads continue");
    expect(lockModeWords("SHARE UPDATE EXCLUSIVE")).toContain("reads and writes continue");
    expect(lockModeWords("SHARE ROW EXCLUSIVE")).toContain("blocks writes");
    expect(lockModeWords("EXCLUSIVE")).toContain("plain reads continue");
    expect(lockModeWords("ROW EXCLUSIVE")).toContain("reads and writes continue");
    expect(lockModeWords("ACCESS SHARE")).toContain("reads and writes continue");
    expect(lockModeWords("ROW SHARE")).toContain("reads and writes continue");
  });

  it("grades the modes by what they block and handles unknown names", () => {
    expect(lockModeTone("ACCESS EXCLUSIVE")).toBe("danger");
    expect(lockModeTone("SHARE")).toBe("warn");
    expect(lockModeTone("SHARE UPDATE EXCLUSIVE")).toBe("info");
    expect(lockModeWords("SIESTA")).toMatch(/no description/);
  });

  it("describes lock_timeout and size", () => {
    expect(lockTimeoutCopy(true, 1).tone).toBe("success");
    expect(lockTimeoutCopy(false, 1).text).toMatch(/^No lock_timeout: while it waits/);
    expect(lockTimeoutCopy(false, 0).tone).toBe("info");
    expect(formatBytes(164429824)).toBe("157 MiB");
    expect(formatBytes(512)).toBe("512 bytes");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(sizeWords(undefined, 1)).toMatch(/^Unknown/);
    expect(sizeWords(undefined, 0)).toBe("No relation is locked");
  });
});

describe("ratings", () => {
  it("maps each rating to the design system's shape and tone", () => {
    expect(["low", "medium", "high", "critical"].map((r) => ratingMeta(r as "low").shape)).toEqual([
      "circle",
      "triangle",
      "octagon",
      "octagon-filled",
    ]);
    expect(ratingMeta("low").tone).toBe("info");
    expect(ratingMeta("medium").tone).toBe("warn");
    expect(ratingMeta("critical").tone).toBe("danger");
    expect(ratingRank("critical")).toBeGreaterThan(ratingRank("high"));
    expect(worstRating(["low", "high", "medium"])).toBe("high");
    expect(worstRating([])).toBe("low");
  });

  it("labels server versions", () => {
    expect(pgVersionLabel(170000)).toBe("PostgreSQL 17");
    expect(pgVersionLabel(170002)).toBe("PostgreSQL 17.2");
    expect(pgVersionLabel(0)).toMatch(/unknown/);
    expect(sqliteVersionLabel(3054001)).toBe("SQLite 3.54.1");
    expect(sqliteVersionLabel(3035005)).toBe("SQLite 3.35.5");
    expect(mysqlVersionLabel(80413, "mysql")).toBe("MySQL 8.4.13");
    expect(mysqlVersionLabel(110809, "mariadb")).toBe("MariaDB 11.8.9");
  });

  it("describes SQLite's database-wide write lock", () => {
    expect(lockModeWords("DATABASE WRITE")).toMatch(/^blocks every other writer/);
    expect(lockModeTone("DATABASE WRITE")).toBe("warn");
  });
});
