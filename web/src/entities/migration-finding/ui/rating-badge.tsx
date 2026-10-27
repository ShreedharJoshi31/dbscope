import { cn } from "cn";
import { ratingMeta } from "@/entities/migration-finding/model/rating";
import { RATING_ICON_PARTS, ratingIconClass } from "@/entities/migration-finding/ui/rating-icon-shape";
import type { Rating } from "@/shared/api";

/** The rating's shape (circle, triangle, octagon, filled octagon with bar). Decorative: pair it with text. */
export function RatingIcon({ rating, className }: { rating: Rating; className?: string }) {
  const meta = ratingMeta(rating);
  return (
    <svg
      aria-hidden
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      data-shape={meta.shape}
      className={cn("size-3.5 shrink-0", ratingIconClass(rating), className)}
    >
      {RATING_ICON_PARTS[meta.shape].map((part) =>
        part.tag === "circle" ? (
          <circle key="circle" cx={part.cx} cy={part.cy} r={part.r} />
        ) : (
          <path
            key={part.d}
            d={part.d}
            fill={part.role === "fill" ? "currentColor" : undefined}
            stroke={part.role === "bar" ? "var(--text-on-emphasis)" : undefined}
          />
        ),
      )}
    </svg>
  );
}

const BADGE_TONE = {
  info: "border-status-info-fg text-status-info-fg",
  warn: "border-status-warn-fg text-status-warn-fg",
  danger: "border-status-danger-fg text-status-danger-fg",
} as const;

interface RatingBadgeProps {
  rating: Rating;
  /** Shown after the label, e.g. a count. */
  suffix?: string | number | undefined;
  size?: "sm" | "md";
  className?: string;
}

/** Outline badge: icon + text + token colour, never colour alone (UX rule 7). */
export function RatingBadge({ rating, suffix, size = "sm", className }: RatingBadgeProps) {
  const meta = ratingMeta(rating);
  return (
    <span
      data-testid="rating-badge"
      data-rating={rating}
      title={meta.description}
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded-sm border bg-surface-canvas font-semibold whitespace-nowrap",
        size === "sm" ? "h-5 px-1.5 text-2xs" : "h-6 px-2 text-xs",
        BADGE_TONE[meta.tone],
        className,
      )}
    >
      <RatingIcon rating={rating} className={size === "sm" ? "size-3" : "size-3.5"} />
      {meta.label}
      {suffix !== undefined && <span className="font-mono tabular-nums">{suffix}</span>}
    </span>
  );
}
