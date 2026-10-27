import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { type CellKind, cellKind, formatCell, formatRowTsv, isNumericType } from "@/entities/query/model/value-format";
import { CellValueView } from "@/entities/query/ui/cell-value";
import type { CellValue } from "@/shared/api";

describe("value formatting per type", () => {
  it.each([
    [null, "bigint", "null", "NULL"],
    [42, "integer", "number", "42"],
    ["9007199254740993", "bigint", "number", "9007199254740993"],
    ["12.50", "numeric(10,2)", "number", "12.50"],
    ["NaN", "double precision", "number", "NaN"],
    [true, "boolean", "boolean", "true"],
    [{ a: 1 }, "jsonb", "json", '{"a":1}'],
    [[1, "x"], "json", "json", '[1,"x"]'],
    ["2026-10-27 10:00:00+00", "timestamp with time zone", "text", "2026-10-27 10:00:00+00"],
    ["\\xdeadbeef", "bytea", "text", "\\xdeadbeef"],
    ["P1DT2H", "interval", "text", "P1DT2H"],
    ["", "text", "text", ""],
  ] as [CellValue, string, CellKind, string][])("%j as %s renders as %s %j", (value, type, kind, text) => {
    expect(cellKind(value, type)).toBe(kind);
    expect(formatCell(value)).toBe(text);
  });

  it("recognizes numeric types by format_type name, not other types containing digits", () => {
    expect(isNumericType("numeric(12,2)")).toBe(true);
    expect(isNumericType("double precision")).toBe(true);
    expect(isNumericType("character varying(80)")).toBe(false);
    expect(isNumericType("int4range")).toBe(false);
    expect(isNumericType(undefined)).toBe(false);
  });

  it("renders NULL as italic text that differs from an empty string", () => {
    render(
      <>
        <CellValueView value={null} type="text" />
        <CellValueView value="" type="text" />
        <CellValueView value={7} type="integer" />
      </>,
    );
    const nul = screen.getByText("NULL");
    expect(nul).toHaveAttribute("data-kind", "null");
    expect(nul.className).toMatch(/italic/);
    const empty = screen.getByTitle("Empty string");
    expect(empty).toHaveTextContent("");
    expect(empty).toHaveAttribute("data-kind", "text");
    expect(screen.getByText("7").className).toMatch(/font-mono/);
  });

  it("copies a row as tab-separated text with line breaks flattened", () => {
    expect(formatRowTsv([1, null, "a\tb\nc", ""])).toBe("1\tNULL\ta b c\t");
  });
});
