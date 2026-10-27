import { screen, waitFor, within } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { afterEach, describe, expect, it } from "vitest";
import type { DraftEntry } from "@/features/edit-glossary";
import { GlossaryDialog, toGlossary, useGlossaryDialog, validateGlossary } from "@/features/edit-glossary";
import type { GlossaryEntry } from "@/shared/api";
import { makeProfile, renderWithProviders } from "@/test/render";
import { server } from "@/test/server";

function serve(initial: GlossaryEntry[] | null) {
  const puts: GlossaryEntry[][] = [];
  server.use(
    http.get("/api/profiles", () => HttpResponse.json([makeProfile({ id: "p1", name: "orders-dev" })])),
    http.get("/api/profiles/p1/glossary", () => HttpResponse.json(initial)),
    http.put("/api/profiles/p1/glossary", async ({ request }) => {
      puts.push((await request.json()) as GlossaryEntry[]);
      return new HttpResponse(null, { status: 204 });
    }),
  );
  return puts;
}

const draft = (term: string, extra: Partial<DraftEntry> = {}): DraftEntry => ({
  key: term,
  term,
  definition: "",
  sql: "",
  ...extra,
});

afterEach(() => useGlossaryDialog.setState({ open: false }));

describe("glossary model", () => {
  it("requires terms, rejects duplicates case-insensitively and enforces the API's lengths", () => {
    const errors = validateGlossary([
      draft("Active user"),
      draft(" active USER "),
      draft("", { key: "empty" }),
      draft("long", { definition: "x".repeat(1001) }),
    ]);
    expect(errors.get(" active USER ")?.term).toMatch(/already defined/);
    expect(errors.get("empty")?.term).toBe("Enter a term.");
    expect(errors.get("long")?.definition).toMatch(/1000/);
    expect(errors.has("Active user")).toBe(false);
    expect(toGlossary([draft("  churned ", { sql: " cancelled_at IS NOT NULL " })])).toEqual([
      { term: "churned", definition: "", sql: "cancelled_at IS NOT NULL" },
    ]);
  });
});

describe("GlossaryDialog", () => {
  it("adds, edits and deletes terms, then saves the full list with PUT", async () => {
    const puts = serve([
      { term: "active user", definition: "Signed in within 30 days", sql: "last_seen_at > now() - interval '30 days'" },
      { term: "churned", definition: "Cancelled", sql: "cancelled_at IS NOT NULL" },
    ]);
    useGlossaryDialog.setState({ open: true });
    const { user } = renderWithProviders(<GlossaryDialog />);
    expect(await screen.findByRole("heading", { name: "Glossary for orders-dev" })).toBeInTheDocument();
    const rows = await screen.findAllByTestId("glossary-row");
    expect(rows).toHaveLength(2);

    // Edit the first definition.
    const definition = within(rows[0] as HTMLElement).getByLabelText("Definition");
    await user.clear(definition);
    await user.type(definition, "Signed in within 7 days");
    // Delete "churned".
    await user.click(screen.getByRole("button", { name: "Delete churned" }));
    // Add a term.
    await user.click(screen.getByRole("button", { name: "Add term" }));
    const added = (await screen.findAllByTestId("glossary-row"))[1] as HTMLElement;
    await waitFor(() => expect(within(added).getByLabelText("Term")).toHaveFocus());
    await user.type(within(added).getByLabelText("Term"), "big order");
    await user.type(within(added).getByLabelText(/^SQL/), "total_cents > 100000");

    await user.click(screen.getByRole("button", { name: "Save glossary" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual([
      { term: "active user", definition: "Signed in within 7 days", sql: "last_seen_at > now() - interval '30 days'" },
      { term: "big order", definition: "", sql: "total_cents > 100000" },
    ]);
    await waitFor(() => expect(useGlossaryDialog.getState().open).toBe(false));
  });

  it("blocks saving an empty term with an inline error and handles a null glossary", async () => {
    const puts = serve(null);
    useGlossaryDialog.setState({ open: true });
    const { user } = renderWithProviders(<GlossaryDialog />);
    expect(await screen.findByText(/No terms yet/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Add term" }));
    await user.click(screen.getByRole("button", { name: "Save glossary" }));
    expect(await screen.findByText("Enter a term.")).toBeInTheDocument();
    expect(puts).toHaveLength(0);
  });
});
