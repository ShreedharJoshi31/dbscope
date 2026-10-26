/**
 * F6 region cycling. Regions are elements with `data-region` and tabIndex=-1;
 * hidden (collapsed) regions are skipped.
 */
export function focusAdjacentRegion(direction: 1 | -1, root: ParentNode = document): void {
  const regions = Array.from(root.querySelectorAll<HTMLElement>("[data-region]")).filter(
    (el) => el.getClientRects().length > 0 && el.offsetWidth > 0 && el.offsetHeight > 0,
  );
  if (regions.length === 0) return;
  const active = document.activeElement;
  const current = regions.findIndex((el) => el === active || (active !== null && el.contains(active)));
  const start = current === -1 ? (direction === 1 ? -1 : 0) : current;
  const next = regions[(start + direction + regions.length) % regions.length];
  next?.focus();
}
