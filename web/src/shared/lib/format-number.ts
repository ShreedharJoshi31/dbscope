const compact = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });
const grouped = new Intl.NumberFormat("en");

/** 1234567 → "1.2M", 950 → "950". */
export function formatCompact(value: number): string {
  return compact.format(value);
}

/** 1234567 → "1,234,567". */
export function formatGrouped(value: number): string {
  return grouped.format(value);
}
