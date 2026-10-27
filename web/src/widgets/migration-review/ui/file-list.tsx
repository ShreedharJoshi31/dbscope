import { cn } from "cn";
import { ChevronDown, ChevronRight, FileCode2 } from "lucide-react";
import { lazy, Suspense } from "react";
import {
  emptyCounts,
  RATINGS,
  RatingBadge,
  type ReviewFile,
  type ReviewModel,
  ratingRank,
} from "@/entities/migration-finding";
import type { FileCodeHandle } from "@/widgets/migration-review/ui/file-code";

// CodeMirror loads on first use, like the SQL editor.
const FileCode = lazy(() => import("@/widgets/migration-review/ui/file-code"));

/** Up to this many files start expanded; beyond it, only files with Medium or worse statements do. */
export const EXPAND_ALL_UP_TO = 20;

export function initiallyExpanded(model: ReviewModel): Set<number> {
  const out = new Set<number>();
  model.files.forEach((f, i) => {
    const risky = f.statements.some((s) => ratingRank(model.statements[s]?.rating ?? "low") >= 1);
    if (model.files.length <= EXPAND_ALL_UP_TO || risky) out.add(i);
  });
  return out;
}

function FileCounts({ model, file }: { model: ReviewModel; file: ReviewFile }) {
  const counts = emptyCounts();
  for (const i of file.statements) {
    const s = model.statements[i];
    if (s) counts[s.rating] += 1;
  }
  return (
    <span className="ml-auto flex shrink-0 items-center gap-1">
      {RATINGS.filter((r) => r !== "low" && counts[r] > 0).map((r) => (
        <RatingBadge key={r} rating={r} suffix={counts[r]} />
      ))}
      <span className="font-mono text-2xs text-text-muted tabular-nums">
        {file.statements.length} statement{file.statements.length === 1 ? "" : "s"}
      </span>
    </span>
  );
}

interface FileListProps {
  model: ReviewModel;
  selected: number;
  expanded: ReadonlySet<number>;
  onToggle: (fileIndex: number) => void;
  onSelect: (index: number) => void;
  onOpenDetail: (index: number) => void;
  registerHandle: (fileIndex: number, handle: FileCodeHandle | null) => void;
}

/** The files in execution order, each a read-only editor with a rating marker per statement. */
export function FileList({
  model,
  selected,
  expanded,
  onToggle,
  onSelect,
  onOpenDetail,
  registerHandle,
}: FileListProps) {
  return (
    <div className="flex flex-col">
      {model.files.map((file, fileIndex) => {
        const open = expanded.has(fileIndex);
        const Chevron = open ? ChevronDown : ChevronRight;
        const statements = file.statements.map((i) => model.statements[i]).filter((s) => s !== undefined);
        return (
          <section
            key={file.name}
            aria-label={file.name}
            data-testid="migration-file"
            data-file={file.name}
            className="border-b border-border-default"
          >
            <h3 className="sticky top-0 z-10 bg-surface-subtle">
              <button
                type="button"
                aria-expanded={open}
                onClick={() => onToggle(fileIndex)}
                className={cn(
                  "flex h-8 w-full items-center gap-1.5 px-2 text-left text-xs outline-none",
                  "hover:bg-surface-hover focus-visible:ring-2 focus-visible:ring-border-focus focus-visible:ring-inset",
                )}
              >
                <Chevron aria-hidden className="size-3.5 shrink-0 text-text-muted" />
                <FileCode2 aria-hidden className="size-3.5 shrink-0 text-text-muted" />
                <span className="min-w-0 truncate font-mono font-semibold text-text-primary">{file.name}</span>
                <FileCounts model={model} file={file} />
              </button>
            </h3>
            {open &&
              (statements.length === 0 && !file.text.trim() ? (
                <p className="px-3 py-2 text-xs text-text-muted">Empty file.</p>
              ) : (
                <>
                  {file.synthesized && (
                    <p className="px-3 py-1 text-2xs text-text-muted">
                      The original file was not available; showing the statements at their lines.
                    </p>
                  )}
                  {statements.length === 0 && (
                    <p className="px-3 py-1 text-2xs text-text-muted">No SQL statements in this file.</p>
                  )}
                  <Suspense
                    fallback={
                      <pre className="overflow-hidden px-3 py-1 font-mono text-xs text-text-muted">{file.text}</pre>
                    }
                  >
                    <FileCode
                      text={file.text}
                      label={`${file.name}, read only`}
                      statements={statements}
                      selected={selected}
                      onSelect={onSelect}
                      onOpenDetail={onOpenDetail}
                      handleRef={(h) => registerHandle(fileIndex, h)}
                    />
                  </Suspense>
                </>
              ))}
          </section>
        );
      })}
    </div>
  );
}
