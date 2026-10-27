import "@/shared/ui/sql/sql-code.css";
import {
  autocompletion,
  closeBrackets,
  closeBracketsKeymap,
  closeCompletion,
  completionKeymap,
} from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { PostgreSQL, sql } from "@codemirror/lang-sql";
import { bracketMatching, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { type Diagnostic, setDiagnostics } from "@codemirror/lint";
import { Compartment, EditorSelection, EditorState, Prec } from "@codemirror/state";
import {
  drawSelection,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
  placeholder,
} from "@codemirror/view";
import { useEffect, useRef } from "react";
import type { EditorDiagnostic, EditorReveal } from "@/entities/query";
import { formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { codeMirrorKey } from "@/shared/lib/hotkeys";
import { sqlHighlighter } from "@/shared/ui/sql/highlighter";
import type { SchemaCompletion } from "@/widgets/sql-editor/model/schema-completion";
import { tokenTheme } from "@/widgets/sql-editor/ui/editor-theme";

export interface CodeEditorProps {
  initialSql: string;
  label: string;
  schema: SchemaCompletion;
  diagnostic: EditorDiagnostic | null;
  reveal: EditorReveal | null;
  onChange: (sql: string) => void;
  onRun: () => void;
  onExplain: () => void;
  onCancel: () => void;
}

function sqlExtension(schema: SchemaCompletion) {
  return sql({
    dialect: PostgreSQL,
    schema: schema.namespace,
    ...(schema.defaultSchema ? { defaultSchema: schema.defaultSchema } : {}),
    upperCaseKeywords: true,
  });
}

function diagnostics(d: EditorDiagnostic | null, length: number): Diagnostic[] {
  if (!d || length === 0) return [];
  const from = Math.min(d.from, length);
  return [{ from, to: Math.min(Math.max(d.to, from + 1), length), severity: "error", message: d.message }];
}

/**
 * CodeMirror 6 with the PostgreSQL dialect and schema completion. Lazily
 * loaded; the doc belongs to the editor after mount (the tab store is fed
 * through onChange), so remount with a key to switch documents.
 */
export default function CodeEditor(props: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const language = useRef(new Compartment());
  const latest = useRef(props);
  latest.current = props;

  // biome-ignore lint/correctness/useExhaustiveDependencies: created once; later prop changes are applied by the effects below.
  useEffect(() => {
    if (!host.current) return;
    // Running from the keyboard also dismisses an open completion list.
    const run = (fn: () => void) => (view: EditorView) => {
      closeCompletion(view);
      fn();
      return true;
    };
    const shortcuts = Prec.highest(
      keymap.of([
        { key: codeMirrorKey(SHORTCUTS.runQuery.combo), run: run(() => latest.current.onRun()) },
        { key: codeMirrorKey(SHORTCUTS.explainQuery.combo), run: run(() => latest.current.onExplain()) },
        { key: codeMirrorKey(SHORTCUTS.cancelQuery.combo), run: run(() => latest.current.onCancel()) },
      ]),
    );
    const editor = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: props.initialSql,
        extensions: [
          shortcuts,
          lineNumbers(),
          highlightActiveLineGutter(),
          history(),
          drawSelection(),
          indentOnInput(),
          bracketMatching(),
          closeBrackets(),
          autocompletion({ icons: false }),
          highlightActiveLine(),
          // Tab is left to the browser so keyboard users can leave the editor (UX rule 11).
          keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap, ...completionKeymap]),
          language.current.of(sqlExtension(props.schema)),
          syntaxHighlighting(sqlHighlighter),
          tokenTheme,
          EditorView.lineWrapping,
          placeholder(`Write one SELECT, then run it with ${formatCombo(SHORTCUTS.runQuery.combo)}`),
          EditorView.contentAttributes.of({ "aria-label": props.label, "aria-multiline": "true" }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) latest.current.onChange(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = editor;
    return () => {
      editor.destroy();
      view.current = null;
    };
  }, []);

  useEffect(() => {
    view.current?.dispatch({ effects: language.current.reconfigure(sqlExtension(props.schema)) });
  }, [props.schema]);

  useEffect(() => {
    const v = view.current;
    if (!v) return;
    v.dispatch(setDiagnostics(v.state, diagnostics(props.diagnostic, v.state.doc.length)));
  }, [props.diagnostic]);

  useEffect(() => {
    const v = view.current;
    if (!v || !props.reveal) return;
    const length = v.state.doc.length;
    const from = Math.min(props.reveal.from, length);
    const to = Math.min(Math.max(props.reveal.to, from), length);
    v.dispatch({ selection: EditorSelection.range(from, to), scrollIntoView: true });
    v.focus();
  }, [props.reveal]);

  return <div ref={host} data-testid="sql-editor" className="h-full min-h-0 overflow-hidden" />;
}
