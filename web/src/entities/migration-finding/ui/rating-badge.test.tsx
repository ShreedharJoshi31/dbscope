import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { RatingBadge } from "@/entities/migration-finding/ui/rating-badge";
import { ratingIconElement } from "@/entities/migration-finding/ui/rating-icon-shape";
import type { Rating } from "@/shared/api";

const RATINGS: Rating[] = ["low", "medium", "high", "critical"];

describe("rating badge and icon", () => {
  it("shows icon and text for every rating, never colour alone", () => {
    render(
      <div>
        {RATINGS.map((r) => (
          <RatingBadge key={r} rating={r} suffix={r === "critical" ? 2 : undefined} />
        ))}
      </div>,
    );
    const badges = screen.getAllByTestId("rating-badge");
    expect(badges.map((b) => b.textContent)).toEqual(["Low", "Medium", "High", "Critical2"]);
    expect(badges.map((b) => b.querySelector("svg")?.getAttribute("data-shape"))).toEqual([
      "circle",
      "triangle",
      "octagon",
      "octagon-filled",
    ]);
    expect(badges[0]?.className).toContain("text-status-info-fg");
    expect(badges[1]?.className).toContain("text-status-warn-fg");
    expect(badges[2]?.className).toContain("text-status-danger-fg");
    expect(badges[3]?.getAttribute("title")).toMatch(/^Critical: will fail/);
  });

  it("draws Critical as a filled danger octagon with a bar", () => {
    const { container } = render(<RatingBadge rating="critical" />);
    const svg = container.querySelector("svg");
    expect(svg?.getAttribute("class")).toContain("text-status-danger-emphasis");
    const paths = svg?.querySelectorAll("path") ?? [];
    expect(paths[0]?.getAttribute("fill")).toBe("currentColor");
    expect(paths[1]?.getAttribute("d")).toBe("M8 12h8");
    expect(paths[1]?.getAttribute("stroke")).toBe("var(--text-on-emphasis)");
  });

  it("builds the same shapes as DOM for the CodeMirror gutter", () => {
    for (const r of RATINGS) {
      const el = ratingIconElement(r);
      expect(el.getAttribute("aria-hidden")).toBe("true");
      expect(el.getAttribute("data-shape")).toBe(
        { low: "circle", medium: "triangle", high: "octagon", critical: "octagon-filled" }[r],
      );
    }
    expect(ratingIconElement("low").querySelector("circle")).not.toBeNull();
  });
});
