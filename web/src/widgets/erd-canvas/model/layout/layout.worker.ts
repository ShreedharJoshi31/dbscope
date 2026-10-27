import { forceLayout } from "@/widgets/erd-canvas/model/layout/force";
import { gridLayout } from "@/widgets/erd-canvas/model/layout/grid";
import type { SimpleLayoutRequest, SimpleLayoutResponse } from "@/widgets/erd-canvas/model/layout/types";

/** Module worker for the d3-force and grid layouts; ELK runs in its own classic worker. */
interface WorkerScope {
  onmessage: ((event: MessageEvent<SimpleLayoutRequest>) => void) | null;
  postMessage: (message: SimpleLayoutResponse) => void;
}

// The app compiles against the DOM lib; adding the WebWorker lib would clash with it.
const scope = self as unknown as WorkerScope;

scope.onmessage = (event: MessageEvent<SimpleLayoutRequest>) => {
  const { id, algorithm, nodes, links, groups } = event.data;
  let response: SimpleLayoutResponse;
  try {
    const positions = algorithm === "force" ? forceLayout(nodes, links) : gridLayout(nodes, links, groups);
    response = { id, positions };
  } catch (error) {
    response = { id, positions: {}, error: error instanceof Error ? error.message : String(error) };
  }
  scope.postMessage(response);
};
