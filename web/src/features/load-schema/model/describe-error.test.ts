import { describe, expect, it } from "vitest";
import { describeSchemaError } from "@/features/load-schema/model/describe-error";
import { layoutKeyFor } from "@/features/load-schema/model/layout-key";
import { ApiError } from "@/shared/api";
import { sampleModel } from "@/test/fixtures";

describe("describeSchemaError", () => {
  it("names the upstream cause with the connection test's hint", () => {
    const copy = describeSchemaError(new ApiError("upstream", "password authentication failed", 502, "auth"));
    expect(copy.title).toBe("Authentication failed");
    expect(copy.hint).toMatch(/pg_hba\.conf/);
    expect(copy.message).toBe("password authentication failed");
    expect(describeSchemaError(new ApiError("upstream", "x", 502, "weird")).title).toBe("Connection failed");
  });

  it("explains a statement timeout with the profile's limit", () => {
    const copy = describeSchemaError(new ApiError("timeout", "cancelled", 504), 10_000);
    expect(copy.title).toBe("Reading the schema timed out");
    expect(copy.hint).toContain("(10 s)");
  });
});

describe("layoutKeyFor", () => {
  it("is the sorted schema list joined by commas", () => {
    const model = sampleModel();
    model.schemas = ["shop", "app", "billing"];
    expect(layoutKeyFor(model)).toBe("app,billing,shop");
  });
});
