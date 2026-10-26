import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { EnvBadge } from "@/entities/connection";

describe("EnvBadge", () => {
  it.each([
    ["local", "local"],
    ["staging", "staging"],
    ["prod-replica", "prod-replica"],
  ] as const)("names the %s environment in text, not only colour", (env, label) => {
    render(<EnvBadge env={env} />);
    const badge = screen.getByText(label);
    expect(badge).toHaveTextContent(`Environment: ${label}`);
    expect(badge).toHaveAttribute("data-env", env);
    expect(badge.querySelector("svg")).not.toBeNull();
  });
});
