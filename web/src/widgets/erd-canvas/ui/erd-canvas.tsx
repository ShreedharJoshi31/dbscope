import { LoaderCircle, Network } from "lucide-react";
import { lazy, Suspense } from "react";
import { getSchemaGraph } from "@/entities/schema";
import { openSample, SchemaLoadError, useWorkbenchSchema } from "@/features/load-schema";
import type { Model } from "@/shared/api";
import { useLayoutEpoch } from "@/shared/lib/layout-sync";
import { EmptyState } from "@/shared/ui/empty-state";
import { useLayoutStorage } from "@/widgets/erd-canvas/model/use-layout-storage";

// React Flow and the canvas load on first use, so onboarding and the shell stay small.
const CanvasView = lazy(() => import("@/widgets/erd-canvas/ui/canvas-view").then((m) => ({ default: m.CanvasView })));

function Loading({ label }: { label: string }) {
  return (
    <div role="status" className="flex h-full items-center justify-center gap-2 text-sm text-text-muted">
      <LoaderCircle aria-hidden className="size-4 animate-spin" />
      {label}
    </div>
  );
}

interface LoadedProps {
  model: Model;
  layoutKey: string;
  profileId: string | undefined;
  sample: boolean;
}

function Loaded({ model, layoutKey, profileId, sample }: LoadedProps) {
  const storage = useLayoutStorage(profileId, layoutKey, sample);
  const graph = getSchemaGraph(model);
  if (storage.restored === undefined) return <Loading label="Restoring the saved layout…" />;
  return (
    <Suspense fallback={<Loading label="Loading the canvas…" />}>
      <CanvasView
        graph={graph}
        layoutKey={layoutKey}
        restored={storage.restored}
        schedule={storage.schedule}
        profileId={sample ? undefined : profileId}
      />
    </Suspense>
  );
}

export function ErdCanvas() {
  const { source, profile, query, layoutKey } = useWorkbenchSchema();
  const epoch = useLayoutEpoch((s) => s.epoch);

  if (source === "profile" && !profile) {
    return (
      <EmptyState
        icon={Network}
        title="No connection selected"
        description="Add a connection to draw its tables, or explore the built-in sample schema without a database."
        action={{ label: "Open sample schema", onClick: openSample }}
      />
    );
  }
  if (query.isPending) return <Loading label="Reading schema…" />;
  if (query.isError) {
    return (
      <SchemaLoadError
        error={query.error}
        statementTimeoutMs={profile?.statementTimeoutMs}
        onRetry={() => void query.refetch()}
        retrying={query.isFetching}
        onOpenSample={source === "profile" ? openSample : undefined}
      />
    );
  }
  if ((query.data.relations ?? []).length === 0 || !layoutKey) {
    return (
      <EmptyState
        icon={Network}
        title="No tables in the selected schemas"
        description={`dbscope read ${profile?.database ?? "the database"} and found no tables, views or materialized views. Choose other schemas in the explorer, or open the sample schema to see the canvas.`}
        action={{ label: "Open sample schema", onClick: openSample }}
      />
    );
  }
  return (
    <Loaded
      // A new source, connection or schema selection is a new canvas with its own saved
      // layout; so is an imported team file (epoch), which replaced the saved one.
      key={`${source}:${profile?.id ?? ""}:${layoutKey}:${epoch}`}
      model={query.data}
      layoutKey={layoutKey}
      profileId={profile?.id}
      sample={source === "sample"}
    />
  );
}
