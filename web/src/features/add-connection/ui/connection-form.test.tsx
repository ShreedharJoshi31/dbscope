import { screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { describe, expect, it, vi } from "vitest";
import { ConnectionForm } from "@/features/add-connection";
import type { ProfileInput } from "@/shared/api";
import { makeProfile, renderWithProviders } from "@/test/render";
import { server } from "@/test/server";

function renderForm() {
  const onSaved = vi.fn();
  const utils = renderWithProviders(<ConnectionForm submitLabel="Save and open" onSaved={onSaved} autoFocus />);
  return { ...utils, onSaved };
}

describe("ConnectionForm", () => {
  it("switches to MySQL with its default port and saves the engine", async () => {
    let sent: ProfileInput | undefined;
    server.use(
      http.post("/api/profiles", async ({ request }) => {
        sent = (await request.json()) as ProfileInput;
        return HttpResponse.json(makeProfile({ ...sent, id: "01JB0000000000000000000010" }), { status: 201 });
      }),
    );
    const { user, onSaved } = renderForm();
    await user.click(screen.getByRole("radio", { name: "MySQL" }));
    expect(screen.getByLabelText("Port")).toHaveValue("3306");
    expect(screen.getByLabelText("Paste a connection URL")).toHaveAttribute(
      "placeholder",
      expect.stringMatching(/^mysql:\/\//),
    );
    await user.type(screen.getByLabelText("Name"), "shop");
    await user.click(screen.getByRole("radio", { name: /local/ }));
    await user.type(screen.getByLabelText("Host"), "127.0.0.1");
    await user.type(screen.getByLabelText("Database", { exact: true }), "app");
    await user.type(screen.getByLabelText("User"), "reader");
    await user.click(screen.getByRole("button", { name: "Save and open" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sent).toMatchObject({ engine: "mysql", port: 3306, host: "127.0.0.1", database: "app" });
  });

  it("switches to a SQLite file and saves its path", async () => {
    let sent: ProfileInput | undefined;
    server.use(
      http.post("/api/profiles", async ({ request }) => {
        sent = (await request.json()) as ProfileInput;
        return HttpResponse.json(makeProfile({ ...sent, id: "01JB0000000000000000000009" }), { status: 201 });
      }),
    );
    const { user, onSaved } = renderForm();

    await user.click(screen.getByRole("radio", { name: "SQLite" }));
    expect(screen.queryByLabelText("Paste a connection URL")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Host")).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Name"), "shop");
    await user.click(screen.getByRole("radio", { name: /local/ }));
    await user.type(screen.getByLabelText("Database file"), "shop.db");
    await user.click(screen.getByRole("button", { name: "Save and open" }));
    expect(await screen.findByText(/full path of the database file/)).toBeInTheDocument();
    expect(sent).toBeUndefined();

    await user.clear(screen.getByLabelText("Database file"));
    await user.type(screen.getByLabelText("Database file"), "/Users/me/shop.db");
    await user.click(screen.getByRole("button", { name: "Save and open" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sent).toMatchObject({ engine: "sqlite", path: "/Users/me/shop.db", name: "shop", env: "local" });
  });

  it("shows inline errors on Enter and focuses the first invalid field", async () => {
    const { user, onSaved } = renderForm();

    await user.click(screen.getByLabelText("Name"));
    await user.keyboard("{Enter}");

    expect(await screen.findByText("Enter a name for this connection.")).toBeInTheDocument();
    expect(screen.getByText("Choose the environment this database belongs to.")).toBeInTheDocument();
    expect(screen.getByText("Enter the server host name or IP address.")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Name")).toHaveAccessibleDescription("Enter a name for this connection.");
    await waitFor(() => expect(screen.getByLabelText("Name")).toHaveFocus());
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("validates a field when the user leaves it", async () => {
    const { user } = renderForm();
    const port = screen.getByLabelText("Port");
    await user.clear(port);
    await user.type(port, "99999");
    await user.tab();
    expect(screen.getByText("Port must be a whole number from 1 to 65535.")).toBeInTheDocument();
    expect(screen.queryByText("Enter the database name.")).not.toBeInTheDocument();
  });

  it("fills the fields from a pasted URL and clears the URL", async () => {
    let sent: unknown;
    server.use(
      http.post("/api/connection-url/parse", async ({ request }) => {
        sent = await request.json();
        return HttpResponse.json({
          engine: "postgres",
          host: "127.0.0.1",
          port: 6432,
          database: "orders",
          user: "app",
          password: "pw",
          sslMode: "disable",
        });
      }),
    );
    const { user } = renderForm();

    const url = screen.getByLabelText("Paste a connection URL");
    expect(url).toHaveFocus();
    await user.paste("postgresql://app:pw@127.0.0.1:6432/orders?sslmode=disable");

    await waitFor(() => expect(screen.getByLabelText("Host")).toHaveValue("127.0.0.1"));
    expect(sent).toEqual({ url: "postgresql://app:pw@127.0.0.1:6432/orders?sslmode=disable" });
    expect(screen.getByLabelText("Port")).toHaveValue("6432");
    expect(screen.getByLabelText("Database")).toHaveValue("orders");
    expect(screen.getByLabelText("User")).toHaveValue("app");
    expect(screen.getByLabelText(/Password/)).toHaveValue("pw");
    expect(screen.getByLabelText("Name")).toHaveValue("orders@127.0.0.1");
    expect(url).toHaveValue("");
    expect(screen.getByTestId("live-region")).toHaveTextContent(
      "Filled name, host, port, database, user, SSL mode, password",
    );
    // Loopback host: suggest local, but do not choose it for the user.
    expect(screen.getByText("This host is on your machine.")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /local/ })).not.toBeChecked();
    await waitFor(() => expect(screen.getByRole("radio", { name: /local/ })).toHaveFocus());
  });

  it("shows the parser's error inline when the URL is invalid", async () => {
    server.use(
      http.post("/api/connection-url/parse", () =>
        HttpResponse.json({ error: { code: "validation", message: 'unsupported scheme "ftp"' } }, { status: 400 }),
      ),
    );
    const { user } = renderForm();
    await user.type(screen.getByLabelText("Paste a connection URL"), "ftp://x{Enter}");
    expect(await screen.findByText('unsupported scheme "ftp"')).toBeInTheDocument();
    expect(screen.getByLabelText("Paste a connection URL")).toHaveAttribute("aria-invalid", "true");
  });

  it("does not suggest local for a remote host", async () => {
    const { user } = renderForm();
    await user.type(screen.getByLabelText("Host"), "db.prod.internal");
    expect(screen.queryByText("This host is on your machine.")).not.toBeInTheDocument();
  });

  it("tests, then saves with the chosen environment and its default timeout", async () => {
    let saved: ProfileInput | undefined;
    server.use(
      http.post("/api/profiles/test", () =>
        HttpResponse.json({ ok: true, latencyMs: 4, serverVersion: "17.2", warnings: [] }),
      ),
      http.post("/api/profiles", async ({ request }) => {
        saved = (await request.json()) as ProfileInput;
        return HttpResponse.json(makeProfile({ ...saved, id: "new-id" }), { status: 201 });
      }),
    );
    const { user, onSaved } = renderForm();

    await user.type(screen.getByLabelText("Name"), "reports");
    await user.click(screen.getByRole("radio", { name: /prod-replica/ }));
    await user.type(screen.getByLabelText("Host"), "replica.internal");
    await user.type(screen.getByLabelText("Database"), "reports");
    await user.type(screen.getByLabelText("User"), "reader");

    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Connected in 4 ms")).toBeInTheDocument();
    expect(screen.getByTestId("live-region")).toHaveTextContent("Connection test passed.");

    await user.click(screen.getByRole("button", { name: "Save and open" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce());
    expect(saved).toMatchObject({ name: "reports", env: "prod-replica", statementTimeoutMs: 10000 });
    expect(saved).not.toHaveProperty("password");
  });

  it("flags a duplicate name reported by the agent on the name field", async () => {
    server.use(
      http.post("/api/profiles", () =>
        HttpResponse.json({ error: { code: "conflict", message: "name already used" } }, { status: 409 }),
      ),
    );
    const { user } = renderForm();
    await user.type(screen.getByLabelText("Name"), "orders-dev");
    await user.click(screen.getByRole("radio", { name: /staging/ }));
    await user.type(screen.getByLabelText("Host"), "h");
    await user.type(screen.getByLabelText("Database"), "d");
    await user.type(screen.getByLabelText("User"), "u{Enter}");

    expect(await screen.findByText(/A connection with this name already exists/)).toBeInTheDocument();
  });

  it("clears the test result when the environment changes", async () => {
    server.use(
      http.post("/api/profiles/test", () =>
        HttpResponse.json({ ok: true, latencyMs: 4, isReplica: false, writePrivileges: [], warnings: [] }),
      ),
    );
    const { user } = renderForm();
    await user.type(screen.getByLabelText("Name"), "reports");
    await user.click(screen.getByRole("radio", { name: /staging/ }));
    await user.type(screen.getByLabelText("Host"), "replica.internal");
    await user.type(screen.getByLabelText("Database"), "reports");
    await user.type(screen.getByLabelText("User"), "reader");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Connected in 4 ms")).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: /prod-replica/ }));
    expect(screen.queryByRole("region", { name: "Connection test result" })).not.toBeInTheDocument();
  });
  it("saves an SSH tunnel and shows an SSH failure from the test", async () => {
    let sent: ProfileInput | undefined;
    server.use(
      http.post("/api/profiles/test", () =>
        HttpResponse.json({
          ok: false,
          warnings: [],
          error: { kind: "ssh", message: "the SSH server bastion:22 is not in /home/me/.ssh/known_hosts" },
        }),
      ),
      http.post("/api/profiles", async ({ request }) => {
        sent = (await request.json()) as ProfileInput;
        return HttpResponse.json(makeProfile({ ...sent, id: "01JB0000000000000000000011" }), { status: 201 });
      }),
    );
    const { user, onSaved } = renderForm();
    await user.type(screen.getByLabelText("Name"), "orders-prod");
    await user.click(screen.getByRole("radio", { name: /prod-replica/ }));
    await user.type(screen.getByLabelText("Host"), "orders.internal");
    await user.type(screen.getByLabelText("Database", { exact: true }), "orders");
    await user.type(screen.getByLabelText("User"), "reader");
    expect(screen.queryByLabelText("SSH host")).not.toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: /Connect through an SSH server/ }));
    await user.click(screen.getByRole("button", { name: "Save and open" }));
    expect(screen.getByLabelText("SSH host")).toHaveFocus();
    await user.type(screen.getByLabelText("SSH host"), "bastion");
    await user.type(screen.getByLabelText("SSH user"), "ops");
    expect(screen.queryByLabelText(/Key passphrase/)).not.toBeInTheDocument();
    await user.type(screen.getByLabelText(/Key file/), "/Users/me/.ssh/id_ed25519");
    await user.type(screen.getByLabelText(/Key passphrase/), "pp");

    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("SSH tunnel failed")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Save and open" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sent).toMatchObject({
      host: "orders.internal",
      ssh: { host: "bastion", port: 22, user: "ops", keyFile: "/Users/me/.ssh/id_ed25519" },
      sshPassphrase: "pp",
    });
  });
});
