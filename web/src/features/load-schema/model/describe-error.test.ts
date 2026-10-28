import { describe, expect, it } from "vitest";
import { errorCopy } from "@/entities/connection";
import { describeSchemaError } from "@/features/load-schema/model/describe-error";
import { layoutKeyFor } from "@/features/load-schema/model/layout-key";
import { ApiError } from "@/shared/api";
import { sampleModel } from "@/test/fixtures";

describe("describeSchemaError", () => {
  it("names the upstream cause with the connection test's hint", () => {
    const copy = describeSchemaError(
      new ApiError("upstream", "password authentication failed", 502, "auth"),
      undefined,
      "postgres",
    );
    expect(copy.title).toBe("Authentication failed");
    expect(copy.hint).toMatch(/pg_hba\.conf/);
    expect(copy.message).toBe("password authentication failed");
    expect(describeSchemaError(new ApiError("upstream", "x", 502, "weird")).title).toBe("Connection failed");
  });

  it("names the engine's own listen setting, and no engine's when it is unknown", () => {
    const network = new ApiError("upstream", "connection refused", 502, "network");
    expect(describeSchemaError(network, undefined, "redis").hint).toMatch(/protected-mode/);
    expect(describeSchemaError(network, undefined, "neo4j").hint).toMatch(/server\.default_listen_address/);
    expect(describeSchemaError(network, undefined, "dynamodb").hint).toMatch(/region/);
    expect(describeSchemaError(network).hint).not.toMatch(/PostgreSQL|listen_addresses/);
    expect(errorCopy("network", undefined).hint).toMatch(/listen_addresses/); // a TestResult without an engine is PostgreSQL
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
