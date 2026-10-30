import { describe, expect, it } from "vitest";
import type { GraphNode, ResultGraph } from "@/shared/api";
import { layoutGraph, type Point } from "@/widgets/results-dock/model/graph-layout";
import { caption, drawnDegrees, labelColors, mergeGraph } from "@/widgets/results-dock/model/graph-view";

const node = (id: string, props: GraphNode["props"] = {}, labels = ["Person"]): GraphNode => ({ id, labels, props });
const rel = (id: string, start: string, end: string) => ({ id, type: "KNOWS", start, end, props: {} });

describe("mergeGraph", () => {
  it("adds new elements once and replaces stubs with full nodes", () => {
    const base: ResultGraph = {
      nodes: [node("a"), { id: "b", labels: [], props: {}, stub: true }],
      relationships: [rel("r1", "a", "b")],
      truncated: false,
    };
    const merged = mergeGraph(base, {
      nodes: [node("b", { name: "Bea" }), node("a", { name: "changed" }), node("c")],
      relationships: [rel("r1", "a", "b"), rel("r2", "b", "c")],
      truncated: true,
    });
    expect(merged.nodes.map((n) => n.id)).toEqual(["a", "b", "c"]);
    expect(merged.nodes[0]?.props).toEqual({});
    expect(merged.nodes[1]).toMatchObject({ props: { name: "Bea" } });
    expect(merged.nodes[1]?.stub).toBeUndefined();
    expect(merged.relationships.map((r) => r.id)).toEqual(["r1", "r2"]);
    expect(merged.truncated).toBe(true);
  });
});

describe("caption", () => {
  it("prefers naming properties, then any scalar, then the label", () => {
    expect(caption(node("1", { age: 3, email: "a@x", name: "Ann" }))).toBe("Ann");
    expect(caption(node("1", { age: 3, email: "a@x" }))).toBe("a@x");
    expect(caption(node("1", { tags: ["x"], age: 3 }))).toBe("3");
    expect(caption(node("1", { tags: ["x"] }))).toBe("Person");
    expect(caption({ id: "1", labels: [], props: {}, stub: true })).toBe("not returned");
  });
});

describe("labelColors and drawnDegrees", () => {
  it("gives labels colour slots in order and counts drawn relationships per node", () => {
    const colors = labelColors([node("1"), node("2", {}, ["Company"]), node("3")]);
    expect([...colors]).toEqual([
      ["Person", 1],
      ["Company", 2],
    ]);
    const degrees = drawnDegrees([rel("r1", "a", "b"), rel("r2", "a", "c"), rel("r3", "a", "a")]);
    expect(degrees.get("a")).toBe(3);
    expect(degrees.get("b")).toBe(1);
  });
});

function distance(a: Point | undefined, b: Point | undefined): number {
  if (!a || !b) throw new Error("a node was not placed");
  return Math.hypot(a.x - b.x, a.y - b.y);
}

describe("layoutGraph", () => {
  const graph: ResultGraph = {
    nodes: [node("a"), node("b"), node("c"), node("d")],
    relationships: [rel("r1", "a", "b"), rel("r2", "b", "c"), rel("r3", "c", "a")],
    truncated: false,
  };

  it("places every node apart from the others", () => {
    const at = layoutGraph(graph);
    const ids = Object.keys(at);
    expect(ids.sort()).toEqual(["a", "b", "c", "d"]);
    for (const p of ids) {
      for (const q of ids) {
        if (p < q) expect(distance(at[p], at[q])).toBeGreaterThan(60);
      }
    }
  });

  it("keeps placed nodes still and puts new ones near their neighbour", () => {
    const placed = { a: { x: 1000, y: 1000 }, b: { x: 1200, y: 1000 } };
    const at = layoutGraph(
      { ...graph, nodes: [...graph.nodes, node("e")], relationships: [...graph.relationships, rel("r4", "a", "e")] },
      placed,
    );
    expect(at.a).toEqual(placed.a);
    expect(at.b).toEqual(placed.b);
    expect(distance(at.e, placed.a)).toBeLessThan(400);
  });
});
