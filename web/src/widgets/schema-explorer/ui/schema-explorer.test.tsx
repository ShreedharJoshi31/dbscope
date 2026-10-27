import { act, screen, within } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { closeSampleWorkspace, openSampleWorkspace, useCanvasStore } from "@/entities/schema";
import { sampleModel } from "@/test/fixtures";
import { renderWithProviders } from "@/test/render";
import { server } from "@/test/server";
import { SchemaExplorer } from "@/widgets/schema-explorer";

beforeEach(() => {
  openSampleWorkspace();
  server.use(
    http.get("/api/profiles", () => HttpResponse.json([])),
    http.get("/api/sample/schema", () => HttpResponse.json(sampleModel())),
  );
});
afterEach(() => {
  closeSampleWorkspace();
  act(() => useCanvasStore.getState().select(null));
});

async function renderExplorer() {
  const view = renderWithProviders(<SchemaExplorer />);
  const tree = await screen.findByRole("tree", { name: "Schemas, tables and columns" });
  return { ...view, tree };
}

describe("SchemaExplorer", () => {
  it("lists schemas, relations with kind and row estimate, and the snapshot age", async () => {
    const { tree } = await renderExplorer();
    expect(within(tree).getByRole("treeitem", { name: "Schema app, 15 relations" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    expect(within(tree).getByRole("treeitem", { name: "audit_log, partitioned table, 3.7M rows" })).toBeInTheDocument();
    expect(within(tree).getByRole("treeitem", { name: "feature_flags, table, rows unknown" })).toBeInTheDocument();
    expect(within(tree).getByRole("treeitem", { name: "countries, outside the selected schemas" })).toBeInTheDocument();
    expect(screen.getByTestId("snapshot-info")).toHaveTextContent(/42 relations · read in 214 ms · snapshot .* ago/);
  });

  it("filters by table and column name, expanding the way to column matches", async () => {
    const { user, tree } = await renderExplorer();
    await user.type(screen.getByPlaceholderText("Filter tables and columns"), "tracking");
    const items = within(tree)
      .getAllByRole("treeitem")
      .map((el) => el.getAttribute("aria-label"));
    expect(items).toEqual([
      "Schema shop, 15 relations",
      "shipments, table, 1.1M rows",
      "tracking_number, text, nullable",
    ]);
    await user.clear(screen.getByPlaceholderText("Filter tables and columns"));
    await user.type(screen.getByPlaceholderText("Filter tables and columns"), "zzz");
    expect(await screen.findByText("No tables or columns match “zzz”.")).toBeInTheDocument();
  });

  it("is one tab stop with arrow-key navigation, and Enter selects on the canvas", async () => {
    const { user, tree } = await renderExplorer();
    const schemaApp = within(tree).getByRole("treeitem", { name: "Schema app, 15 relations" });
    expect(
      within(tree)
        .getAllByRole("treeitem")
        .filter((el) => el.tabIndex === 0),
    ).toEqual([schemaApp]);

    schemaApp.focus();
    await user.keyboard("{ArrowLeft}");
    expect(schemaApp).toHaveAttribute("aria-expanded", "false");
    await user.keyboard("{ArrowRight}{ArrowDown}");
    const apiKeys = within(tree).getByRole("treeitem", { name: /^api_keys/ });
    expect(apiKeys).toHaveFocus();
    expect(apiKeys).toHaveAttribute("tabindex", "0");
    expect(schemaApp).toHaveAttribute("tabindex", "-1");

    await user.keyboard("{Enter}");
    expect(useCanvasStore.getState().selectedId).toBe("app.api_keys");
    expect(useCanvasStore.getState().reveal).toMatchObject({ id: "app.api_keys" });
    expect(apiKeys).toHaveAttribute("aria-selected", "true");

    await user.keyboard("{ArrowRight}{ArrowRight}");
    const idColumn = within(tree).getByRole("treeitem", { name: "id, uuid, primary key" });
    expect(idColumn).toHaveFocus();
    await user.keyboard("{ArrowDown}{Enter}");
    expect(useCanvasStore.getState().reveal).toMatchObject({ id: "app.api_keys", column: "team_id" });
    await user.keyboard("{ArrowLeft}");
    expect(apiKeys).toHaveFocus();
    await user.keyboard("{End}");
    expect(within(tree).getByRole("treeitem", { name: /^countries/ })).toHaveFocus();
    await user.keyboard("{Home}");
    expect(schemaApp).toHaveFocus();
  });
});
