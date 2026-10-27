import { cn } from "cn";
import { comboKeys, type KeyCombo } from "@/shared/config/shortcuts";
import { Kbd, KbdGroup } from "@/shared/ui/kbd";

interface ShortcutKeysProps {
  combo: KeyCombo;
  /** Overrides the caps derived from the combo, e.g. the four arrow keys. */
  keys?: string[] | undefined;
  className?: string;
}

export function ShortcutKeys({ combo, keys, className }: ShortcutKeysProps) {
  return (
    <KbdGroup className={cn("font-mono", className)}>
      {(keys ?? comboKeys(combo)).map((key) => (
        <Kbd key={key}>{key}</Kbd>
      ))}
    </KbdGroup>
  );
}
