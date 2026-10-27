import { describe, expect, it } from "vitest";
import { csvField, csvFileName, escapeFormula, toCsv } from "@/entities/query/model/csv";

describe("CSV serializer", () => {
  it("writes NULL as an empty unquoted field and an empty string as a quoted empty field", () => {
    expect(csvField(null)).toBe("");
    expect(csvField("")).toBe('""');
    expect(toCsv([{ name: "a" }, { name: "b" }], [[null, ""]])).toBe('a,b\r\n,""\r\n');
  });

  it("quotes fields with commas, quotes, CR, LF and edge whitespace, doubling quotes", () => {
    expect(csvField("a,b")).toBe('"a,b"');
    expect(csvField('say "hi"')).toBe('"say ""hi"""');
    expect(csvField("one\ntwo")).toBe('"one\ntwo"');
    expect(csvField("one\r\ntwo")).toBe('"one\r\ntwo"');
    expect(csvField(" padded ")).toBe('" padded "');
    expect(csvField("plain")).toBe("plain");
  });

  it.each([
    ["=SUM(A1:A9)", "'=SUM(A1:A9)"],
    ["+cmd|' /C calc'!A0", "'+cmd|' /C calc'!A0"],
    ["-2+3", "'-2+3"],
    ["@SUM(1)", "'@SUM(1)"],
    ["\tleading tab", "'\tleading tab"],
    ["\rleading cr", "'\rleading cr"],
  ])("prefixes formula-like text %j", (input, escaped) => {
    expect(escapeFormula(input)).toBe(escaped);
  });

  it("leaves plain signed numbers and numbers from the database alone", () => {
    expect(escapeFormula("-12.5")).toBe("-12.5");
    expect(escapeFormula("+3e10")).toBe("+3e10");
    expect(csvField(-7)).toBe("-7");
    expect(csvField(true)).toBe("true");
  });

  it("escapes then quotes when both apply, and serializes JSON values", () => {
    expect(csvField('=HYPERLINK("x","y")')).toBe('"\'=HYPERLINK(""x"",""y"")"');
    expect(csvField({ a: 1, b: [2, "x"] })).toBe('"{""a"":1,""b"":[2,""x""]}"');
    expect(csvField([1, 2])).toBe('"[1,2]"');
  });

  it("escapes and quotes header names too and ends every row with CRLF", () => {
    const csv = toCsv(
      [{ name: "=id" }, { name: "note, text" }],
      [
        [1, "ok"],
        [2, null],
      ],
    );
    expect(csv).toBe(`'=id,"note, text"\r\n1,ok\r\n2,\r\n`);
  });

  it("builds a safe file name", () => {
    expect(csvFileName("orders dev/prod", new Date("2026-10-27T10:15:00Z"))).toBe(
      "orders-dev-prod-2026-10-27T10-15-00.csv",
    );
    expect(csvFileName("///", new Date("2026-10-27T10:15:00Z"))).toBe("result-2026-10-27T10-15-00.csv");
  });
});
