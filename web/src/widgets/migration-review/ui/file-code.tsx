import { PostgreSQL, sql } from "@codemirror/lang-sql";
import { syntaxHighlighting } from "@codemirror/language";
import { EditorState, RangeSetBuilder, StateEffect, StateField } from "@codemirror/state";
import { Decoration, EditorView, GutterMarker, gutter, lineNumbers } from "@codemirror/view";
import { useEffect, useRef } from "react";
import { type ReviewStatement, ratingIconElement, ratingMeta } from "@/entities/migration-finding";
import { sqlHighlighter } from "@/shared/ui/sql/highlighter";

/** What the review asks of a file's view: scroll to a statement and focus its marker. */
export interface FileCodeHandle {
  reveal: (index: number, focusMarker: boolean) => void;
}

export interface FileCodeProps {
  text: string;
  label: string;
  statements: readonly ReviewStatement[];
  selected: number;
  onSelect: (index: number) => void;
  onOpenDetail: (index: number) => void;
  handleRef: (handle: FileCodeHandle | null) => void;
}

const setSelected = StateEffect.define<number>();

function markerLabel(s: ReviewStatement, others: number): string {
  const n = s.findings.length;
  const extra = others > 0 ? `, and ${others} more statement${others === 1 ? "" : "s"} on this line` : "";
  return `Line ${s.line}: ${ratingMeta(s.rating).label} risk, ${s.kind}, ${n} finding${n === 1 ? "" : "s"}${extra}`;
}

/** Rating icon + text in the gutter (UX rule 7); a roving tab stop on the selected statement. */
class RiskMarker extends GutterMarker {
  constructor(
    readonly statement: ReviewStatement,
    readonly selected: boolean,
    readonly others: number,
  ) {
    super();
  }

  override eq(other: GutterMarker): boolean {
    return (
      other instanceof RiskMarker &&
      other.statement === this.statement &&
      other.selected === this.selected &&
      other.others === this.others
    );
  }

  override toDOM(view: EditorView): Node {
    const s = this.statement;
    const el = view.dom.ownerDocument.createElement("div");
    el.className = "cm-risk-marker";
    el.setAttribute("role", "button");
    el.setAttribute("tabindex", this.selected ? "0" : "-1");
    el.setAttribute("aria-label", markerLabel(s, this.others));
    el.setAttribute("aria-current", this.selected ? "true" : "false");
    el.dataset.statement = String(s.index);
    el.dataset.rating = s.rating;
    if (this.selected) el.dataset.selected = "true";
    el.append(ratingIconElement(s.rating, view.dom.ownerDocument));
    const text = view.dom.ownerDocument.createElement("span");
    text.textContent = ratingMeta(s.rating).label + (this.others > 0 ? ` +${this.others}` : "");
    text.setAttribute("aria-hidden", "true");
    el.append(text);
    return el;
  }
}

class SpacerMarker extends GutterMarker {
  override toDOM(view: EditorView): Node {
    const el = view.dom.ownerDocument.createElement("div");
    el.className = "cm-risk-marker";
    el.style.visibility = "hidden";
    el.append(ratingIconElement("critical", view.dom.ownerDocument));
    const text = view.dom.ownerDocument.createElement("span");
    text.textContent = "Critical +9";
    el.append(text);
    return el;
  }
}

const selectedLine = Decoration.line({ class: "cm-statement-selected" });

const theme = EditorView.theme({
  "&": { color: "var(--text-primary)", backgroundColor: "var(--surface-canvas)", fontSize: "var(--font-size-13)" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.5", overflow: "visible" },
  ".cm-content": { padding: "4px 0", cursor: "pointer" },
  ".cm-gutters": {
    backgroundColor: "var(--surface-subtle)",
    color: "var(--text-muted)",
    borderRight: "1px solid var(--border-default)",
  },
  ".cm-risk-gutter .cm-gutterElement": { padding: "0 4px", display: "flex", alignItems: "flex-start" },
  ".cm-risk-marker": {
    display: "inline-flex",
    alignItems: "center",
    gap: "3px",
    height: "18px",
    marginTop: "1px",
    padding: "0 4px",
    borderRadius: "var(--radius-4)",
    border: "1px solid transparent",
    fontFamily: "var(--font-sans)",
    fontSize: "var(--font-size-11)",
    fontWeight: "600",
    color: "var(--text-primary)",
    cursor: "pointer",
    whiteSpace: "nowrap",
  },
  ".cm-risk-marker svg": { width: "12px", height: "12px", flexShrink: "0" },
  ".cm-risk-marker:hover": { backgroundColor: "var(--surface-hover)" },
  ".cm-risk-marker[data-selected]": {
    backgroundColor: "var(--surface-selected)",
    borderColor: "var(--accent-fg)",
  },
  ".cm-risk-marker:focus-visible": { outline: "2px solid var(--border-focus)", outlineOffset: "1px" },
  ".cm-statement-selected": {
    backgroundColor: "var(--surface-selected)",
    boxShadow: "inset 2px 0 0 var(--accent-fg)",
  },
});

/** A read-only migration file with line numbers and a rating marker per statement. Lazily loaded. */
export default function FileCode({
  text,
  label,
  statements,
  selected,
  onSelect,
  onOpenDetail,
  handleRef,
}: FileCodeProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const latest = useRef({ onSelect, onOpenDetail });
  latest.current = { onSelect, onOpenDetail };

  // biome-ignore lint/correctness/useExhaustiveDependencies: created once per file text; selection is applied by the effect below.
  useEffect(() => {
    if (!host.current) return;
    const byIndex = new Map(statements.map((s) => [s.index, s]));
    const startsAt = new Map<number, ReviewStatement[]>();
    for (const s of statements) startsAt.set(s.line, [...(startsAt.get(s.line) ?? []), s]);
    const atLine = (line: number) =>
      statements.find((s) => line >= s.line && line <= Math.max(s.endLine, s.line))?.index ?? null;

    const selectedField = StateField.define<number>({
      create: () => selected,
      update: (value, tr) => {
        for (const e of tr.effects) if (e.is(setSelected)) return e.value;
        return value;
      },
    });
    const selectedDecorations = EditorView.decorations.compute([selectedField], (state) => {
      const s = byIndex.get(state.field(selectedField));
      if (!s) return Decoration.none;
      const builder = new RangeSetBuilder<Decoration>();
      const last = Math.min(Math.max(s.endLine, s.line), state.doc.lines);
      for (let n = Math.max(1, s.line); n <= last; n++) {
        const line = state.doc.line(n);
        builder.add(line.from, line.from, selectedLine);
      }
      return builder.finish();
    });
    const riskGutter = gutter({
      class: "cm-risk-gutter",
      lineMarker: (v, block) => {
        const list = startsAt.get(v.state.doc.lineAt(block.from).number);
        if (!list || list.length === 0) return null;
        const current = v.state.field(selectedField);
        const shown = list.find((s) => s.index === current) ?? list.reduce((a, b) => (b.score > a.score ? b : a));
        return new RiskMarker(shown, shown.index === current, list.length - 1);
      },
      lineMarkerChange: (u) => u.transactions.some((tr) => tr.effects.some((e) => e.is(setSelected))),
      initialSpacer: () => new SpacerMarker(),
      domEventHandlers: {
        click: (_v, _block, event) => {
          const el = (event.target as Element | null)?.closest<HTMLElement>("[data-statement]");
          if (!el) return false;
          latest.current.onSelect(Number(el.dataset.statement));
          return true;
        },
        keydown: (_v, _block, event) => {
          const key = (event as KeyboardEvent).key;
          const el = (event.target as Element | null)?.closest<HTMLElement>("[data-statement]");
          if (!el || (key !== "Enter" && key !== " ")) return false;
          latest.current.onOpenDetail(Number(el.dataset.statement));
          return true;
        },
      },
    });

    const editor = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: text,
        extensions: [
          EditorState.readOnly.of(true),
          EditorView.editable.of(false),
          lineNumbers(),
          riskGutter,
          selectedField,
          selectedDecorations,
          sql({ dialect: PostgreSQL, upperCaseKeywords: true }),
          syntaxHighlighting(sqlHighlighter),
          theme,
          EditorView.lineWrapping,
          EditorView.contentAttributes.of({ "aria-label": label, "aria-readonly": "true" }),
          EditorView.domEventHandlers({
            mousedown: (event, v) => {
              const pos = v.posAtCoords({ x: event.clientX, y: event.clientY });
              if (pos === null) return false;
              const index = atLine(v.state.doc.lineAt(pos).number);
              if (index !== null) latest.current.onSelect(index);
              return false;
            },
          }),
        ],
      }),
    });
    view.current = editor;

    handleRef({
      reveal: (index, focusMarker) => {
        const s = byIndex.get(index);
        if (!s) return;
        const line = editor.state.doc.line(Math.min(Math.max(1, s.line), editor.state.doc.lines));
        editor.dispatch({ effects: EditorView.scrollIntoView(line.from, { y: "center" }) });
        if (!focusMarker) return;
        // The marker exists once the line is in CodeMirror's viewport; that can take a frame or two after scrolling.
        let tries = 0;
        const focus = () => {
          const el = editor.dom.querySelector<HTMLElement>(`.cm-risk-marker[data-statement="${index}"]`);
          if (el) el.focus({ preventScroll: true });
          else if (tries++ < 10) requestAnimationFrame(focus);
        };
        requestAnimationFrame(focus);
      },
    });
    return () => {
      handleRef(null);
      editor.destroy();
      view.current = null;
    };
  }, [text, statements]);

  useEffect(() => {
    const v = view.current;
    if (!v) return;
    // Changing the selection redraws the markers; keep keyboard focus on the selected one (roving tabindex).
    const hadFocus = v.dom.contains(v.dom.ownerDocument.activeElement);
    v.dispatch({ effects: setSelected.of(selected) });
    if (hadFocus) {
      v.dom.querySelector<HTMLElement>(`.cm-risk-marker[data-statement="${selected}"]`)?.focus({ preventScroll: true });
    }
  }, [selected]);

  return <div ref={host} data-testid="migration-file-code" className="min-w-0" />;
}
