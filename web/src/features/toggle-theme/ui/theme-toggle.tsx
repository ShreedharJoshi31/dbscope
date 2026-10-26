import { Monitor, Moon, Sun } from "lucide-react";
import { type ThemePreference, useThemeStore } from "@/features/toggle-theme/model/theme-store";
import { Button } from "@/shared/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";

const OPTIONS: { value: ThemePreference; label: string; icon: typeof Sun }[] = [
  { value: "system", label: "System", icon: Monitor },
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
];

export function ThemeToggle() {
  const { preference, setPreference } = useThemeStore();
  const current = OPTIONS.find((o) => o.value === preference) ?? OPTIONS[0];
  const Icon = current?.icon ?? Monitor;

  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger
          render={
            <DropdownMenuTrigger
              render={<Button variant="ghost" size="icon-sm" aria-label={`Theme: ${current?.label ?? "System"}`} />}
            />
          }
        >
          <Icon aria-hidden />
        </TooltipTrigger>
        <TooltipContent side="bottom">Theme</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end" className="w-40">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Theme</DropdownMenuLabel>
          <DropdownMenuRadioGroup value={preference} onValueChange={(v) => setPreference(v as ThemePreference)}>
            {OPTIONS.map(({ value, label, icon: OptionIcon }) => (
              <DropdownMenuRadioItem key={value} value={value} closeOnClick>
                <OptionIcon aria-hidden />
                {label}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
