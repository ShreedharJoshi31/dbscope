import { screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { describe, expect, it } from "vitest";
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

  it("stays closed when the vault is unlocked", async () => {
    server.use(http.get("/api/secrets/status", () => HttpResponse.json({ backend: "keyring", locked: false })));
    renderWithProviders(<UnlockSecretsDialog />);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
