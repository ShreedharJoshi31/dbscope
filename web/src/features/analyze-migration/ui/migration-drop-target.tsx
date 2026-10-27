import { FileUp } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { startAnalysis, useMigrationTarget } from "@/features/analyze-migration/model/analyze";
import { collectDrop, readPicked } from "@/features/analyze-migration/model/read-files";

function hasFiles(event: DragEvent): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes("Files");
}

/** dragover fires every ~50 ms while a drag is over the page; no dragover for this long means it left. */
const LEAVE_MS = 200;

/**
 * Files dropped anywhere on the workbench open a migration tab. Listens in the
 * capture phase on window, so a drop on the SQL editor analyzes the files
 * instead of pasting their text. Drop targets that handle files themselves
 * (the analyze dialog) stop propagation first.
 */
export function MigrationDropTarget() {
  const { target, label } = useMigrationTarget();
  const latest = useRef({ target, label });
  latest.current = { target, label };
  const [active, setActive] = useState(false);

  useEffect(() => {
    let timer: number | undefined;
    const hideSoon = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => setActive(false), LEAVE_MS);
    };
    const onDragOver = (event: DragEvent) => {
      if (!hasFiles(event)) return;
      // Inside a dialog, its own drop zone (or nothing) handles the drop.
      if (event.target instanceof Element && event.target.closest('[role="dialog"]')) return;
      event.preventDefault();
      if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
      setActive(true);
      hideSoon();
    };
    const onDrop = (event: DragEvent) => {
      if (!hasFiles(event) || !event.dataTransfer) return;
      if (event.target instanceof Element && event.target.closest('[role="dialog"]')) return;
      event.preventDefault();
      event.stopPropagation();
      window.clearTimeout(timer);
      setActive(false);
      const collecting = collectDrop(event.dataTransfer);
      void collecting.then(async ({ picked, ignored }) => {
        const { sources, failed } = await readPicked(picked);
        const { target: t, label: l } = latest.current;
        startAnalysis({ sources, ignored: [...ignored, ...failed], target: t, targetLabel: l });
      });
    };
    window.addEventListener("dragover", onDragOver, true);
    window.addEventListener("drop", onDrop, true);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("dragover", onDragOver, true);
      window.removeEventListener("drop", onDrop, true);
    };
  }, []);

  if (!active) return null;
  return (
    <div
      data-testid="migration-drop-overlay"
      aria-hidden
      className="pointer-events-none fixed inset-2 z-50 flex items-center justify-center rounded-lg border-2 border-dashed border-accent-fg bg-surface-overlay/90"
    >
      <div className="flex max-w-md flex-col items-center gap-2 text-center">
        <FileUp className="size-6 text-accent-fg" strokeWidth={1.5} />
        <p className="text-sm font-semibold text-text-primary">
          {target ? `Drop to analyze the migrations against ${label}` : "Drop to analyze the migrations"}
        </p>
        <p className="text-xs text-text-muted">
          {target
            ? ".sql files or a folder. dbscope reads the catalog only; nothing runs against your tables."
            : "Add a connection or open the sample schema first: migrations are checked against a schema."}
        </p>
      </div>
    </div>
  );
}
