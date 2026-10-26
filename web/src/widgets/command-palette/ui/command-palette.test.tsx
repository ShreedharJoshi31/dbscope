import { screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { beforeEach, describe, expect, it } from "vitest";
import { AppHotkeys } from "@/app/hotkeys";
import { ShortcutSheet, useShortcutSheet } from "@/features/show-shortcuts";
import { Input } from "@/shared/ui/input";
import { makeProfile, renderWithProviders } from "@/test/render";
import { server } from "@/test/server";
import { CommandPalette, useCommandPalette } from "@/widgets/command-palette";

function renderWorkbenchShortcuts() {
  server.use(
    http.get("/api/profiles", () =>
      HttpResponse.json([
        makeProfile({ id: "a", name: "orders-dev" }),
        makeProfile({ id: "b", name: "reports", env: "prod-replica", host: "replica.internal" }),
      ]),
    ),
  );
  return renderWithProviders(
    <>
      <Input aria-label="Filter" />
      <AppHotkeys workbench />
      <CommandPalette />
      <ShortcutSheet />
    </>,
  );
}

describe("command palette and shortcut sheet", () => {
  beforeEach(() => {
    useCommandPalette.setState({ open: false });
    useShortcutSheet.setState({ open: false });
  });

  it("opens the command palette with Ctrl+K and lists connection switching", async () => {
    const { user } = renderWorkbenchShortcuts();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    await user.keyboard("{Control>}k{/Control}");

    const dialog = await screen.findByRole("dialog", { name: "Command palette" });
    expect(dialog).toBeInTheDocument();
    expect(await screen.findByRole("option", { name: /Switch to reports/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /New connection/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Toggle schema explorer/ })).toBeInTheDocument();
  });

  it("opens the palette with Ctrl+K even while typing in a field", async () => {
    const { user } = renderWorkbenchShortcuts();
    await user.click(screen.getByLabelText("Filter"));
    await user.keyboard("{Control>}k{/Control}");
    expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeInTheDocument();
  });

  it("does not open the shortcut sheet when ? is typed into a text field", async () => {
    const { user } = renderWorkbenchShortcuts();
    const field = screen.getByLabelText("Filter");
    await user.click(field);
    await user.keyboard("?");

    expect(field).toHaveValue("?");
    expect(screen.queryByRole("dialog", { name: "Keyboard shortcuts" })).not.toBeInTheDocument();
  });

  it("opens the shortcut sheet with ? outside text fields", async () => {
    const { user } = renderWorkbenchShortcuts();
    await user.keyboard("?");
    const sheet = await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    await waitFor(() => expect(sheet).toHaveTextContent("Toggle results dock"));
  });
});
