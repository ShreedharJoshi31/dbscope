import { EditorView } from "@codemirror/view";

/**
 * Editor chrome from the design tokens only. The tokens are light-dark()
 * pairs, so one theme serves both colour schemes; syntax colours come from
 * the class highlighter (shared/ui/sql).
 */
export const tokenTheme = EditorView.theme({
  "&": {
    height: "100%",
    color: "var(--text-primary)",
    backgroundColor: "var(--surface-canvas)",
    fontSize: "var(--font-size-14)",
  },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.5" },
  ".cm-content": { caretColor: "var(--text-primary)", padding: "8px 0" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--text-primary)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, ::selection": {
    backgroundColor: "var(--surface-selected)",
  },
  ".cm-gutters": {
    backgroundColor: "var(--surface-subtle)",
    color: "var(--text-muted)",
    borderRight: "1px solid var(--border-default)",
  },
  ".cm-activeLine": { backgroundColor: "var(--surface-hover)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--surface-hover)", color: "var(--text-primary)" },
  ".cm-matchingBracket": { backgroundColor: "var(--surface-selected)", outline: "1px solid var(--border-control)" },
  ".cm-placeholder": { color: "var(--text-muted)" },
  ".cm-tooltip": {
    backgroundColor: "var(--surface-overlay)",
    color: "var(--text-primary)",
    border: "1px solid var(--border-default)",
    borderRadius: "var(--radius-6)",
  },
  ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
    backgroundColor: "var(--surface-selected)",
    color: "var(--text-primary)",
  },
  ".cm-completionDetail": { color: "var(--text-muted)", fontStyle: "normal", marginLeft: "8px" },
  ".cm-completionMatchedText": { textDecoration: "none", fontWeight: "600", color: "var(--accent-fg)" },
  ".cm-lintRange-error": {
    backgroundImage: "none",
    textDecoration: "underline wavy var(--status-danger-fg)",
    textUnderlineOffset: "3px",
    backgroundColor: "var(--status-danger-subtle)",
  },
  ".cm-diagnostic-error": { borderLeftColor: "var(--status-danger-fg)" },
  ".cm-lint-marker-error": { content: "none" },
  ".cm-tooltip-lint": { padding: "4px 0" },
  ".cm-panels": { backgroundColor: "var(--surface-subtle)", color: "var(--text-primary)" },
});
