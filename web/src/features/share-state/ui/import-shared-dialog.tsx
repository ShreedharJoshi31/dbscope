import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, FileUp, LoaderCircle, Users } from "lucide-react";
import { type DragEvent, useId, useRef, useState } from "react";
import { useActiveConnection } from "@/entities/connection";
import { applyImport, readTeamFile, useImportSharedDialog } from "@/features/share-state/model/share-state";
import {
  type Profile,
  previewShared,
  type ShareChange,
  type ShareMode,
  type SharePreview,
  type ShareSection,
} from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/shared/ui/collapsible";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/ui/dialog";
import { SegmentedControl } from "@/shared/ui/segmented-control";
import { StatusIcon } from "@/shared/ui/status-icon";

const ORDER: ShareChange[] = ["added", "changed", "removed", "kept", "unchanged"];

function summary(s: ShareSection): string {
  const parts = ORDER.filter((c) => (s.counts[c] ?? 0) > 0).map((c) => `${s.counts[c]} ${c}`);
  return parts.length > 0 ? parts.join(" · ") : "nothing";
}

function Section({ title, section, layouts }: { title: string; section: ShareSection; layouts: boolean }) {
  const shown = section.items.filter((i) => i.change !== "unchanged");
  return (
    <Collapsible className="rounded-md border border-border-default">
      <CollapsibleTrigger
        disabled={shown.length === 0}
        className="group flex w-full items-center gap-2 px-3 py-2 text-left text-sm disabled:cursor-default"
      >
        <ChevronRight
          aria-hidden
          className="size-3.5 text-text-muted transition-transform group-data-[panel-open]:rotate-90 group-disabled:invisible"
        />
        <span className="font-medium">{title}</span>
        <span className="text-xs text-text-muted">{summary(section)}</span>
        {section.orphans > 0 && (
          <span className="ml-auto text-xs text-status-warn-fg">{section.orphans} not in this database</span>
        )}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <ul className="flex max-h-48 flex-col gap-0.5 overflow-y-auto border-t border-border-default px-3 py-2 text-xs">
          {shown.map((item) => (
            <li key={`${item.layout ?? ""}|${item.key}`} className="flex flex-wrap items-baseline gap-x-2">
              <span className="w-16 shrink-0 text-text-muted">{item.change}</span>
              <code className="font-mono break-all">{item.key}</code>
              {layouts && item.layout && <span className="text-text-muted">in {item.layout}</span>}
              {item.detail && <span className="text-text-muted">{item.detail}</span>}
              {item.orphan && <span className="text-status-warn-fg">not in this database</span>}
            </li>
          ))}
        </ul>
      </CollapsibleContent>
    </Collapsible>
  );
}

function PreviewView({ preview }: { preview: SharePreview }) {
  const layouts = new Set(preview.tables.items.map((i) => i.layout)).size > 1;
  return (
    <div className="flex flex-col gap-2">
      <Section title="Table positions" section={preview.tables} layouts={layouts} />
      <Section title="Inferred foreign keys" section={preview.foreignKeys} layouts={false} />
      <Section title="Glossary terms" section={preview.glossary} layouts={false} />
      {!preview.schemaChecked && (
        <p className="text-xs text-text-muted">
          dbscope could not read this database now, so entries for tables or keys it lacks are not flagged. They are
          imported either way and are simply unused.
        </p>
      )}
    </div>
  );
}

type Stage =
  | { step: "pick"; error?: string }
  | { step: "preview"; name: string; data: unknown; id: number }
  | { step: "done"; name: string; result: SharePreview; previous: unknown };

function ImportFlow({ profile, onDone }: { profile: Profile; onDone: () => void }) {
  const client = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [stage, setStage] = useState<Stage>({ step: "pick" });
  const [mode, setMode] = useState<ShareMode>("merge");
  const [busy, setBusy] = useState<"import" | "undo" | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [over, setOver] = useState(false);
  const modeId = useId();

  const preview = useQuery({
    queryKey: ["share-preview", profile.id, stage.step === "preview" ? stage.id : 0, mode],
    queryFn: ({ signal }) => previewShared(profile.id, stage.step === "preview" ? stage.data : null, mode, signal),
    enabled: stage.step === "preview",
    gcTime: 0,
    retry: false,
  });

  const pick = async (file: File | undefined) => {
    if (!file) return;
    const read = await readTeamFile(file);
    if ("error" in read) setStage({ step: "pick", error: read.error });
    else setStage({ step: "preview", name: file.name, data: read.data, id: Date.now() });
  };

  const onImport = async () => {
    if (stage.step !== "preview") return;
    setBusy("import");
    setActionError(null);
    try {
      const { result, previous } = await applyImport(client, profile.id, stage.data, mode, { snapshot: true });
      setStage({ step: "done", name: stage.name, result, previous });
      announce(`Imported ${stage.name} into ${profile.name}.`);
    } catch (error) {
      setActionError(error instanceof Error ? error.message : "The import failed.");
    } finally {
      setBusy(null);
    }
  };

  const onUndo = async () => {
    if (stage.step !== "done") return;
    setBusy("undo");
    setActionError(null);
    try {
      await applyImport(client, profile.id, stage.previous, "replace");
      announce("The import was undone.");
      onDone();
    } catch (error) {
      setActionError(error instanceof Error ? error.message : "Undo failed.");
    } finally {
      setBusy(null);
    }
  };

  const onDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setOver(false);
    void pick(event.dataTransfer.files[0]);
  };

  if (stage.step === "done") {
    const r = stage.result;
    return (
      <div className="flex flex-col gap-3">
        <p role="status" className="flex items-center gap-1.5 text-sm">
          <StatusIcon tone="success" />
          Imported {stage.name} into {profile.name}.
        </p>
        <ul className="text-xs text-text-muted">
          <li>Table positions: {summary(r.tables)}</li>
          <li>Inferred foreign keys: {summary(r.foreignKeys)}</li>
          <li>Glossary terms: {summary(r.glossary)}</li>
        </ul>
        {actionError && <ErrorLine message={actionError} />}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => void onUndo()} disabled={busy !== null}>
            {busy === "undo" && <LoaderCircle aria-hidden className="animate-spin" />}
            Undo import
          </Button>
          <Button type="button" onClick={onDone} disabled={busy !== null}>
            Done
          </Button>
        </DialogFooter>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      {stage.step === "pick" ? (
        // biome-ignore lint/a11y/noStaticElementInteractions: a drop target; the button inside is the keyboard path.
        <div
          data-testid="import-shared-drop"
          onDragOver={(e) => {
            e.preventDefault();
            setOver(true);
          }}
          onDragLeave={() => setOver(false)}
          onDrop={onDrop}
          className={
            over
              ? "flex flex-col items-center gap-2 rounded-md border-2 border-dashed border-accent-fg bg-surface-selected p-6 text-center"
              : "flex flex-col items-center gap-2 rounded-md border-2 border-dashed border-border-control p-6 text-center"
          }
        >
          <p className="text-xs text-text-muted">Drop a .dbscope.json file here, or</p>
          <Button type="button" variant="outline" size="sm" onClick={() => input.current?.click()}>
            <FileUp aria-hidden />
            Choose file…
          </Button>
          <input
            ref={input}
            type="file"
            accept=".json,application/json"
            className="sr-only"
            tabIndex={-1}
            aria-hidden
            data-testid="import-shared-input"
            onChange={(e) => void pick(e.target.files?.[0])}
          />
        </div>
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm">
              <code className="font-mono">{stage.name}</code>
            </p>
            <span id={`${modeId}-label`} className="sr-only">
              How to import
            </span>
            <SegmentedControl
              size="sm"
              aria-labelledby={`${modeId}-label`}
              value={mode}
              onValueChange={setMode}
              options={[
                { value: "merge", label: "Merge" },
                { value: "replace", label: "Replace" },
              ]}
            />
          </div>
          <p className="text-xs text-text-muted">
            {mode === "merge"
              ? "Entries in the file win; your entries the file does not mention stay."
              : `${profile.name} ends up exactly as the file: your entries the file does not mention are removed.`}
          </p>
          {preview.isPending && (
            <p role="status" className="flex items-center gap-2 text-sm text-text-muted">
              <LoaderCircle aria-hidden className="size-4 animate-spin" />
              Comparing with {profile.name}…
            </p>
          )}
          {preview.isError && <ErrorLine message={preview.error.message} />}
          {preview.isSuccess && <PreviewView preview={preview.data} />}
        </>
      )}
      {stage.step === "pick" && stage.error && <ErrorLine message={stage.error} />}
      {actionError && <ErrorLine message={actionError} />}
      <DialogFooter>
        {stage.step === "preview" && (
          <Button type="button" variant="ghost" className="mr-auto" onClick={() => setStage({ step: "pick" })}>
            Choose another file
          </Button>
        )}
        <Button type="button" variant="outline" onClick={onDone}>
          Cancel
        </Button>
        {stage.step === "preview" && (
          <Button
            type="button"
            variant={mode === "replace" ? "destructive" : "default"}
            disabled={!preview.isSuccess || busy !== null}
            onClick={() => void onImport()}
          >
            {busy === "import" && <LoaderCircle aria-hidden className="animate-spin" />}
            {mode === "replace" ? "Replace with file" : "Import"}
          </Button>
        )}
      </DialogFooter>
    </div>
  );
}

function ErrorLine({ message }: { message: string }) {
  return (
    <p role="alert" className="flex items-start gap-1 text-xs text-status-danger-fg">
      <StatusIcon tone="danger" className="size-3.5" />
      {message}
    </p>
  );
}

/** Imports a teammate's layouts, foreign key decisions and glossary (docs/SHARING.md). */
export function ImportSharedDialog() {
  const { open, setOpen } = useImportSharedDialog();
  const { profile } = useActiveConnection();
  const [session, setSession] = useState(0);
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setSession((n) => n + 1);
  }
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="flex max-h-[90vh] flex-col overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-1.5">
            <Users aria-hidden className="size-4" />
            Import a team file
          </DialogTitle>
          <DialogDescription>
            Canvas layouts, inferred foreign key decisions and glossary terms a teammate exported
            {profile ? ` for ${profile.name}` : ""}. Nothing about the database itself changes.
          </DialogDescription>
        </DialogHeader>
        {profile ? (
          <ImportFlow key={session} profile={profile} onDone={() => setOpen(false)} />
        ) : (
          <p className="text-sm text-text-muted">Choose a connection first; each one has its own team file.</p>
        )}
      </DialogContent>
    </Dialog>
  );
}
