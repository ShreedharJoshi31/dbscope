/**
 * PostgreSQL and the guard report error positions as 1-based character
 * (code point) offsets. JavaScript strings index UTF-16 code units, so an
 * emoji or other astral character before the error shifts the two apart.
 */

export interface TextRange {
  /** UTF-16 index of the first character, 0-based. */
  from: number;
  /** UTF-16 index after the highlighted token. */
  to: number;
  /** 1-based. */
  line: number;
  /** 1-based, in characters. */
  column: number;
}

/** UTF-16 index of the 1-based code point `position`, clamped to the text. */
export function codePointToIndex(text: string, position: number): number {
  let index = 0;
  let remaining = Math.max(0, Math.floor(position) - 1);
  while (remaining > 0 && index < text.length) {
    const code = text.codePointAt(index) ?? 0;
    index += code > 0xffff ? 2 : 1;
    remaining--;
  }
  return index;
}

const WORD = /[\p{L}\p{N}_$]/u;

/** The range a position points at: the identifier or literal starting there, or one character. */
export function positionToRange(text: string, position: number): TextRange | null {
  if (!Number.isFinite(position) || position < 1 || text.length === 0) return null;
  const from = Math.min(codePointToIndex(text, position), Math.max(0, text.length - 1));
  let to = from;
  const first = text[from] ?? "";
  if (first === "'" || first === '"') {
    const close = text.indexOf(first, from + 1);
    to = close === -1 ? text.length : close + 1;
  } else if (WORD.test(first)) {
    while (to < text.length && WORD.test(text[to] ?? "")) to++;
  } else {
    to = from + 1;
  }
  const before = text.slice(0, from);
  const line = before.split("\n").length;
  const lineStart = before.lastIndexOf("\n") + 1;
  const column = Array.from(text.slice(lineStart, from)).length + 1;
  return { from, to, line, column };
}
