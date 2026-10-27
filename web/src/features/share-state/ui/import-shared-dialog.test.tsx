import { screen, waitFor, within } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { afterEach, describe, expect, it } from "vitest";
import { ImportSharedDialog, useImportSharedDialog } from "@/features/share-state";
import type { SharePreview } from "@/shared/api";
import { registerLayoutFlusher, useLayoutEpoch } from "@/shared/lib/layout-sync";
import { makeProfile, renderWithProviders } from "@/test/render";
import { server } from "@/test/server";

const teamFile = { kind: "dbscope-shared-state", formatVersion: 1, glossary: { mrr: { definition: "net" } } };
const previous = { kind: "dbscope-shared-state", formatVersion: 1, glossary: { mrr: { definition: "gross" } } };

function preview(mode: "merge" | "replace"): SharePreview {
  return {
    mode,
    schemaChecked: true,
    tables: {
      items: [{ layout: "public", key: "public.gone", change: "added", orphan: true }],
      counts: { added: 1 },
      orphans: 1,
    },
    foreignKeys: { items: [], counts: {}, orphans: 0 },
    glossary: {
      items: [
        { key: "mrr", change: "changed" },
        { key: "arr", change: mode === "merge" ? "kept" : "removed" },
      ],
      counts: mode === "merge" ? { changed: 1, kept: 1 } : { changed: 1, removed: 1 },
      orphans: 0,
    },
  };
}

function serve() {
  const imports: { mode: string | null; body: unknown }[] = [];
  const previews: (string | null)[] = [];
  server.use(
    http.get("/api/profiles", () => HttpResponse.json([makeProfile({ id: "p1", name: "orders-dev" })])),
    http.get("/api/profiles/p1/shared", () =>
      HttpResponse.text(JSON.stringify(previous), {
        headers: { "Content-Disposition": 'attachment; filename="orders-dev.dbscope.json"' },
      }),
    ),
    http.post("/api/profiles/p1/shared/preview", ({ request }) => {
      const mode = new URL(request.url).searchParams.get("mode");
      previews.push(mode);
      return HttpResponse.json(preview(mode === "replace" ? "replace" : "merge"));
    }),
    http.post("/api/profiles/p1/shared/import", async ({ request }) => {
      const mode = new URL(request.url).searchParams.get("mode");
      imports.push({ mode, body: await request.json() });
      return HttpResponse.json(preview(mode === "replace" ? "replace" : "merge"));
    }),
  );
  return { imports, previews };
}

afterEach(() => useImportSharedDialog.setState({ open: false }));

describe("ImportSharedDialog", () => {
  it("previews a team file, imports it after flushing canvas saves, and can undo", async () => {
    const { imports, previews } = serve();
    const order: string[] = [];
    const unregister = registerLayoutFlusher(async () => {
      order.push("flush");
    });
    const epoch = useLayoutEpoch.getState().epoch;
    useImportSharedDialog.setState({ open: true });
    const { user } = renderWithProviders(<ImportSharedDialog />);

    const file = new File([JSON.stringify(teamFile)], "orders.dbscope.json", { type: "application/json" });
    await user.upload(await screen.findByTestId("import-shared-input"), file);
    expect(await screen.findByText("1 changed · 1 kept")).toBeInTheDocument();
    expect(screen.getByText("1 not in this database")).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "Replace" }));
    expect(await screen.findByText("1 changed · 1 removed")).toBeInTheDocument();
    expect(previews).toEqual(["merge", "replace"]);

    await user.click(screen.getByRole("button", { name: "Replace with file" }));
    expect(
      await within(screen.getByRole("dialog")).findByText("Imported orders.dbscope.json into orders-dev."),
    ).toBeInTheDocument();
    expect(imports).toEqual([{ mode: "replace", body: teamFile }]);
    expect(order).toEqual(["flush"]);
    expect(useLayoutEpoch.getState().epoch).toBe(epoch + 1);

    await user.click(screen.getByRole("button", { name: "Undo import" }));
    await waitFor(() => expect(useImportSharedDialog.getState().open).toBe(false));
    expect(imports[1]).toEqual({ mode: "replace", body: previous });
    unregister();
  });

  it("explains a file that is not JSON without calling the agent", async () => {
    const { previews } = serve();
    useImportSharedDialog.setState({ open: true });
    const { user } = renderWithProviders(<ImportSharedDialog />);
    await user.upload(
      await screen.findByTestId("import-shared-input"),
      new File(["not json"], "notes.json", { type: "application/json" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("notes.json is not JSON");
    expect(previews).toEqual([]);
  });
});
