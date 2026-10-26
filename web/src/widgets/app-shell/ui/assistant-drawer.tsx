import { type ReactNode, useRef } from "react";
import { usePanelStore } from "@/features/toggle-panel";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/shared/ui/sheet";

/** Below 1280px the assistant overlays the workspace instead of taking a column. */
export function AssistantDrawer({ children }: { children: ReactNode }) {
  const open = usePanelStore((s) => s.assistantDrawer);
  const setOpen = usePanelStore((s) => s.setAssistantDrawer);
  // Focus the region, not the first button: a focused button opens its tooltip, which would eat the first Escape.
  const regionRef = useRef<HTMLElement>(null);

  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetContent
        side="right"
        showCloseButton={false}
        initialFocus={regionRef}
        className="w-[360px] gap-0 p-0 sm:max-w-[360px]"
      >
        <SheetTitle className="sr-only">Assistant</SheetTitle>
        <SheetDescription className="sr-only">Ask questions about this database in plain English.</SheetDescription>
        <aside
          ref={regionRef}
          aria-label="Assistant"
          data-region="assistant"
          tabIndex={-1}
          className="flex h-full flex-col"
        >
          {children}
        </aside>
      </SheetContent>
    </Sheet>
  );
}
