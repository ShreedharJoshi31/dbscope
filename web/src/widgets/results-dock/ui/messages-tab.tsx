import { CornerDownLeft, MessageSquareText, Trash2 } from "lucide-react";
import { useActiveConnection } from "@/entities/connection";
import {
  describeQueryError,
  positionToRange,
  QueryErrorView,
  type RunMessage,
  useRunStore,
  useSqlTabs,
} from "@/entities/query";
import { useQueryRunner } from "@/features/run-query";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";
import { StatusIcon } from "@/shared/ui/status-icon";

const LEVEL = {
  error: { tone: "danger", text: "Error" },
  warning: { tone: "warn", text: "Warning" },
  info: { tone: "info", text: "Note" },
} as const;

const time = new Intl.DateTimeFormat("en", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });

function origin(m: RunMessage): string {
  if (m.meta.source === "assistant") return "Assistant answer";
  const tab = useSqlTabs.getState().tabs.find((t) => t.id === m.meta.tabId);
  const kind = m.meta.kind === "explain" ? (m.meta.analyze ? "Explain analyze" : "Explain") : "Query";
  return tab ? `${kind} · ${tab.title}` : kind;
}

function ShowInEditor({ message }: { message: RunMessage }) {
  const exists = useSqlTabs((s) => s.tabs.some((t) => t.id === message.meta.tabId));
  const error = message.error;
  if (!error || !message.meta.tabId || !exists) return null;
  const copy = describeQueryError(error);
  const range = copy.position?.target === "input" ? positionToRange(message.meta.sql, copy.position.value) : null;
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={() =>
        message.meta.tabId && useSqlTabs.getState().revealInEditor(message.meta.tabId, range?.from, range?.to)
      }
    >
      <CornerDownLeft aria-hidden />
      {range ? `Show line ${range.line}, column ${range.column} in the editor` : "Show in the editor"}
    </Button>
  );
}

function MessageItem({ message }: { message: RunMessage }) {
  const { profile } = useActiveConnection();
  const runner = useQueryRunner();
  const level = LEVEL[message.level];
  const retry =
    profile && message.meta.source === "editor"
      ? () =>
          runner.request({
            profile,
            sql: message.meta.sql,
            kind: message.meta.kind,
            analyze: message.meta.analyze,
            tabId: message.meta.tabId,
          })
      : undefined;

  return (
    <li
      data-testid="run-message"
      data-level={message.level}
      className="flex flex-col gap-2 border-b border-border-default px-3 py-2"
    >
      <p className="flex items-center gap-1.5 text-xs text-text-muted">
        <StatusIcon tone={level.tone} className="size-3.5" />
        <span className="font-medium text-text-primary">{level.text}</span>
        <span>·</span>
        <span>{origin(message)}</span>
        <span>·</span>
        <time className="font-mono tabular-nums" dateTime={new Date(message.at).toISOString()}>
          {time.format(message.at)}
        </time>
      </p>
      {message.error ? (
        <QueryErrorView
          error={message.error}
          submittedSql={message.meta.sql}
          executedSql={message.executedSql}
          statementTimeoutMs={profile?.statementTimeoutMs}
          onRetry={retry}
        >
          <ShowInEditor message={message} />
        </QueryErrorView>
      ) : (
        <p className="text-sm break-words">{message.text}</p>
      )}
    </li>
  );
}

export function MessagesTab() {
  const messages = useRunStore((s) => s.messages);
  if (messages.length === 0) {
    return (
      <EmptyState
        icon={MessageSquareText}
        title="No messages"
        description="Safety-check rejections, database errors with SQLSTATE and position, and warnings from your queries appear here."
      />
    );
  }
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center border-b border-border-default px-2 py-1">
        <span className="text-xs text-text-muted">Newest first</span>
        <Button variant="ghost" size="xs" className="ml-auto" onClick={() => useRunStore.getState().clearMessages()}>
          <Trash2 aria-hidden />
          Clear messages
        </Button>
      </div>
      <ul aria-label="Messages" className="min-h-0 flex-1 overflow-y-auto">
        {messages.map((m) => (
          <MessageItem key={m.id} message={m} />
        ))}
      </ul>
    </div>
  );
}
