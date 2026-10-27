import type { XYPosition } from "@xyflow/react";
import type { ELK } from "elkjs/lib/elk-api.js";
import { relationKey, type SchemaGraph } from "@/entities/schema";
import { layoutNodes } from "@/widgets/erd-canvas/model/elements";
import { toElkGraph } from "@/widgets/erd-canvas/model/layout/elk-graph";
import type {
  LayoutAlgorithm,
  LayoutLink,
  LayoutResult,
  SimpleLayoutRequest,
  SimpleLayoutResponse,
} from "@/widgets/erd-canvas/model/layout/types";

export class LayoutCancelled extends Error {
  override readonly name = "LayoutCancelled";
}

let elk: ELK | null = null;
let elkBusy: { reject: (e: Error) => void } | null = null;

/**
 * ELK runs in its own classic worker (elk-worker.min.js, loaded by URL), not
 * bundled into a module worker: the generated code takes over self.onmessage
 * and is not strict-mode safe (elkjs#141, #96). Loaded lazily so the ~1.5 MB
 * worker is fetched only when a layered layout is requested.
 */
async function getElk(): Promise<ELK> {
  if (elk) return elk;
  const [{ default: ElkConstructor }, { default: workerUrl }] = await Promise.all([
    import("elkjs/lib/elk-api.js"),
    import("elkjs/lib/elk-worker.min.js?url"),
  ]);
  elk = new ElkConstructor({ workerUrl });
  return elk;
}

/** Stops a running ELK layout; ELK cannot cancel, so its worker is terminated and recreated next time. */
function cancelElk(): void {
  if (!elkBusy) return;
  elkBusy.reject(new LayoutCancelled("layout replaced by a newer request"));
  elkBusy = null;
  elk?.terminateWorker();
  elk = null;
}

async function runElk(graph: SchemaGraph, collapsed: ReadonlySet<string>): Promise<Map<string, XYPosition>> {
  cancelElk();
  const instance = await getElk();
  const input = toElkGraph(graph, collapsed);
  const result = await new Promise<Awaited<ReturnType<ELK["layout"]>>>((resolve, reject) => {
    elkBusy = { reject };
    instance.layout(input).then(resolve, reject);
  });
  elkBusy = null;
  const positions = new Map<string, XYPosition>();
  for (const child of result.children ?? []) positions.set(child.id, { x: child.x ?? 0, y: child.y ?? 0 });
  return positions;
}

let worker: Worker | null = null;
let nextId = 1;
const pending = new Map<number, { resolve: (r: SimpleLayoutResponse) => void; reject: (e: Error) => void }>();

function getWorker(): Worker {
  if (worker) return worker;
  // Vite only bundles the worker when `new URL(...)` appears inline in `new Worker(...)`.
  worker = new Worker(new URL("./layout.worker.ts", import.meta.url), { type: "module", name: "dbscope-layout" });
  worker.onmessage = (event: MessageEvent<SimpleLayoutResponse>) => {
    const entry = pending.get(event.data.id);
    pending.delete(event.data.id);
    if (!entry) return;
    if (event.data.error) entry.reject(new Error(event.data.error));
    else entry.resolve(event.data);
  };
  worker.onerror = (event) => {
    for (const entry of pending.values()) entry.reject(new Error(event.message || "layout worker failed"));
    pending.clear();
    worker?.terminate();
    worker = null;
  };
  return worker;
}

function linksOf(graph: SchemaGraph): LayoutLink[] {
  return graph.edges.map((e) => ({ source: e.parent, target: e.child }));
}

async function runSimple(
  algorithm: "force" | "grid",
  graph: SchemaGraph,
  collapsed: ReadonlySet<string>,
): Promise<Map<string, XYPosition>> {
  // Older requests are superseded: settle them as cancelled so callers can ignore them.
  for (const entry of pending.values()) entry.reject(new LayoutCancelled("layout replaced by a newer request"));
  pending.clear();
  const groups: Record<string, string> = {};
  for (const r of graph.relations) groups[relationKey(r)] = r.schema;
  const request: SimpleLayoutRequest = {
    id: nextId++,
    algorithm,
    nodes: layoutNodes(graph, collapsed),
    links: linksOf(graph),
    groups,
  };
  const response = await new Promise<SimpleLayoutResponse>((resolve, reject) => {
    pending.set(request.id, { resolve, reject });
    getWorker().postMessage(request);
  });
  return new Map(Object.entries(response.positions));
}

/**
 * Runs a layout off the main thread and records a `dbscope:layout` performance
 * measure (algorithm, node count, duration) that the perf tests read.
 */
export async function runLayout(
  algorithm: LayoutAlgorithm,
  graph: SchemaGraph,
  collapsed: ReadonlySet<string>,
): Promise<LayoutResult> {
  if (algorithm !== "layered") cancelElk();
  const start = performance.now();
  const positions =
    algorithm === "layered" ? await runElk(graph, collapsed) : await runSimple(algorithm, graph, collapsed);
  const end = performance.now();
  try {
    performance.measure("dbscope:layout", {
      start,
      end,
      detail: { algorithm, nodes: graph.relations.length, edges: graph.edges.length },
    });
  } catch {
    // Measures are diagnostics only.
  }
  return { positions, ms: end - start, algorithm };
}
