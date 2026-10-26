/**
 * Storage access that never throws. Browsers can deny storage (privacy modes,
 * disabled cookies, quota), and a preference write must not break the app.
 */
export interface SafeStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

function wrap(resolve: () => Storage): SafeStorage {
  return {
    getItem(key) {
      try {
        return resolve().getItem(key);
      } catch {
        return null;
      }
    },
    setItem(key, value) {
      try {
        resolve().setItem(key, value);
      } catch {
        // Storage unavailable: the value lives only for this page load.
      }
    },
    removeItem(key) {
      try {
        resolve().removeItem(key);
      } catch {
        // Nothing to remove when storage is unavailable.
      }
    },
  };
}

export const safeLocalStorage = wrap(() => window.localStorage);
export const safeSessionStorage = wrap(() => window.sessionStorage);
