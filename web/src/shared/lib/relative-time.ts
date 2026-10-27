import { useEffect, useState } from "react";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

function plural(n: number, unit: string): string {
  return `${n} ${unit}${n === 1 ? "" : "s"}`;
}

/** "just now", "1 min ago", "3 h ago", "2 days ago". Future times (clock skew) read as "just now". */
export function formatAge(from: Date | string | number, now: number = Date.now()): string {
  const then = new Date(from).getTime();
  if (Number.isNaN(then)) return "at an unknown time";
  const elapsed = now - then;
  if (elapsed < MINUTE) return "just now";
  if (elapsed < HOUR) return `${Math.floor(elapsed / MINUTE)} min ago`;
  if (elapsed < DAY) return `${Math.floor(elapsed / HOUR)} h ago`;
  return `${plural(Math.floor(elapsed / DAY), "day")} ago`;
}

/** The current time, refreshed every `intervalMs` so relative labels stay true. */
export function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(timer);
  }, [intervalMs]);
  return now;
}
