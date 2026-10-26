import { screen, waitFor, within } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { describe, expect, it, vi } from "vitest";
import { UnlockSecretsDialog } from "@/features/unlock-secrets";
import { renderWithProviders } from "@/test/render";
import { server } from "@/test/server";

describe("UnlockSecretsDialog", () => {
  it("asks for the passphrase while the vault is locked and closes once unlocked", async () => {
    let locked = true;
    let sent: unknown;
    server.use(
      http.get("/api/secrets/status", () => HttpResponse.json({ backend: "passphrase", locked })),
      http.post("/api/secrets/unlock", async ({ request }) => {
        sent = await request.json();
        const { passphrase } = sent as { passphrase: string };
        if (passphrase !== "correct horse") {
          return HttpResponse.json({ error: { code: "unauthorized" } }, { status: 401 });
        }
        locked = false;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const { user } = renderWithProviders(<UnlockSecretsDialog />);

    const dialog = await screen.findByRole("dialog", { name: "Unlock saved passwords" });
    expect(dialog).toHaveTextContent(
      /The first time you unlock, the passphrase you enter becomes the vault passphrase/,
    );

    await user.type(screen.getByLabelText("Passphrase"), "wrong{Enter}");
    expect(await screen.findByText("That passphrase does not unlock the vault.")).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Passphrase"));
    await user.type(screen.getByLabelText("Passphrase"), "correct horse{Enter}");
    expect(sent).toEqual({ passphrase: "correct horse" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("keeps its content while the close animation runs", async () => {
    let locked = true;
    server.use(
      http.get("/api/secrets/status", () => HttpResponse.json({ backend: "passphrase", locked })),
      http.post("/api/secrets/unlock", () => {
        locked = false;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const { user } = renderWithProviders(<UnlockSecretsDialog />);
    await screen.findByRole("dialog", { name: "Unlock saved passwords" });

    // Base UI keeps a closing popup mounted until its animations finish; hold one open.
    let finishAnimation = () => {};
    const finished = new Promise<void>((resolve) => {
      finishAnimation = resolve;
    });
    const animation = { finished, effect: { getTiming: () => ({ duration: 100, iterations: 1 }) } };
    vi.spyOn(Element.prototype, "getAnimations").mockReturnValue([animation as unknown as Animation]);

    await user.type(screen.getByLabelText("Passphrase"), "correct horse{Enter}");
    const popup = await waitFor(() => {
      const closing = document.querySelector<HTMLElement>('[data-slot="dialog-content"][data-closed]');
      expect(closing).not.toBeNull();
      return closing as HTMLElement;
    });
    expect(popup).toHaveTextContent("Unlock saved passwords");
    expect(popup.querySelector("form")).not.toBeNull();

    finishAnimation();
    await waitFor(() => expect(document.querySelector('[data-slot="dialog-content"]')).toBeNull());
  });

  it("shows that unlocking is in progress", async () => {
    let release = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let locked = true;
    server.use(
      http.get("/api/secrets/status", () => HttpResponse.json({ backend: "passphrase", locked })),
      http.post("/api/secrets/unlock", async () => {
        await gate;
        locked = false;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const { user } = renderWithProviders(<UnlockSecretsDialog />);
    const dialog = await screen.findByRole("dialog", { name: "Unlock saved passwords" });

    await user.type(screen.getByLabelText("Passphrase"), "correct horse{Enter}");
    const button = await screen.findByRole("button", { name: /Unlocking/ });
    expect(button).toBeDisabled();
    expect(screen.getByLabelText("Passphrase")).toHaveAttribute("readonly");
    expect(dialog.querySelector("form")).toHaveAttribute("aria-busy", "true");
    expect(within(dialog).getByRole("status")).toHaveTextContent(/can take a few seconds/);

    release();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("stays closed when the vault is unlocked", async () => {
    server.use(http.get("/api/secrets/status", () => HttpResponse.json({ backend: "keyring", locked: false })));
    renderWithProviders(<UnlockSecretsDialog />);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
