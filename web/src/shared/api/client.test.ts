import { HttpResponse, http } from "msw/http";
import { afterEach, describe, expect, it } from "vitest";
import { ApiError, apiFetch, bootstrapToken, TOKEN_HEADER } from "@/shared/api";
import { safeSessionStorage } from "@/shared/lib/storage";
import { server } from "@/test/server";

function signInWith(token: string | null) {
  bootstrapToken({
    location: { hash: token ? `#t=${token}` : "", pathname: "/", search: "" },
    history: { state: null, replaceState: () => {} },
    storage: safeSessionStorage,
  });
}

describe("apiFetch", () => {
  afterEach(() => signInWith(null));

  it("sends the session token header and parses JSON", async () => {
    signInWith("secret-token");
    let seen: string | null = null;
    server.use(
      http.get("/api/health", ({ request }) => {
        seen = request.headers.get(TOKEN_HEADER);
        return HttpResponse.json({ version: "dev", os: "darwin", arch: "arm64" });
      }),
    );

    await expect(apiFetch("/api/health")).resolves.toEqual({ version: "dev", os: "darwin", arch: "arm64" });
    expect(seen).toBe("secret-token");
  });

  it("sends JSON bodies with a content type", async () => {
    signInWith("t");
    let contentType: string | null = null;
    let body: unknown = null;
    server.use(
      http.post("/api/connection-url/parse", async ({ request }) => {
        contentType = request.headers.get("Content-Type");
        body = await request.json();
        return HttpResponse.json({ host: "h", port: 5432, database: "d", user: "u", sslMode: "prefer" });
      }),
    );

    await apiFetch("/api/connection-url/parse", { method: "POST", body: { url: "postgres://u@h/d" } });
    expect(contentType).toBe("application/json");
    expect(body).toEqual({ url: "postgres://u@h/d" });
  });

  it("turns the error envelope into a typed ApiError", async () => {
    signInWith("t");
    server.use(
      http.post("/api/profiles", () =>
        HttpResponse.json(
          { error: { code: "validation", message: "port must be 1-65535", detail: "got 70000" } },
          { status: 400 },
        ),
      ),
    );

    const error = await apiFetch("/api/profiles", { method: "POST", body: {} }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      code: "validation",
      status: 400,
      message: "port must be 1-65535",
      detail: "got 70000",
    });
  });

  it("maps 423 secrets_locked without a message to readable text", async () => {
    signInWith("t");
    server.use(
      http.get("/api/profiles", () => HttpResponse.json({ error: { code: "secrets_locked" } }, { status: 423 })),
    );

    const error = await apiFetch("/api/profiles").catch((e: unknown) => e);
    expect(error).toMatchObject({ code: "secrets_locked", status: 423, message: "The secrets vault is locked." });
  });

  it("reports a non-envelope error body as malformed_response", async () => {
    signInWith("t");
    server.use(http.get("/api/profiles", () => new HttpResponse("<html>Bad gateway</html>", { status: 502 })));

    const error = await apiFetch("/api/profiles").catch((e: unknown) => e);
    expect(error).toMatchObject({ code: "malformed_response", status: 502 });
  });

  it("reports an unreachable agent as a network error", async () => {
    signInWith("t");
    server.use(http.get("/api/health", () => HttpResponse.error()));

    const error = await apiFetch("/api/health").catch((e: unknown) => e);
    expect(error).toMatchObject({ code: "network", status: 0 });
  });

  it("resolves 204 responses to undefined", async () => {
    signInWith("t");
    server.use(http.delete("/api/profiles/abc", () => new HttpResponse(null, { status: 204 })));

    await expect(apiFetch("/api/profiles/abc", { method: "DELETE" })).resolves.toBeUndefined();
  });
});
