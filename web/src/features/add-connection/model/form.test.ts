import { describe, expect, it } from "vitest";
import {
  applyParsedUrl,
  EMPTY_CONNECTION_FORM,
  firstInvalidField,
  toProfileInput,
  validateConnectionForm,
} from "@/features/add-connection/model/form";

const valid = {
  ...EMPTY_CONNECTION_FORM,
  name: "orders-dev",
  host: "localhost",
  database: "orders",
  user: "app",
  env: "local" as const,
};

describe("validateConnectionForm", () => {
  it("accepts a complete form", () => {
    expect(validateConnectionForm(valid)).toEqual({});
  });

  it("requires name, env, host, database and user on an empty form", () => {
    const errors = validateConnectionForm(EMPTY_CONNECTION_FORM);
    expect(Object.keys(errors).sort()).toEqual(["database", "env", "host", "name", "user"]);
    expect(firstInvalidField(errors)).toBe("name");
  });

  it.each(["0", "65536", "54x", "", "-1"])("rejects port %j", (port) => {
    expect(validateConnectionForm({ ...valid, port }).port).toBe("Port must be a whole number from 1 to 65535.");
  });

  it("limits names to 80 characters", () => {
    expect(validateConnectionForm({ ...valid, name: "x".repeat(81) }).name).toMatch(/at most 80/);
  });

  it("requires a positive whole-number statement timeout", () => {
    expect(validateConnectionForm({ ...valid, statementTimeoutMs: "0" }).statementTimeoutMs).toBeDefined();
    expect(validateConnectionForm({ ...valid, statementTimeoutMs: "1.5" }).statementTimeoutMs).toBeDefined();
  });
});

describe("SQLite", () => {
  const sqlite = { ...EMPTY_CONNECTION_FORM, engine: "sqlite" as const, name: "shop", env: "local" as const };

  it("needs only an absolute file path, not network details", () => {
    expect(Object.keys(validateConnectionForm({ ...sqlite, path: "" }))).toEqual(["path"]);
    for (const path of ["app.db", "./app.db", "~/app.db"]) {
      expect(validateConnectionForm({ ...sqlite, path }).path).toMatch(/full path/);
    }
    for (const path of ["/Users/me/app.db", "C:\\data\\app.db", "D:/app.db", "\\\\server\\share\\app.db"]) {
      expect(validateConnectionForm({ ...sqlite, path })).toEqual({});
    }
  });

  it("sends the engine and path and no network fields", () => {
    const input = toProfileInput({ ...sqlite, path: " /data/app.db ", host: "ignored", password: "ignored" });
    expect(input).toMatchObject({ engine: "sqlite", path: "/data/app.db", host: "", user: "", schemas: [] });
    expect(input).not.toHaveProperty("password");
    expect(input).not.toHaveProperty("maxCost");
  });
});

describe("toProfileInput", () => {
  it("trims, converts numbers, splits schemas and omits an empty password", () => {
    expect(
      toProfileInput({ ...valid, name: " orders-dev ", port: "6432", schemas: "public, billing ,", password: "" }),
    ).toEqual({
      engine: "postgres",
      name: "orders-dev",
      host: "localhost",
      port: 6432,
      database: "orders",
      user: "app",
      sslMode: "prefer",
      env: "local",
      schemas: ["public", "billing"],
      summaryMode: "profile",
      maxRowsToLLM: 50,
      statementTimeoutMs: 30000,
    });
  });

  it("sends the root certificate only for verify modes", () => {
    expect(toProfileInput({ ...valid, sslMode: "require", sslRootCert: "/ca.crt" }).sslRootCert).toBeUndefined();
    expect(toProfileInput({ ...valid, sslMode: "verify-full", sslRootCert: "/ca.crt" }).sslRootCert).toBe("/ca.crt");
  });
});

describe("applyParsedUrl", () => {
  it("fills connection fields and suggests a name when none is set", () => {
    const { values, filled } = applyParsedUrl(EMPTY_CONNECTION_FORM, {
      host: "db.internal",
      port: 6543,
      database: "billing",
      user: "reader",
      password: "s3cret",
      sslMode: "require",
    });
    expect(values).toMatchObject({
      name: "billing@db.internal",
      host: "db.internal",
      port: "6543",
      database: "billing",
      user: "reader",
      password: "s3cret",
      sslMode: "require",
    });
    expect(filled).toEqual(["name", "host", "port", "database", "user", "SSL mode", "password"]);
  });

  it("keeps a name the user already typed", () => {
    const { values } = applyParsedUrl(
      { ...EMPTY_CONNECTION_FORM, name: "mine" },
      { host: "h", port: 5432, database: "d", user: "u", sslMode: "prefer" },
    );
    expect(values.name).toBe("mine");
    expect(values.password).toBe("");
  });
});

describe("maxCost", () => {
  it("blank means the environment default (omitted), 0 means no limit, a number is the limit", () => {
    expect(toProfileInput({ ...valid, maxCost: "" })).not.toHaveProperty("maxCost");
    expect(toProfileInput({ ...valid, maxCost: "0" }).maxCost).toBe(0);
    expect(toProfileInput({ ...valid, maxCost: " 250000 " }).maxCost).toBe(250_000);
    expect(validateConnectionForm({ ...valid, maxCost: "-1" }).maxCost).toBeDefined();
    expect(validateConnectionForm({ ...valid, maxCost: "lots" }).maxCost).toBeDefined();
    expect(validateConnectionForm({ ...valid, maxCost: "1e6" }).maxCost).toBeDefined();
    expect(validateConnectionForm({ ...valid, maxCost: "1500.5" }).maxCost).toBeUndefined();
  });
});
