import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TestResultChecklist } from "@/entities/connection";
import type { TestResult } from "@/shared/api";

const healthy: TestResult = {
  ok: true,
  latencyMs: 12,
  serverVersion: "17.2",
  serverVersionNum: 170002,
  isReplica: true,
  defaultReadOnly: true,
  writePrivileges: [],
  pgStatStatements: true,
  warnings: [],
};

function items() {
  return within(screen.getByRole("region", { name: "Connection test result" })).getAllByRole("listitem");
}

describe("TestResultChecklist", () => {
  it("lists every check of a passing test as readable text", () => {
    render(<TestResultChecklist result={healthy} env="local" />);
    const text = items().map((li) => li.textContent);
    expect(text).toEqual([
      "Passed: Connected in 12 ms",
      "Note: Server version: PostgreSQL 17.2",
      "Passed: Replica: yes, the server is in recovery",
      "Passed: Read-only by default: yes",
      "Passed: Write privileges: none, this login can only read",
      "Passed: pg_stat_statements: installed and readable",
    ]);
  });

  it("shows server warnings and a missing pg_stat_statements", () => {
    render(
      <TestResultChecklist
        result={{
          ...healthy,
          pgStatStatements: false,
          warnings: ["the server reports an unusual setting"],
        }}
        env="local"
      />,
    );
    expect(screen.getByText(/pg_stat_statements: not available/)).toBeInTheDocument();
    expect(screen.getByText("the server reports an unusual setting").closest("li")).toHaveTextContent(/^Warning:/);
  });

  it("explains an authentication failure with the server message, SQLSTATE and a hint", () => {
    render(
      <TestResultChecklist
        result={{
          ok: false,
          warnings: [],
          error: { kind: "auth", message: 'password authentication failed for user "app"', sqlstate: "28P01" },
        }}
        env="local"
      />,
    );
    const region = screen.getByRole("region", { name: "Connection test result" });
    expect(region).toHaveTextContent("Problem: Authentication failed");
    expect(region).toHaveTextContent('password authentication failed for user "app"');
    expect(region).toHaveTextContent("SQLSTATE 28P01");
    expect(region).toHaveTextContent(/Check the user name and password/);
  });

  it("warns strongly when a prod-replica login can write", () => {
    render(
      <TestResultChecklist
        result={{
          ...healthy,
          isReplica: false,
          defaultReadOnly: false,
          writePrivileges: ["INSERT on public.orders", "CREATE on schema public"],
        }}
        env="prod-replica"
      />,
    );
    const privileges = screen.getByText(/Write privileges: this login can change data \(2\)/).closest("li");
    expect(privileges).toHaveAttribute("data-tone", "danger");
    expect(privileges).toHaveTextContent(/^Problem:/);
    expect(privileges).toHaveTextContent(/use a SELECT-only role for production/);
    expect(privileges).toHaveTextContent("INSERT on public.orders");
    expect(privileges).toHaveTextContent("CREATE on schema public");
    expect(screen.getByText("Replica: no, this is a primary server").closest("li")).toHaveAttribute(
      "data-tone",
      "warn",
    );
  });

  it.each([
    [undefined, "default_transaction_read_only is off"],
    ["mariadb", "the server's read_only is off"],
    ["sqlserver", "the database is READ_WRITE"],
  ] as const)("names the %s setting that leaves the server writable", (engine, setting) => {
    render(<TestResultChecklist result={{ ...healthy, engine, defaultReadOnly: false }} env="local" />);
    expect(screen.getByText(`Read-only by default: no (${setting})`)).toBeInTheDocument();
  });

  it("uses a plain warning for write privileges outside prod-replica", () => {
    render(<TestResultChecklist result={{ ...healthy, writePrivileges: ["INSERT on public.orders"] }} env="staging" />);
    const privileges = screen.getByText(/Write privileges: this login can change data \(1\)/).closest("li");
    expect(privileges).toHaveAttribute("data-tone", "warn");
  });

  it("shows each prod-replica fact once and keeps the other server warnings", () => {
    const sslWarning =
      "sslMode is disable on a non-loopback host, so the password and query results travel unencrypted.";
    render(
      <TestResultChecklist
        result={{
          ...healthy,
          isReplica: false,
          defaultReadOnly: false,
          writePrivileges: ["INSERT on public.orders"],
          // The server states the replica and privilege facts only as fields.
          warnings: [sslWarning],
        }}
        env="prod-replica"
      />,
    );
    const text = items().map((li) => li.textContent ?? "");
    expect(text.filter((t) => /primary/.test(t))).toEqual(["Warning: Replica: no, this is a primary server"]);
    const privileges = text.filter((t) => /write privileges/i.test(t));
    expect(privileges).toHaveLength(1);
    expect(privileges[0]).toMatch(/^Problem: Write privileges: this login can change/);
    expect(screen.getByText(sslWarning).closest("li")).toHaveAttribute("data-tone", "warn");
  });
});
