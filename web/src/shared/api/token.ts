import { type SafeStorage, safeSessionStorage } from "@/shared/lib/storage";

const TOKEN_KEY = "dbscope.token";

let cachedToken: string | null = null;

interface BootstrapEnv {
  location: Pick<Location, "hash" | "pathname" | "search">;
  history: Pick<History, "replaceState" | "state">;
  storage: SafeStorage;
}

/**
 * Moves the launch token from `#t=<token>` into sessionStorage and strips the
 * fragment so it does not linger in the address bar or history. Returns the
 * token for this tab, or null when dbscope was not opened from `dbscope ui`.
 */
export function bootstrapToken(
  env: BootstrapEnv = { location: window.location, history: window.history, storage: safeSessionStorage },
): string | null {
  const params = new URLSearchParams(env.location.hash.replace(/^#/, ""));
  const fromHash = params.get("t");
  if (fromHash) {
    env.storage.setItem(TOKEN_KEY, fromHash);
    params.delete("t");
    const rest = params.toString();
    env.history.replaceState(
      env.history.state,
      "",
      `${env.location.pathname}${env.location.search}${rest ? `#${rest}` : ""}`,
    );
  }
  cachedToken = fromHash ?? env.storage.getItem(TOKEN_KEY);
  return cachedToken;
}

export function getToken(): string | null {
  return cachedToken;
}
