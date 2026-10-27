import { History, LoaderCircle, MessageSquareText, PanelLeftClose, RefreshCw, SquareTerminal } from "lucide-react";
import type { ReactNode } from "react";
import { useActiveConnection } from "@/entities/connection";
import { openSqlTab } from "@/entities/query";
import { formatDuration } from "@/features/run-query";
import { toggleExplorerView } from "@/features/toggle-panel";
import { type HistoryEntry, type HistoryStatus, useQueryHistory } from "@/shared/api";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { announce } from "@/shared/lib/announcer";
import { formatGrouped } from "@/shared/lib/format-number";
import { formatAge, useNow } from "@/shared/lib/relative-time";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";
import { PanelHeader } from "@/shared/ui/panel-header";
import { StatusIcon, type StatusTone } from "@/shared/ui/status-icon";

const STATUS: Record<HistoryStatus, { tone: StatusTone; text: string }> = {
  ok: { tone: "success", text: "OK" },
  error: { tone: "danger", text: "Error" },
  rejected: { tone: "warn", text: "Rejected" },
  cancelled: { tone: "info", text: "Cancelled" },
};

const ERROR_CODE_TEXT: Record<string, string> = {
  cost_confirmation_required: "waited for cost confirmation",
  rows_confirmation_required: "waited for rows confirmation",
  cancelled: "cancelled",
  timeout: "timed out",
};

function HistoryItem({ entry, now }: { entry: HistoryEntry; now: number }) {
  const status = STATUS[entry.status] ?? STATUS.error;
  const Source = entry.source === "ask" ? MessageSquareText : SquareTerminal;
  const open = () => {
    openSqlTab(entry.sql);
    announce("Opened the SQL in a new tab.");
  };
  return (
    <li>
      <button
        type="button"
        data-testid="history-entry"
        onClick={open}
        title="Open this SQL in a new tab"
        className="flex w-full min-w-0 flex-col gap-0.5 border-b border-border-default px-2 py-1.5 text-left hover:bg-surface-hover focus-visible:ring-2 focus-visible:ring-border-focus focus-visible:ring-inset focus-visible:outline-none"
      >
        <span className="flex items-center gap-1 text-2xs text-text-muted">
          <StatusIcon tone={status.tone} className="size-3" />
          <span className="font-medium text-text-primary">{status.text}</span>
          {entry.errorCode && <span className="truncate">({ERROR_CODE_TEXT[entry.errorCode] ?? entry.errorCode})</span>}
          <span className="ml-auto flex shrink-0 items-center gap-1">
            <Source aria-hidden className="size-3" />
            {entry.source === "ask" ? "Assistant" : "Query"}
          </span>
        </span>
        {entry.question && <span className="line-clamp-2 text-xs text-text-primary">“{entry.question}”</span>}
        <span className="line-clamp-2 font-mono text-2xs break-all text-text-muted">{entry.sql}</span>
        <span className="flex gap-2 text-2xs text-text-muted">
          <span className="font-mono tabular-nums">{formatDuration(entry.durationMs)}</span>
          {entry.status === "ok" && (
            <span className="font-mono tabular-nums">{formatGrouped(entry.rowCount)} rows</span>
          )}
          <time className="ml-auto" dateTime={entry.startedAt}>
            {formatAge(entry.startedAt, now)}
          </time>
        </span>
      </button>
    </li>
  );
}

/** GET /history for the active connection; a click opens the SQL in a new editor tab. */
export function QueryHistory() {
  const { profile } = useActiveConnection();
  const history = useQueryHistory(profile?.id);
  const now = useNow();

  const header = (
    <PanelHeader
      title="Query history"
      close={{
        label: "Hide query history",
        icon: PanelLeftClose,
        combo: SHORTCUTS.toggleExplorer.combo,
        onClick: () => toggleExplorerView("history"),
      }}
    >
      {profile && (
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label="Refresh query history"
          disabled={history.isFetching}
          onClick={() => void history.refetch()}
        >
          <RefreshCw aria-hidden className={history.isFetching ? "animate-spin" : undefined} />
        </Button>
      )}
    </PanelHeader>
  );

  let body: ReactNode;
  if (!profile) {
    body = (
      <EmptyState
        icon={History}
        title="No connection"
        description="Queries you run and questions you ask on a connection are listed here, newest first."
      />
    );
  } else if (history.isPending) {
    body = (
      <p role="status" className="flex items-center gap-2 px-3 py-4 text-xs text-text-muted">
        <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
        Loading history…
      </p>
    );
  } else if (history.isError) {
    body = (
      <p role="alert" className="flex items-start gap-1 px-3 py-4 text-xs text-status-danger-fg">
        <StatusIcon tone="danger" className="size-3.5" />
        {history.error.message}
      </p>
    );
  } else if (history.data.length === 0) {
    body = (
      <EmptyState
        icon={History}
        title="No queries yet"
        description={`Queries and assistant questions on ${profile.name} appear here. dbscope stores the SQL and timing, never result rows.`}
      />
    );
  } else {
    body = (
      <ul aria-label={`Query history for ${profile.name}`} className="min-h-0 flex-1 overflow-y-auto">
        {history.data.map((entry) => (
          <HistoryItem key={entry.id} entry={entry} now={now} />
        ))}
      </ul>
    );
  }

  return (
    <>
      {header}
      {body}
    </>
  );
}
