import "@/shared/ui/sql/sql-code.css";
import {
  autocompletion,
  type CompletionSource,
  closeBrackets,
  closeBracketsKeymap,
  closeCompletion,
  completionKeymap,
} from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { json } from "@codemirror/lang-json";
import { Cassandra, MySQL, PostgreSQL, SQLDialect, SQLite, StandardSQL, sql } from "@codemirror/lang-sql";
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
import { type EditorLanguage, looksLikeJson, type Names } from "@/widgets/sql-editor/model/languages";
import type { SchemaCompletion } from "@/widgets/sql-editor/model/schema-completion";
import { tokenTheme } from "@/widgets/sql-editor/ui/editor-theme";

export interface CodeEditorProps {
  initialSql: string;
  label: string;
  language: EditorLanguage;
  schema: SchemaCompletion;
  names: Names;
  /** An example query, shown while the tab is empty. */
  example: string;
  diagnostic: EditorDiagnostic | null;
  reveal: EditorReveal | null;
  onChange: (sql: string) => void;
  onRun: () => void;
  onExplain: () => void;
  onCancel: () => void;
}

// ClickHouse: standard SQL plus its own clauses.
const ClickHouse = SQLDialect.define({
  ...StandardSQL.spec,
  keywords: `${StandardSQL.spec.keywords ?? ""} final prewhere sample array settings format global any asof semi anti ilike`,
  identifierQuotes: '`"',
  hashComments: true,
});

// Cypher's read clauses only: dbscope runs no writing Cypher, so CREATE, MERGE, SET,
// DELETE and FOREACH are left out of completion. Backquoted names, // comments.
const Cypher = SQLDialect.define({
  keywords:
    "match optional where return with unwind order by skip limit call yield as distinct and or xor not in is null true false case when then else end exists count collect starts ends contains asc desc ascending descending union all shortestpath allshortestpaths",
  builtin:
    "id labels type keys properties size length nodes relationships coalesce tostring tointeger tofloat date datetime duration point",
  types: "",
  identifierQuotes: "`",
  slashComments: true,
  doubleQuotedStrings: true,
});

const SQL_DIALECTS = {
  postgres: PostgreSQL,
  mysql: MySQL,
  sqlite: SQLite,
  clickhouse: ClickHouse,
  partiql: StandardSQL,
  cql: Cassandra,
} as const;

/** Completes names while typing: after ":" in Cypher, inside a JSON string, or any word. */
function nameCompletion(names: Names, mode: "cypher" | "json"): CompletionSource {
  return (context) => {
    if (mode === "json") {
      const m = context.matchBefore(/"[\w.@*-]*/);
      if (!m || (m.from === m.to && !context.explicit)) return null;
      const options = [
        ...names.relations.map((label) => ({ label, type: "type" })),
        ...names.fields.map((label) => ({ label, type: "property" })),
      ];
      return { from: m.from + 1, options, validFor: /^[\w.@*-]*$/ };
    }
    const label = context.matchBefore(/:\s*`?[\w]*/);
    if (label) {
      return {
        from: label.to - (label.text.match(/[\w]*$/)?.[0].length ?? 0),
        options: names.relations.map((n) => ({ label: n, type: "type" })),
      };
    }
    const prop = context.matchBefore(/\.[\w]*/);
    if (prop) return { from: prop.from + 1, options: names.fields.map((n) => ({ label: n, type: "property" })) };
    return null;
  };
}

function languageExtension(props: Pick<CodeEditorProps, "language" | "schema" | "names" | "example">, doc: string) {
  const hint = placeholder(`${props.example}    (${formatCombo(SHORTCUTS.runQuery.combo)} runs it)`);
  const lang = props.language === "json-or-sql" ? (looksLikeJson(doc) ? "json" : "partiql") : props.language;
  // Build each source once: completion tracks sources by identity.
  if (lang === "json") {
    const data = [{ autocomplete: nameCompletion(props.names, "json") }];
    return [json(), EditorState.languageData.of(() => data), hint];
  }
  if (lang === "cypher") {
    const data = [{ autocomplete: nameCompletion(props.names, "cypher") }];
    return [sql({ dialect: Cypher, upperCaseKeywords: true }), EditorState.languageData.of(() => data), hint];
  }
  return [
    sql({
      dialect: SQL_DIALECTS[lang],
      schema: props.schema.namespace,
      ...(props.schema.defaultSchema ? { defaultSchema: props.schema.defaultSchema } : {}),
      upperCaseKeywords: true,
    }),
    hint,
  ];
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
  const jsonMode = useRef(looksLikeJson(props.initialSql));
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
          language.current.of(languageExtension(props, props.initialSql)),
          syntaxHighlighting(sqlHighlighter),
          tokenTheme,
          EditorView.lineWrapping,
          EditorView.contentAttributes.of({ "aria-label": props.label, "aria-multiline": "true" }),
          EditorView.updateListener.of((u) => {
            if (!u.docChanged) return;
            const doc = u.state.doc.toString();
            latest.current.onChange(doc);
            // Elasticsearch takes a search request as JSON or SQL: follow the text.
            if (latest.current.language === "json-or-sql" && looksLikeJson(doc) !== jsonMode.current) {
              jsonMode.current = looksLikeJson(doc);
              u.view.dispatch({ effects: language.current.reconfigure(languageExtension(latest.current, doc)) });
            }
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

  const { language: lang, schema, names, example } = props;
  useEffect(() => {
    const v = view.current;
    if (!v) return;
    const config = { language: lang, schema, names, example };
    v.dispatch({ effects: language.current.reconfigure(languageExtension(config, v.state.doc.toString())) });
  }, [lang, schema, names, example]);

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
