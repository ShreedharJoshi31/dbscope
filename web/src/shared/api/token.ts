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

/**
 * Pasting a new `dbscope ui` link into an open tab changes only the fragment,
 * so the page does not reload and bootstrapToken never sees it. Adopt the new
 * token and reload: the query cache and every screen derived from the old
 * token (rejected, missing) then start over cleanly.
 */
export function reloadOnNewToken(win: Window = window): () => void {
  const onHashChange = () => {
    if (!new URLSearchParams(win.location.hash.replace(/^#/, "")).get("t")) return;
    bootstrapToken({ location: win.location, history: win.history, storage: safeSessionStorage });
    win.location.reload();
  };
  win.addEventListener("hashchange", onHashChange);
  return () => win.removeEventListener("hashchange", onHashChange);
}

export function getToken(): string | null {
  return cachedToken;
}
