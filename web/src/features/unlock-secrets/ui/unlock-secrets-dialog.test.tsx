import { useQuery } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UnlockSecretsDialog } from "@/features/unlock-secrets";
import { useUnlockPrompt } from "@/features/unlock-secrets/model/unlock-prompt";
import { apiFetch } from "@/shared/api";
import { renderWithProviders } from "@/test/render";
import { server } from "@/test/server";

function Probe() {
  const q = useQuery({ queryKey: ["probe"], queryFn: () => apiFetch<{ ok: boolean }>("/api/probe"), retry: false });
  return <p>{q.isSuccess ? "probe ok" : q.isError ? "probe failed" : "probe loading"}</p>;
}

describe("UnlockSecretsDialog", () => {
  beforeEach(() => useUnlockPrompt.setState({ dismissed: false, requested: false }));

  it("asks for the passphrase while the vault is locked and closes once unlocked", async () => {
    let locked = true;
    let sent: unknown;
    server.use(
      http.get("/api/secrets/status", () => HttpResponse.json({ backend: "passphrase", locked, initialized: true })),
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
    expect(dialog).toHaveTextContent(/encrypted with your passphrase/);

    await user.type(screen.getByLabelText("Passphrase"), "wrong{Enter}");
    expect(await screen.findByText("That passphrase does not unlock the vault.")).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Passphrase"));
    await user.type(screen.getByLabelText("Passphrase"), "correct horse{Enter}");
    expect(sent).toEqual({ passphrase: "correct horse" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("refetches requests that failed while the vault was locked", async () => {
    let locked = true;
    let probeCalls = 0;
    server.use(
      http.get("/api/secrets/status", () => HttpResponse.json({ backend: "passphrase", locked, initialized: true })),
      http.post("/api/secrets/unlock", () => {
        locked = false;
        return new HttpResponse(null, { status: 204 });
      }),
      http.get("/api/probe", () => {
        probeCalls++;
        return locked
          ? HttpResponse.json({ error: { code: "secrets_locked" } }, { status: 423 })
          : HttpResponse.json({ ok: true });
      }),
    );
    const { user } = renderWithProviders(
      <>
        <UnlockSecretsDialog />
        <Probe />
      </>,
    );
    expect(await screen.findByText("probe failed")).toBeInTheDocument();

    await user.type(await screen.findByLabelText("Passphrase"), "pw{Enter}");
    expect(await screen.findByText("probe ok")).toBeInTheDocument();
    expect(probeCalls).toBe(2);
  });

  it("keeps its content while the close animation runs", async () => {
    let locked = true;
    server.use(
      http.get("/api/secrets/status", () => HttpResponse.json({ backend: "passphrase", locked, initialized: true })),
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
      http.get("/api/secrets/status", () => HttpResponse.json({ backend: "passphrase", locked, initialized: true })),
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
    server.use(
      http.get("/api/secrets/status", () =>
        HttpResponse.json({ backend: "keyring", locked: false, initialized: true }),
      ),
    );
    renderWithProviders(<UnlockSecretsDialog />);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("does not interrupt a first run with nothing saved, and asks for a new passphrase once one is needed", async () => {
    let initialized = false;
    server.use(
      http.get("/api/secrets/status", () =>
        HttpResponse.json({ backend: "passphrase", locked: !initialized, initialized }),
      ),
      http.post("/api/secrets/unlock", () => {
        initialized = true;
        return new HttpResponse(null, { status: 204 });
      }),
      http.get("/api/probe", () =>
        initialized
          ? HttpResponse.json({ ok: true })
          : HttpResponse.json({ error: { code: "secrets_locked" } }, { status: 423 }),
      ),
    );
    const { user } = renderWithProviders(<UnlockSecretsDialog />);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    renderWithProviders(<Probe />);
    expect(await screen.findByText("probe failed")).toBeInTheDocument();
    const dialog = await screen.findByRole("dialog", { name: "Choose a passphrase for saved passwords" });
    expect(screen.getByLabelText("Passphrase")).toHaveAttribute("autocomplete", "new-password");

    await user.type(screen.getByLabelText("Passphrase"), "correct horse");
    await user.click(within(dialog).getByRole("button", { name: "Set passphrase" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
