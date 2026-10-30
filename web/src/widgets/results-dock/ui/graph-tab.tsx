import { LoaderCircle, Share2 } from "lucide-react";
import { lazy, Suspense } from "react";
import { useActiveConnection } from "@/entities/connection";
import { useRunStore } from "@/entities/query";
import { EmptyState } from "@/shared/ui/empty-state";

// React Flow loads with the first drawn graph, as on the canvas.
const GraphView = lazy(() => import("@/widgets/results-dock/ui/graph-view"));

export function GraphTab() {
  const output = useRunStore((s) => s.output);
  const { profile } = useActiveConnection();
  const graph = output?.result?.graph;
  if (!output || !graph || !profile) {
    return (
      <EmptyState
        icon={Share2}
        title="No graph to draw"
        description="Return nodes, relationships or paths, such as MATCH p = (a)-[*1..2]-(b) RETURN p LIMIT 25, to draw them here. Other values stay on Results."
      />
    );
  }
  return (
    <Suspense
      fallback={
        <p role="status" className="flex items-center gap-2 p-3 text-xs text-text-muted">
          <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
          Loading the graph view…
        </p>
      }
    >
      {/* A new run starts a new drawing; expansions belong to the result they grew from. */}
      <GraphView
        key={output.meta.id}
        initial={graph}
        profileId={profile.id}
        // Graph databases expand any node; PostgreSQL expands vertices of the Apache AGE graph the result read.
        canExpand={Boolean(profile.capabilities.graph || graph.source)}
      />
    </Suspense>
  );
}
