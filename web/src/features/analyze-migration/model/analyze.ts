import { useActiveConnection } from "@/entities/connection";
import {
  canvasRelationFor,
  columnChanges,
  DEFAULT_REVIEW_OPTIONS,
  impactByRelation,
  initialSelection,
  type MigrationReview,
  type MigrationSource,
  type ReviewOptions,
  relationChanges,
  toReviewModel,
  useReviewStore,
} from "@/entities/migration-finding";
import { ERD_TAB, useSqlTabs } from "@/entities/query";
import { type DiffOverlay, showMigrationDiff, useCanvasStore, useWorkspaceStore } from "@/entities/schema";
import { reviewTitle, validateSources } from "@/features/analyze-migration/model/read-files";
import {
  analyzeMigrationFolder,
  analyzeMigrations,
  isApiError,
  type MigrationTarget,
  migrationFolderState,
  type Profile,
} from "@/shared/api";
import { announce } from "@/shared/lib/announcer";

export interface MigrationTargetInfo {
  target: MigrationTarget | null;
  /** "orders-dev" or "the sample schema". */
  label: string;
  profile: Profile | undefined;
}

/** Where a drop is analyzed: the sample schema in the sample workspace, else the active connection. */
export function useMigrationTarget(): MigrationTargetInfo {
  const sample = useWorkspaceStore((s) => s.source === "sample");
  const { profile } = useActiveConnection();
  if (sample) return { target: { kind: "sample" }, label: "the sample schema", profile: undefined };
  if (profile) return { target: { kind: "profile", profileId: profile.id }, label: profile.name, profile };
  return { target: null, label: "no connection", profile: undefined };
}

let lastOptions: ReviewOptions = DEFAULT_REVIEW_OPTIONS;

/** Options for the next drop: whatever the user chose last in this session. */
export function lastReviewOptions(): ReviewOptions {
  return lastOptions;
}

const controllers = new Map<string, AbortController>();
let nextId = 1;

function isAbort(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

async function run(id: string): Promise<void> {
  const store = useReviewStore.getState();
  const review = store.reviews[id];
  if (!review) return;
  controllers.get(id)?.abort();
  // A folder review's files are read by the agent, so there is nothing to check here.
  const problem = !review.target
    ? { code: "no_target" as const }
    : review.folder
      ? null
      : validateSources(review.sources, review.ignored);
  if (problem) {
    store.patch(id, { status: "error", error: { kind: "client", problem }, model: null });
    announce(`${review.title}: nothing was analyzed.`);
    return;
  }
  const target = review.target;
  if (!target) return;
  const ac = new AbortController();
  controllers.set(id, ac);
  store.patch(id, { status: "running", error: null, startedAt: Date.now() });
  try {
    const options = {
      txMode: review.options.txMode,
      ...(review.options.assumeLockTimeout ? { assumeLockTimeout: true } : {}),
    };
    let report: Awaited<ReturnType<typeof analyzeMigrations>>;
    let sources = review.sources;
    if (review.folder && target.kind === "profile") {
      const res = await analyzeMigrationFolder(
        target.profileId,
        { pendingOnly: review.folder.pendingOnly, ...options },
        ac.signal,
      );
      report = res.report;
      sources = res.sources;
    } else {
      report = await analyzeMigrations(target, { files: review.sources, ...options }, ac.signal);
    }
    if (controllers.get(id) !== ac) return;
    const model = toReviewModel(report, sources);
    // Keep the statement the user was looking at when a watched folder re-runs.
    const selected =
      review.folder && review.model
        ? Math.min(review.selected, Math.max(0, model.statements.length - 1))
        : initialSelection(model);
    useReviewStore.getState().patch(id, { status: "done", model, error: null, selected, sources });
    useReviewStore.getState().setCurrent(id);
    // A diff already on the canvas from this review follows the new result.
    if (useCanvasStore.getState().diff?.sourceId === id) showMigrationDiff(diffOverlayFor(id, review.title));
    const findings = model.findingCount;
    announce(
      `${review.title} analyzed: overall ${model.rating}, ${model.statements.length} statements, ${findings} finding${findings === 1 ? "" : "s"}.`,
    );
  } catch (error) {
    if (isAbort(error) || controllers.get(id) !== ac) return;
    const apiError = isApiError(error) ? error : null;
    useReviewStore.getState().patch(id, {
      status: "error",
      model: null,
      error: apiError
        ? { kind: "api", error: apiError }
        : { kind: "client", problem: { code: "read_failed", names: review.sources.map((s) => s.name) } },
    });
    announce(`${review.title}: analysis failed.`);
  } finally {
    if (controllers.get(id) === ac) controllers.delete(id);
  }
}

export interface StartAnalysis {
  sources: MigrationSource[];
  ignored?: string[];
  target: MigrationTarget | null;
  targetLabel: string;
  options?: ReviewOptions;
}

/** Opens a migration tab for the files and analyzes them. Returns the tab (and review) id. */
export function startAnalysis({ sources, ignored = [], target, targetLabel, options }: StartAnalysis): string {
  const id = `m${nextId++}`;
  const chosen = options ?? lastOptions;
  lastOptions = chosen;
  const title = reviewTitle(sources);
  const review: MigrationReview = {
    id,
    title,
    scope: useSqlTabs.getState().scope ?? "",
    target,
    targetLabel,
    sources,
    ignored,
    options: chosen,
    status: "running",
    model: null,
    error: null,
    selected: 0,
    reveal: null,
    startedAt: Date.now(),
  };
  useReviewStore.getState().add(review);
  useSqlTabs.getState().openDocTab({ id, title, kind: "migration" });
  announce(`Analyzing ${title} against ${targetLabel}.`);
  void run(id);
  return id;
}

/**
 * Opens a migration tab for the connection's migrations folder, read by the
 * agent; pendingOnly leaves out applied files and watch re-checks on save.
 */
export function startFolderAnalysis(
  profile: Profile,
  { pendingOnly, watch, options }: { pendingOnly: boolean; watch: boolean; options?: ReviewOptions },
): string {
  const id = `m${nextId++}`;
  const chosen = options ?? lastOptions;
  lastOptions = chosen;
  const dir = profile.migrationsDir ?? "";
  const title = dir.split(/[\\/]/).filter(Boolean).at(-1) ?? "migrations";
  useReviewStore.getState().add({
    id,
    title,
    scope: useSqlTabs.getState().scope ?? "",
    target: { kind: "profile", profileId: profile.id },
    targetLabel: profile.name,
    sources: [],
    ignored: [],
    options: chosen,
    status: "running",
    model: null,
    error: null,
    selected: 0,
    reveal: null,
    startedAt: Date.now(),
    folder: { pendingOnly, watch },
  });
  useSqlTabs.getState().openDocTab({ id, title, kind: "migration" });
  announce(`Analyzing the migrations folder of ${profile.name}${pendingOnly ? ", pending files only" : ""}.`);
  void run(id);
  if (watch) startWatching(id, profile.id);
  return id;
}

const WATCH_MS = 2000;
const watchers = new Map<string, ReturnType<typeof setInterval>>();

/** Polls the folder's fingerprint and re-runs the review when a migration file is saved, added or removed. */
function startWatching(id: string, profileId: string): void {
  stopWatching(id);
  let last: string | null = null;
  let busy = false;
  const timer = setInterval(() => {
    const review = useReviewStore.getState().reviews[id];
    if (!review?.folder?.watch) {
      stopWatching(id);
      return;
    }
    if (busy) return;
    busy = true;
    migrationFolderState(profileId)
      .then(({ fingerprint }) => {
        if (last !== null && fingerprint !== last && review.status !== "running") {
          announce("A migration file changed; checking again.");
          void run(id);
        }
        last = fingerprint;
      })
      // A folder that is briefly unreadable (an editor's atomic save) is tried again on the next tick.
      .catch(() => {})
      .finally(() => {
        busy = false;
      });
  }, WATCH_MS);
  watchers.set(id, timer);
}

function stopWatching(id: string): void {
  const timer = watchers.get(id);
  if (timer !== undefined) clearInterval(timer);
  watchers.delete(id);
}

/** Turns watching of a folder review on or off. */
export function setFolderWatch(id: string, watch: boolean): void {
  const review = useReviewStore.getState().reviews[id];
  if (!review?.folder || review.target?.kind !== "profile") return;
  useReviewStore.getState().patch(id, { folder: { ...review.folder, watch } });
  if (watch) startWatching(id, review.target.profileId);
  else stopWatching(id);
}

/** Runs the same files again, e.g. after changing the transaction mode. */
export function reanalyze(id: string, options?: ReviewOptions): void {
  if (options) {
    lastOptions = options;
    useReviewStore.getState().patch(id, { options });
  }
  void run(id);
}

export function cancelAnalysis(id: string): void {
  const ac = controllers.get(id);
  if (!ac) return;
  ac.abort();
  controllers.delete(id);
  useReviewStore.getState().patch(id, { status: "error", error: { kind: "client", problem: { code: "cancelled" } } });
  announce("Analysis cancelled.");
}

/** Closes a migration tab: stops its request, forgets the report and clears its canvas overlay. */
export function closeMigrationReview(id: string): void {
  stopWatching(id);
  controllers.get(id)?.abort();
  controllers.delete(id);
  if (useCanvasStore.getState().diff?.sourceId === id) useCanvasStore.getState().setDiff(null);
  useReviewStore.getState().remove(id);
  useSqlTabs.getState().closeTab(id);
}

export function diffOverlayFor(id: string, label?: string): DiffOverlay {
  const review = useReviewStore.getState().reviews[id];
  const model = review?.model;
  if (!model) return { sourceId: id, label: label ?? "", relations: {}, columns: {}, impact: {} };
  return {
    sourceId: id,
    label: label ?? review.title,
    relations: relationChanges(model.diff),
    columns: columnChanges(model.diff),
    impact: impactByRelation(model.report),
  };
}

/**
 * Switches to the ERD tab, colours the migration's changes and, for a
 * statement, selects (and pans to) the changed relation it locks so its
 * dependents light up. Nothing is added to React Flow's nodes or edges.
 */
export function showReviewOnCanvas(id: string, statementIndex: number | null = null): void {
  const review = useReviewStore.getState().reviews[id];
  if (!review?.model) return;
  const diff = diffOverlayFor(id);
  showMigrationDiff(diff);
  useSqlTabs.getState().setActive(ERD_TAB);
  const statement = statementIndex === null ? undefined : review.model.statements[statementIndex];
  // Wait a frame so the ERD panel is visible and React Flow measures it before panning.
  requestAnimationFrame(() => {
    const canvas = useCanvasStore.getState();
    const onCanvas = (key: string) => Boolean(canvas.graph?.byKey.has(key));
    const changed = Object.keys(diff.relations).length;
    if (!statement) {
      announce(`Showing ${changed} changed relation${changed === 1 ? "" : "s"} from ${review.title} on the canvas.`);
      return;
    }
    const relation = canvasRelationFor(statement, diff.relations, onCanvas);
    if (!relation) {
      announce(
        statement.relations.length > 0
          ? `${statement.relations.join(", ")} is not on the canvas; new relations are listed in the migration tab.`
          : "This statement locks no relation on the canvas.",
      );
      return;
    }
    canvas.select(relation, { reveal: true });
    const dependents = diff.impact[relation]?.length ?? 0;
    announce(
      diff.relations[relation]
        ? `Showing ${relation} (${diff.relations[relation]}) and ${dependents} dependent${dependents === 1 ? "" : "s"} on the canvas.`
        : `Showing ${relation} on the canvas.`,
    );
  });
}

/** The migration review in the active centre tab, if any. */
export function activeReview(): MigrationReview | undefined {
  const { active, docTabs } = useSqlTabs.getState();
  if (!docTabs.some((t) => t.id === active)) return undefined;
  return useReviewStore.getState().reviews[active];
}

/** j / k: moves the selection in the active migration tab and focuses the statement's gutter marker. */
export function stepStatement(direction: 1 | -1): boolean {
  const review = activeReview();
  const count = review?.model?.statements.length ?? 0;
  if (!review || count === 0) return false;
  useReviewStore.getState().select(review.id, review.selected + direction, "marker");
  return true;
}

/** From the Migration risk tab: opens the statement's migration tab and focuses its marker. */
export function openStatement(id: string, statementIndex: number): void {
  const review = useReviewStore.getState().reviews[id];
  if (!review) return;
  useSqlTabs.getState().setActive(id);
  useReviewStore.getState().select(id, statementIndex, "marker");
  const s = review.model?.statements[statementIndex];
  if (s) announce(`${s.file}, line ${s.line}: ${s.kind}, ${s.rating}.`);
}
