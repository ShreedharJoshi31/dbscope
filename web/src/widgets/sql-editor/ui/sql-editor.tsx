import { LoaderCircle, Play, ScanText, Square } from "lucide-react";
import { type ComponentProps, lazy, Suspense, useId, useMemo } from "react";
import { useActiveConnection } from "@/entities/connection";
import { editorDiagnostic, type SqlTab, useRunStore, useSqlTabs } from "@/entities/query";
import { useWorkbenchSchema } from "@/features/load-schema";
import { MAX_ROWS_CHOICES, useQueryRunner, useTabRunOption, useTabRunOptions } from "@/features/run-query";
import { ariaKeyShortcut, type KeyCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { Button } from "@/shared/ui/button";
import { Checkbox } from "@/shared/ui/checkbox";
import { NativeSelect } from "@/shared/ui/native-select";
import { ShortcutKeys } from "@/shared/ui/shortcut-keys";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";
import { exampleQuery, languageFor, namesOf } from "@/widgets/sql-editor/model/languages";
import { schemaCompletion } from "@/widgets/sql-editor/model/schema-completion";
import { RunStatus } from "@/widgets/sql-editor/ui/run-status";

// CodeMirror loads on first use, like React Flow, so the shell bundle stays small.
const CodeEditor = lazy(() => import("@/widgets/sql-editor/ui/code-editor"));

function ToolbarButton({ label, combo, ...props }: { label: string; combo: KeyCombo } & ComponentProps<typeof Button>) {
  return (
    <Tooltip>
      <TooltipTrigger render={<Button size="sm" aria-keyshortcuts={ariaKeyShortcut(combo)} {...props} />} />
      <TooltipContent side="bottom" className="gap-2">
        {label} <ShortcutKeys combo={combo} />
      </TooltipContent>
    </Tooltip>
  );
}

function useDiagnostic(tab: SqlTab) {
  const lastError = useRunStore((s) => s.lastError);
  const messages = useRunStore((s) => s.messages);
  return useMemo(() => {
    if (!lastError || lastError.meta.tabId !== tab.id) return null;
    const executed = messages.find((m) => m.meta.id === lastError.meta.id)?.executedSql;
    return editorDiagnostic(lastError.error, lastError.meta.sql, tab.sql, executed);
  }, [lastError, messages, tab.id, tab.sql]);
}

export function SqlEditor({ tab }: { tab: SqlTab }) {
  const { profile } = useActiveConnection();
  const { source, query } = useWorkbenchSchema();
  const runner = useQueryRunner();
  const running = useRunStore((s) => s.running);
  const { analyze, maxRows } = useTabRunOption(tab.id);
  const reveal = useSqlTabs((s) => (s.reveal?.tabId === tab.id ? s.reveal : null));
  const schema = useMemo(() => schemaCompletion(query.data), [query.data]);
  const language = languageFor(source === "profile" ? profile?.engine : undefined);
  const names = useMemo(() => namesOf(query.data), [query.data]);
  const example = useMemo(() => exampleQuery(language, query.data), [language, query.data]);
  const diagnostic = useDiagnostic(tab);
  const analyzeId = useId();
  const rowsId = useId();
  const runnable = source === "profile" && Boolean(profile);
  const busy = running !== null;

  const run = (kind: "query" | "explain") => {
    if (profile && runnable) {
      runner.request({
        profile,
        sql: tab.sql,
        kind,
        analyze: analyze && profile.engine === "postgres",
        maxRows,
        tabId: tab.id,
      });
    } else runner.runActive(undefined, kind);
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div
        role="toolbar"
        aria-label={`${tab.title} tools`}
        className="flex shrink-0 items-center gap-2 border-b border-border-default bg-surface-subtle px-2 py-1"
      >
        <ToolbarButton
          label="Run the query"
          combo={SHORTCUTS.runQuery.combo}
          onClick={() => run("query")}
          disabled={!runnable || !tab.sql.trim()}
        >
          <Play aria-hidden />
          Run
        </ToolbarButton>
        <ToolbarButton
          label="Show the plan without running the query"
          combo={SHORTCUTS.explainQuery.combo}
          variant="outline"
          onClick={() => run("explain")}
          disabled={!runnable || !tab.sql.trim()}
        >
          <ScanText aria-hidden />
          Explain
        </ToolbarButton>
        {/* EXPLAIN ANALYZE is PostgreSQL's. */}
        {profile?.engine === "postgres" && (
          <label htmlFor={analyzeId} className="flex items-center gap-1.5 text-xs text-text-muted">
            <Checkbox
              id={analyzeId}
              checked={analyze}
              onCheckedChange={(on) => useTabRunOptions.getState().set(tab.id, { analyze: on })}
            />
            with ANALYZE
            <span className="sr-only">(executes the query to measure it)</span>
          </label>
        )}
        <label htmlFor={rowsId} className="flex items-center gap-1 text-xs text-text-muted">
          Max rows
          <NativeSelect
            id={rowsId}
            value={String(maxRows)}
            onChange={(e) => useTabRunOptions.getState().set(tab.id, { maxRows: Number(e.target.value) })}
            className="h-7 w-auto px-1 font-mono text-xs"
          >
            {MAX_ROWS_CHOICES.map((n) => (
              <option key={n} value={n}>
                {n.toLocaleString("en")}
              </option>
            ))}
          </NativeSelect>
        </label>
        {busy && (
          <ToolbarButton
            label="Cancel the running query"
            combo={SHORTCUTS.cancelQuery.combo}
            variant="outline"
            onClick={runner.cancel}
          >
            <Square aria-hidden />
            Cancel
          </ToolbarButton>
        )}
        <div className="ml-auto flex items-center gap-2">
          <RunStatus tabId={tab.id} />
        </div>
      </div>
      {!runnable && (
        <p className="shrink-0 border-b border-border-default bg-surface-subtle px-3 py-1 text-xs text-text-muted">
          {source === "sample"
            ? "The sample schema has no database behind it. Add a connection to run queries; completion still uses the sample tables."
            : "Choose a connection to run queries."}
        </p>
      )}
      <div className="min-h-0 flex-1">
        <Suspense
          fallback={
            <p role="status" className="flex items-center gap-2 p-3 text-sm text-text-muted">
              <LoaderCircle aria-hidden className="size-4 animate-spin" />
              Loading the editor…
            </p>
          }
        >
          <CodeEditor
            initialSql={tab.sql}
            label={`${language === "cypher" || language === "json" || language === "json-or-sql" ? "Query" : "SQL"} editor, ${tab.title}`}
            language={language}
            schema={schema}
            names={names}
            example={example}
            diagnostic={diagnostic}
            reveal={reveal}
            onChange={(sql) => useSqlTabs.getState().setSql(tab.id, sql)}
            onRun={() => run("query")}
            onExplain={() => run("explain")}
            onCancel={runner.cancel}
          />
        </Suspense>
      </div>
    </div>
  );
}
