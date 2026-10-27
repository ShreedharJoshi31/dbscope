/**
 * Word-level diff for short texts such as two versions of a generated SQL
 * statement. Longest common subsequence over whitespace-separated tokens;
 * whitespace is kept with the token before it so the output re-joins exactly.
 */

export interface DiffPart {
  type: "same" | "added" | "removed";
  text: string;
}

const MAX_TOKENS = 1500;

function tokenize(text: string): string[] {
  return text.match(/\S+\s*|\s+/g) ?? [];
}

function push(parts: DiffPart[], type: DiffPart["type"], text: string): void {
  const last = parts[parts.length - 1];
  if (last && last.type === type) last.text += text;
  else parts.push({ type, text });
}

export function diffWords(before: string, after: string): DiffPart[] {
  const a = tokenize(before);
  const b = tokenize(after);
  if (a.length > MAX_TOKENS || b.length > MAX_TOKENS) {
    return [
      { type: "removed", text: before },
      { type: "added", text: after },
    ];
  }
  const key = (t: string) => t.trimEnd();
  // lcs[i][j] = length of the LCS of a[i..] and b[j..]
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) {
    const row = lcs[i] as number[];
    const below = lcs[i + 1] as number[];
    for (let j = b.length - 1; j >= 0; j--) {
      row[j] =
        key(a[i] as string) === key(b[j] as string)
          ? (below[j + 1] as number) + 1
          : Math.max(below[j] as number, row[j + 1] as number);
    }
  }
  const parts: DiffPart[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    const ta = a[i] as string;
    const tb = b[j] as string;
    if (key(ta) === key(tb)) {
      push(parts, "same", tb);
      i++;
      j++;
    } else if ((lcs[i + 1]?.[j] ?? 0) >= (lcs[i]?.[j + 1] ?? 0)) {
      push(parts, "removed", ta);
      i++;
    } else {
      push(parts, "added", tb);
      j++;
    }
  }
  while (i < a.length) push(parts, "removed", a[i++] as string);
  while (j < b.length) push(parts, "added", b[j++] as string);
  return parts;
}

export function hasChanges(parts: readonly DiffPart[]): boolean {
  return parts.some((p) => p.type !== "same");
}
