import { screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { useThemeStore } from "@/features/toggle-theme/model/theme-store";
import { ThemeToggle } from "@/features/toggle-theme/ui/theme-toggle";
import { renderWithProviders } from "@/test/render";

describe("ThemeToggle", () => {
  beforeEach(() => useThemeStore.setState({ preference: "system" }));

  it("closes the menu and returns focus to the trigger after a click", async () => {
    const { user } = renderWithProviders(<ThemeToggle />);
    await user.click(screen.getByRole("button", { name: "Theme: System" }));
    await user.click(await screen.findByRole("menuitemradio", { name: "Dark" }));

    expect(useThemeStore.getState().preference).toBe("dark");
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Theme: Dark" })).toHaveFocus();
  });

  it("closes the menu and returns focus to the trigger after Enter", async () => {
    const { user } = renderWithProviders(<ThemeToggle />);
    screen.getByRole("button", { name: "Theme: System" }).focus();
    await user.keyboard("{Enter}");
    await screen.findByRole("menu");
    const light = screen.getByRole("menuitemradio", { name: "Light" });
    await waitFor(() => expect(screen.getByRole("menuitemradio", { name: "System" })).toHaveFocus());
    await user.keyboard("{ArrowDown}");
    expect(light).toHaveFocus();
    await user.keyboard("{Enter}");

    expect(useThemeStore.getState().preference).toBe("light");
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Theme: Light" })).toHaveFocus();
  });
});
