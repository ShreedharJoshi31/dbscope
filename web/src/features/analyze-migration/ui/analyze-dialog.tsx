import { ClipboardPaste, FileUp, FolderOpen, LoaderCircle } from "lucide-react";
import { type DragEvent, type FormEvent, useEffect, useId, useRef, useState } from "react";
import type { ClientProblem, MigrationSource, ReviewOptions } from "@/entities/migration-finding";
import { lastReviewOptions, startAnalysis, useMigrationTarget } from "@/features/analyze-migration/model/analyze";
import { describeReviewError } from "@/features/analyze-migration/model/describe-error";
import { type AnalyzeDialogMode, useAnalyzeDialog } from "@/features/analyze-migration/model/dialog-store";
import {
  type CollectedFiles,
  collectDirectory,
  collectDrop,
  collectFileList,
  directoryPicker,
  isSqlFile,
  readPicked,
  validateSources,
} from "@/features/analyze-migration/model/read-files";
import { ReviewOptionsFields } from "@/features/analyze-migration/ui/review-options";
import { Button } from "@/shared/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/ui/dialog";
import { Input } from "@/shared/ui/input";
import { SegmentedControl } from "@/shared/ui/segmented-control";
import { StatusIcon } from "@/shared/ui/status-icon";
import { Textarea } from "@/shared/ui/textarea";

const MODE_OPTIONS: { value: AnalyzeDialogMode; label: string }[] = [
  { value: "files", label: "Files" },
  { value: "paste", label: "Paste SQL" },
];

const ACCEPT = ".sql,.conf,.sum";
const MAX_LISTED = 8;

function ProblemView({ problem }: { problem: ClientProblem }) {
  const copy = describeReviewError({ kind: "client", problem });
  return (
    <div role="alert" className="flex flex-col gap-1 rounded-md border border-status-danger-fg p-2 text-xs">
      <p className="flex items-center gap-1 font-semibold text-status-danger-fg">
        <StatusIcon tone="danger" className="size-3.5" />
        {copy.title}
      </p>
      <p className="text-text-muted">{copy.hint}</p>
      {copy.details.length > 0 && (
        <ul className="font-mono text-2xs text-text-primary">
          {copy.details.map((d) => (
            <li key={d}>{d}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

function FilesMode({ picked, setPicked }: { picked: CollectedFiles | null; setPicked: (c: CollectedFiles) => void }) {
  const filesRef = useRef<HTMLInputElement>(null);
  const folderRef = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);
  const picker = directoryPicker();

  useEffect(() => {
    // Not in React's input props; the multi-file fallback for browsers without showDirectoryPicker.
    folderRef.current?.setAttribute("webkitdirectory", "");
  }, []);

  const chooseFolder = async () => {
    if (!picker) {
      folderRef.current?.click();
      return;
    }
    try {
      setPicked(await collectDirectory(await picker({ mode: "read", id: "dbscope-migrations" })));
    } catch (error) {
      // The user closed the picker; anything else falls back to the plain input.
      if (!(error instanceof DOMException && error.name === "AbortError")) folderRef.current?.click();
    }
  };

  const onDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    event.stopPropagation();
    setOver(false);
    void collectDrop(event.dataTransfer).then(setPicked);
  };

  const sql = picked?.picked.filter((p) => isSqlFile(p.path)) ?? [];
  return (
    <div className="flex flex-col gap-2">
      {/* biome-ignore lint/a11y/noStaticElementInteractions: a drop target; the buttons inside are the keyboard path. */}
      <div
        data-testid="analyze-dialog-drop"
        onDragOver={(e) => {
          e.preventDefault();
          e.stopPropagation();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={onDrop}
        className={
          over
            ? "flex flex-col items-center gap-2 rounded-md border-2 border-dashed border-accent-fg bg-surface-selected p-4 text-center"
            : "flex flex-col items-center gap-2 rounded-md border-2 border-dashed border-border-control p-4 text-center"
        }
      >
        <p className="text-xs text-text-muted">
          Drop .sql files or a migrations folder here, or anywhere on the workbench.
        </p>
        <div className="flex flex-wrap justify-center gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => filesRef.current?.click()}>
            <FileUp aria-hidden />
            Choose files…
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={() => void chooseFolder()}>
            <FolderOpen aria-hidden />
            Choose a folder…
          </Button>
        </div>
        <p className="text-xs text-text-muted">
          Rails, Django or Alembic? Run <code className="font-mono">dbscope migrate capture</code> to get the SQL they
          run, then drop that folder here.
        </p>
        <input
          ref={filesRef}
          type="file"
          multiple
          accept={ACCEPT}
          className="sr-only"
          tabIndex={-1}
          aria-hidden
          data-testid="migration-file-input"
          onChange={(e) => e.target.files && setPicked(collectFileList(e.target.files))}
        />
        <input
          ref={folderRef}
          type="file"
          multiple
          className="sr-only"
          tabIndex={-1}
          aria-hidden
          onChange={(e) => e.target.files && setPicked(collectFileList(e.target.files))}
        />
      </div>
      {picked && (
        <div aria-live="polite" className="flex flex-col gap-1 text-xs">
          <p className="text-text-primary">
            {sql.length} SQL file{sql.length === 1 ? "" : "s"}
            {picked.picked.length > sql.length ? ` and ${picked.picked.length - sql.length} config files` : ""}
            {picked.ignored.length > 0 ? `; ${picked.ignored.length} other files ignored` : ""}
          </p>
          <ul className="flex max-h-32 flex-col overflow-y-auto font-mono text-2xs text-text-muted">
            {picked.picked.slice(0, MAX_LISTED).map((p) => (
              <li key={p.path} className="truncate">
                {p.path}
              </li>
            ))}
            {picked.picked.length > MAX_LISTED && <li>and {picked.picked.length - MAX_LISTED} more</li>}
          </ul>
        </div>
      )}
    </div>
  );
}

function PasteMode({
  name,
  sql,
  onName,
  onSql,
}: {
  name: string;
  sql: string;
  onName: (v: string) => void;
  onSql: (v: string) => void;
}) {
  const id = useId();
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-col gap-1">
        <label htmlFor={`${id}-name`} className="text-xs font-medium text-text-primary">
          File name
        </label>
        <Input
          id={`${id}-name`}
          aria-describedby={`${id}-name-help`}
          value={name}
          onChange={(e) => onName(e.target.value)}
          className="font-mono"
          spellCheck={false}
        />
        <p id={`${id}-name-help`} className="text-xs text-text-muted">
          The name picks the migration tool's rules, e.g. V42__orders.sql (Flyway) or 0042_orders.up.sql
          (golang-migrate).
        </p>
      </div>
      <div className="flex flex-col gap-1">
        <label htmlFor={`${id}-sql`} className="text-xs font-medium text-text-primary">
          Migration SQL
        </label>
        <Textarea
          id={`${id}-sql`}
          data-testid="paste-migration-sql"
          value={sql}
          onChange={(e) => onSql(e.target.value)}
          spellCheck={false}
          placeholder="ALTER TABLE shop.orders ADD COLUMN note text;"
          className="max-h-72 min-h-40 font-mono text-xs"
        />
      </div>
    </div>
  );
}

function withSqlExtension(name: string): string {
  const trimmed = name.trim() || "pasted.sql";
  return trimmed.toLowerCase().endsWith(".sql") ? trimmed : `${trimmed}.sql`;
}

function AnalyzeForm({ onDone }: { onDone: () => void }) {
  const { mode, setMode } = useAnalyzeDialog();
  const { target, label, profile } = useMigrationTarget();
  const [options, setOptions] = useState<ReviewOptions>(lastReviewOptions);
  const [picked, setPicked] = useState<CollectedFiles | null>(null);
  const [name, setName] = useState("pasted.sql");
  const [sql, setSql] = useState("");
  const [problem, setProblem] = useState<ClientProblem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!target) {
      setProblem({ code: "no_target" });
      return;
    }
    let sources: MigrationSource[];
    let ignored: string[] = [];
    if (mode === "paste") {
      sources = [{ name: withSqlExtension(name), sql }];
      if (!sql.trim()) {
        setProblem({ code: "no_files", ignored: [] });
        return;
      }
    } else {
      if (!picked) {
        setProblem({ code: "no_files", ignored: [] });
        return;
      }
      setBusy(true);
      const read = await readPicked(picked.picked);
      setBusy(false);
      if (read.failed.length > 0) {
        setProblem({ code: "read_failed", names: read.failed });
        return;
      }
      sources = read.sources;
      ignored = picked.ignored;
    }
    const invalid = validateSources(sources, ignored);
    if (invalid) {
      setProblem(invalid);
      return;
    }
    startAnalysis({ sources, ignored, target, targetLabel: label, options });
    onDone();
  };

  return (
    <form onSubmit={(e) => void submit(e)} className="flex min-h-0 flex-col gap-4">
      <SegmentedControl
        aria-label="What to analyze"
        options={MODE_OPTIONS}
        value={mode}
        onValueChange={(m) => {
          setMode(m);
          setProblem(null);
        }}
        size="sm"
      />
      {mode === "files" ? (
        <FilesMode
          picked={picked}
          setPicked={(c) => {
            setPicked(c);
            setProblem(null);
          }}
        />
      ) : (
        <PasteMode name={name} sql={sql} onName={setName} onSql={setSql} />
      )}
      {/* Transaction mode and lock_timeout are PostgreSQL options; other engines read the files as written. */}
      {(!profile || profile.engine === "postgres") && <ReviewOptionsFields value={options} onChange={setOptions} />}
      {problem && <ProblemView problem={problem} />}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy || !target}>
          {busy ? (
            <LoaderCircle aria-hidden className="animate-spin" />
          ) : mode === "paste" ? (
            <ClipboardPaste aria-hidden />
          ) : null}
          Analyze
        </Button>
      </DialogFooter>
      <p className="text-xs text-text-muted">
        {target
          ? `Checked against ${label}. dbscope only reads the catalog; nothing runs against your tables.`
          : "Add a connection or open the sample schema first: migrations are checked against a schema."}
      </p>
    </form>
  );
}

export function AnalyzeMigrationDialog() {
  const { open, setOpen, mode } = useAnalyzeDialog();
  // A new key on each open starts the form fresh.
  const [formKey, setFormKey] = useState(0);
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setFormKey((k) => k + 1);
  }
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{mode === "paste" ? "Paste migration SQL" : "Analyze migration files"}</DialogTitle>
          <DialogDescription>
            dbscope reports the locks, rewrites and breaking changes each statement causes, with a safer way to write
            it. Deterministic, no LLM.
          </DialogDescription>
        </DialogHeader>
        <AnalyzeForm key={formKey} onDone={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  );
}
