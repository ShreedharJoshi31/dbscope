/**
 * Resolves design tokens to colours a 2D canvas accepts. The tokens are
 * light-dark() pairs, which a canvas fillStyle need not understand, so each is
 * resolved through a probe element's computed colour (always rgb()).
 */
export function tokenColors<K extends string>(within: Element, names: readonly K[]): Record<K, string> {
  const probe = document.createElement("span");
  probe.style.display = "none";
  within.appendChild(probe);
  const out = {} as Record<K, string>;
  for (const name of names) {
    probe.style.color = `var(${name})`;
    out[name] = getComputedStyle(probe).color;
  }
  probe.remove();
  return out;
}

/** Calls back when the device pixel ratio changes (the window moved to another screen, or zoomed). */
export function onPixelRatioChange(callback: () => void): () => void {
  let query: MediaQueryList | null = null;
  const listen = () => {
    query = window.matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
    query.addEventListener("change", changed, { once: true });
  };
  const changed = () => {
    callback();
    listen();
  };
  listen();
  return () => query?.removeEventListener("change", changed);
}
