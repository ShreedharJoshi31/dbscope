import { cn } from "cn";
import { comboKeys, type KeyCombo } from "@/shared/config/shortcuts";
import { Kbd, KbdGroup } from "@/shared/ui/kbd";

interface ShortcutKeysProps {
  combo: KeyCombo;
  className?: string;
}

export function ShortcutKeys({ combo, className }: ShortcutKeysProps) {
  return (
    <KbdGroup className={cn("font-mono", className)}>
      {comboKeys(combo).map((key) => (
        <Kbd key={key}>{key}</Kbd>
      ))}
    </KbdGroup>
  );
}
