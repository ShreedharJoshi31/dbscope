import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { useSqlTabs } from "@/entities/query/model/sql-tabs";
import { ExecutedSql } from "@/entities/query/ui/executed-sql";
import { renderWithProviders } from "@/test/render";

describe("ExecutedSql", () => {
  afterEach(() => useSqlTabs.getState().setScope(null));

  it("says SQL for SQL connections", () => {
    useSqlTabs.getState().setScope("pg", "sql");
    renderWithProviders(<ExecutedSql executedSql="SELECT 1 LIMIT 501" submittedSql="SELECT 1" />);
    expect(screen.getByText("Executed SQL")).toBeInTheDocument();
    expect(screen.getByText("(rewritten by dbscope)")).toBeInTheDocument();
  });

  it("says query for MongoDB, Cypher and other languages", () => {
    useSqlTabs.getState().setScope("mongo", "json");
    renderWithProviders(<ExecutedSql executedSql='{"find": "reviews"}' />);
    expect(screen.getByText("Executed query")).toBeInTheDocument();
    expect(screen.queryByText("Executed SQL")).not.toBeInTheDocument();
  });
});
