import { describe, expect, it, vi } from "vitest";
import { bootstrapToken, getToken } from "@/shared/api";
import type { SafeStorage } from "@/shared/lib/storage";

function memoryStorage(initial: Record<string, string> = {}): SafeStorage & { data: Record<string, string> } {
  const data = { ...initial };
  return {
    data,
    getItem: (k) => data[k] ?? null,
    setItem: (k, v) => {
      data[k] = v;
    },
    removeItem: (k) => {
      delete data[k];
    },
  };
}

describe("bootstrapToken", () => {
  it("moves #t=<token> into sessionStorage and strips it from the URL", () => {
    const storage = memoryStorage();
    const replaceState = vi.fn();

    const token = bootstrapToken({
      location: { hash: "#t=abc123", pathname: "/", search: "?x=1" },
      history: { state: null, replaceState },
      storage,
    });

    expect(token).toBe("abc123");
    expect(getToken()).toBe("abc123");
    expect(storage.data["dbscope.token"]).toBe("abc123");
    expect(replaceState).toHaveBeenCalledWith(null, "", "/?x=1");
  });

  it("keeps other fragment parameters when stripping the token", () => {
    const replaceState = vi.fn();
    bootstrapToken({
      location: { hash: "#t=abc&view=erd", pathname: "/app", search: "" },
      history: { state: null, replaceState },
      storage: memoryStorage(),
    });
    expect(replaceState).toHaveBeenCalledWith(null, "", "/app#view=erd");
  });

  it("reuses the stored token after a reload without the fragment", () => {
    const replaceState = vi.fn();
    const token = bootstrapToken({
      location: { hash: "", pathname: "/", search: "" },
      history: { state: null, replaceState },
      storage: memoryStorage({ "dbscope.token": "from-session" }),
    });
    expect(token).toBe("from-session");
    expect(replaceState).not.toHaveBeenCalled();
  });

  it("returns null when there is no token anywhere", () => {
    const token = bootstrapToken({
      location: { hash: "", pathname: "/", search: "" },
      history: { state: null, replaceState: vi.fn() },
      storage: memoryStorage(),
    });
    expect(token).toBeNull();
    expect(getToken()).toBeNull();
  });
});
