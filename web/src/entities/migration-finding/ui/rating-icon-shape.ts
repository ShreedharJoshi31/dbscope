import { type RatingShape, type RatingTone, ratingMeta } from "@/entities/migration-finding/model/rating";
import type { Rating } from "@/shared/api";

/**
 * Icon geometry shared by the React badge and the CodeMirror gutter marker,
 * so both draw the same shape. Paths are lucide's info, triangle-alert,
 * octagon-alert and octagon-minus (24×24, stroke 2), the icon set the rest of
 * the UI uses.
 */
const OCTAGON =
  "M2.586 16.726A2 2 0 0 1 2 15.312V8.688a2 2 0 0 1 .586-1.414l4.688-4.688A2 2 0 0 1 8.688 2h6.624a2 2 0 0 1 1.414.586l4.688 4.688A2 2 0 0 1 22 8.688v6.624a2 2 0 0 1-.586 1.414l-4.688 4.688a2 2 0 0 1-1.414.586H8.688a2 2 0 0 1-1.414-.586z";

export type IconPart =
  | { tag: "circle"; cx: number; cy: number; r: number }
  | { tag: "path"; d: string; role?: "fill" | "bar" };

export const RATING_ICON_PARTS: Record<RatingShape, readonly IconPart[]> = {
  circle: [
    { tag: "circle", cx: 12, cy: 12, r: 10 },
    { tag: "path", d: "M12 16v-4" },
    { tag: "path", d: "M12 8h.01" },
  ],
  triangle: [
    { tag: "path", d: "m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" },
    { tag: "path", d: "M12 9v4" },
    { tag: "path", d: "M12 17h.01" },
  ],
  octagon: [
    { tag: "path", d: OCTAGON },
    { tag: "path", d: "M12 8v4" },
    { tag: "path", d: "M12 16h.01" },
  ],
  "octagon-filled": [
    { tag: "path", d: OCTAGON, role: "fill" },
    { tag: "path", d: "M8 12h8", role: "bar" },
  ],
};

/** Token classes per tone; the filled Critical shape uses the emphasis colour with an on-emphasis bar. */
export const RATING_ICON_CLASS: Record<RatingTone, string> = {
  info: "text-status-info-fg",
  warn: "text-status-warn-fg",
  danger: "text-status-danger-fg",
};

export function ratingIconClass(rating: Rating): string {
  const meta = ratingMeta(rating);
  return meta.shape === "octagon-filled" ? "text-status-danger-emphasis" : RATING_ICON_CLASS[meta.tone];
}

const SVG_NS = "http://www.w3.org/2000/svg";

/** The rating icon as a DOM node, for CodeMirror gutter markers (not React). Decorative: callers add the text. */
export function ratingIconElement(rating: Rating, doc: Document = document): SVGSVGElement {
  const meta = ratingMeta(rating);
  const svg = doc.createElementNS(SVG_NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "2");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("data-shape", meta.shape);
  svg.setAttribute("class", ratingIconClass(rating));
  for (const part of RATING_ICON_PARTS[meta.shape]) {
    const el = doc.createElementNS(SVG_NS, part.tag);
    if (part.tag === "circle") {
      el.setAttribute("cx", String(part.cx));
      el.setAttribute("cy", String(part.cy));
      el.setAttribute("r", String(part.r));
    } else {
      el.setAttribute("d", part.d);
      if (part.role === "fill") el.setAttribute("fill", "currentColor");
      if (part.role === "bar") el.setAttribute("stroke", "var(--text-on-emphasis)");
    }
    svg.append(el);
  }
  return svg;
}
