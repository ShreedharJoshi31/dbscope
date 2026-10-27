import { useShortcutSheet } from "@/features/show-shortcuts/model/sheet-store";
import { SHORTCUT_GROUPS, SHORTCUTS } from "@/shared/config/shortcuts";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/shared/ui/dialog";
import { ShortcutKeys } from "@/shared/ui/shortcut-keys";

export function ShortcutSheet() {
  const { open, setOpen } = useShortcutSheet();
  const all = Object.values(SHORTCUTS);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>
            Single-key shortcuts such as ? do nothing while you are typing in a field.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          {SHORTCUT_GROUPS.map((group) => (
            <section key={group} aria-labelledby={`shortcuts-${group}`}>
              <h3
                id={`shortcuts-${group}`}
                className="mb-1 text-2xs font-semibold tracking-wide text-text-muted uppercase"
              >
                {group}
              </h3>
              <dl className="flex flex-col">
                {all
                  .filter((s) => s.group === group)
                  .map((shortcut) => (
                    <div
                      key={shortcut.label}
                      className="flex h-row-default items-center justify-between border-b border-border-default last:border-b-0"
                    >
                      <dt className="text-sm">{shortcut.label}</dt>
                      <dd>
                        <ShortcutKeys combo={shortcut.combo} keys={shortcut.keys} />
                      </dd>
                    </div>
                  ))}
              </dl>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
