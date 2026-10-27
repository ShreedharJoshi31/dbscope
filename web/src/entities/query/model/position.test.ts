import { describe, expect, it } from "vitest";
import { describeQueryError, editorDiagnostic } from "@/entities/query/model/error-copy";
import { codePointToIndex, positionToRange } from "@/entities/query/model/position";
import { ApiError } from "@/shared/api";

const rejected = (position: number) =>
  new ApiError("rejected", 'syntax error at or near "WHER"', 422, "syntax", { position });
const dbError = (position: number) =>
  new ApiError("database", 'column "nme" does not exist', 400, undefined, { sqlstate: "42703", position });

describe("error positions", () => {
  it("maps a 1-based character offset to the token, line and column", () => {
    const sql = "SELECT id\nFROM shop.orders\nWHER id = 1";
    const range = positionToRange(sql, sql.indexOf("WHER") + 1);
    expect(range).toEqual({ from: 27, to: 31, line: 3, column: 1 });
    expect(sql.slice(range?.from, range?.to)).toBe("WHER");
  });

  it("counts characters, not UTF-16 units, before the error", () => {
    const sql = "SELECT '😀' AS e, nme FROM t";
    // PostgreSQL counts the emoji as one character, so "nme" starts at character 18.
    expect(codePointToIndex(sql, 18)).toBe(sql.indexOf("nme"));
    expect(positionToRange(sql, 18)).toMatchObject({ line: 1, column: 18 });
  });

  it("covers a whole quoted literal and clamps positions past the end", () => {
    const sql = "SELECT 'unterminated";
    expect(positionToRange(sql, 8)).toMatchObject({ from: 7, to: sql.length });
    expect(positionToRange("SELECT 1", 99)).toMatchObject({ from: 7, to: 8 });
    expect(positionToRange("SELECT 1", 0)).toBeNull();
  });

  it("a guard rejection points into the submitted SQL and becomes an editor diagnostic", () => {
    const sql = "SELECT * FROM shop.orders o WHER id = 1";
    const error = rejected(sql.indexOf("WHER") + 1);
    expect(describeQueryError(error).position).toEqual({ value: 29, target: "input" });
    expect(editorDiagnostic(error, sql, sql)).toMatchObject({ from: 28, to: 32 });
  });

  it("drops the editor diagnostic once the text changed", () => {
    const sql = "SELECT * FROM t WHER 1";
    expect(editorDiagnostic(rejected(17), sql, `${sql} `)).toBeNull();
  });

  it("a database position refers to executedSql, so the editor marks it only when the texts match", () => {
    const typed = "select nme from shop.customers";
    const executed = "SELECT nme FROM shop.customers LIMIT 501";
    expect(describeQueryError(dbError(8)).position).toEqual({ value: 8, target: "executed" });
    expect(editorDiagnostic(dbError(8), typed, typed, executed)).toBeNull();
    expect(editorDiagnostic(dbError(8), typed, typed)).toBeNull();
    expect(editorDiagnostic(dbError(8), executed, executed, executed)).toMatchObject({ from: 7, to: 10 });
  });

  it("gives recovery conflicts and timeouts their guidance", () => {
    const conflict = describeQueryError(
      new ApiError("recovery_conflict", "conflict", 503, undefined, { sqlstate: "40001" }),
    );
    expect(conflict).toMatchObject({ retryable: true, title: "The replica removed rows this query needed" });
    expect(conflict.guidance).toMatch(/hot_standby_feedback/);
    expect(describeQueryError(new ApiError("timeout", "stopped", 504), 10_000).guidance).toMatch(/\(10 s\)/);
    expect(describeQueryError(new ApiError("timeout", "x", 504, undefined, { sqlstate: "25P03" })).retryable).toBe(
      true,
    );
    expect(describeQueryError(new ApiError("database", "x", 400, undefined, { sqlstate: "25006" })).title).toBe(
      "Blocked: this would modify data",
    );
  });
});
