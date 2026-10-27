import { formatCompact } from "@/shared/lib/format-number";

/** "1.2M rows" or "rows unknown" when the table was never analyzed. */
export function rowEstimateLabel(rowEstimate: number | null): string {
  if (rowEstimate === null || rowEstimate < 0) return "rows unknown";
  return `${formatCompact(rowEstimate)} ${rowEstimate === 1 ? "row" : "rows"}`;
}
