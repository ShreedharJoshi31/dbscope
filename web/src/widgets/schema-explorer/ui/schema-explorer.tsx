import { useQueryClient } from "@tanstack/react-query";
import { ListTree, LoaderCircle, PanelLeftClose, RefreshCw, Search } from "lucide-react";
import { useState } from "react";
import { getSchemaGraph } from "@/entities/schema";
import { openSample, SchemaLoadError, useWorkbenchSchema } from "@/features/load-schema";
import { SchemaPicker } from "@/features/select-schemas";
import { togglePanel } from "@/features/toggle-panel";
import { schemaQueryKeys } from "@/shared/api";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { announce } from "@/shared/lib/announcer";
import { formatGrouped } from "@/shared/lib/format-number";
import { formatAge, useNow } from "@/shared/lib/relative-time";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";
import { PanelHeader } from "@/shared/ui/panel-header";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";
import { SchemaTree } from "@/widgets/schema-explorer/ui/schema-tree";

function SnapshotInfo({
  introspectedAt,
  durationMs,
  tables,
}: {
  introspectedAt: string;
  durationMs: number;
  tables: number;
}) {
  const now = useNow();
  return (
    <p data-testid="snapshot-info" className="px-2 text-2xs text-text-muted">
      {formatGrouped(tables)} relations · read in <span className="font-mono">{formatGrouped(durationMs)} ms</span> ·
      snapshot {formatAge(introspectedAt, now)}
    </p>
  );
}

function RefreshButton({ onRefresh, busy }: { onRefresh: () => void; busy: boolean }) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="Read the schema again"
            disabled={busy}
            onClick={onRefresh}
          />
        }
      >
        <RefreshCw aria-hidden className={busy ? "animate-spin" : undefined} />
      </TooltipTrigger>
      <TooltipContent side="bottom">Read the schema again</TooltipContent>
    </Tooltip>
  );
}

export function SchemaExplorer() {
  const { source, profile, query, queryKey } = useWorkbenchSchema();
  const client = useQueryClient();
  const [filter, setFilter] = useState("");

  const refresh = () => {
    void client.refetchQueries({ queryKey }).then(() => announce("Schema read again."));
    if (profile) void client.invalidateQueries({ queryKey: schemaQueryKeys.namespaces(profile.id) });
  };

  const header = (
    <PanelHeader
      title="Schema explorer"
      close={{
        label: "Hide schema explorer",
        icon: PanelLeftClose,
        combo: SHORTCUTS.toggleExplorer.combo,
        onClick: () => togglePanel("explorer"),
      }}
    >
      {profile && <SchemaPicker profile={profile} />}
      {(profile || source === "sample") && <RefreshButton onRefresh={refresh} busy={query.isFetching} />}
    </PanelHeader>
  );

  if (source === "profile" && !profile) {
    return (
      <>
        {header}
        <EmptyState
          icon={ListTree}
          title="No connection"
          description="Schemas, tables and columns appear here once a connection is chosen."
          action={{ label: "Open sample schema", onClick: openSample }}
        />
      </>
    );
  }

  if (query.isPending) {
    return (
      <>
        {header}
        <p role="status" className="flex items-center gap-2 px-3 py-4 text-xs text-text-muted">
          <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
          Reading schema…
        </p>
      </>
    );
  }

  if (query.isError) {
    return (
      <>
        {header}
        <SchemaLoadError
          compact
          error={query.error}
          statementTimeoutMs={profile?.statementTimeoutMs}
          engine={profile?.engine}
          onRetry={refresh}
          retrying={query.isFetching}
        />
      </>
    );
  }

  const graph = getSchemaGraph(query.data);
  return (
    <>
      {header}
      <div className="flex shrink-0 flex-col gap-1 border-b border-border-default py-2">
        <label className="mx-2 flex h-7 items-center gap-1 rounded-md border border-border-control bg-surface-canvas px-2 focus-within:ring-2 focus-within:ring-border-focus">
          <Search aria-hidden className="size-3.5 shrink-0 text-text-muted" />
          <span className="sr-only">Filter tables and columns</span>
          <input
            type="search"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape" && filter) {
                e.preventDefault();
                setFilter("");
              }
            }}
            placeholder="Filter tables and columns"
            className="h-full min-w-0 flex-1 bg-transparent text-xs text-text-primary outline-none placeholder:text-text-muted"
          />
        </label>
        <SnapshotInfo
          introspectedAt={query.data.introspectedAt}
          durationMs={query.data.durationMs}
          tables={graph.relations.length}
        />
      </div>
      <SchemaTree key={graph.schemas.join(",")} graph={graph} filter={filter} />
    </>
  );
}
