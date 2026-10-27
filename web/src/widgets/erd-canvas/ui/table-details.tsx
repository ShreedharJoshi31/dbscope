import { ArrowDownLeft, ArrowUpRight, X } from "lucide-react";
import { type ReactNode, useEffect, useRef } from "react";
import {
  BadgeList,
  columnBadges,
  type FkEdge,
  KeyIcon,
  KIND_META,
  KindIcon,
  rowEstimateLabel,
  type SchemaGraph,
  useCanvasStore,
  visibleColumns,
} from "@/entities/schema";
import { confidenceLabel, InferredFkActions } from "@/features/review-inferred-fk";
import type { Relation } from "@/shared/api";
import { formatGrouped } from "@/shared/lib/format-number";
import { Button } from "@/shared/ui/button";

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-1">
      <h4 className="text-2xs font-semibold tracking-wide text-text-muted uppercase">{title}</h4>
      {children}
    </section>
  );
}

function RelationLink({ id, label }: { id: string; label: ReactNode }) {
  return (
    <button
      type="button"
      className="truncate text-left font-mono text-xs text-text-link hover:underline"
      onClick={() => useCanvasStore.getState().select(id, { reveal: true })}
    >
      {label}
    </button>
  );
}

function FkItem({ edge, direction }: { edge: FkEdge; direction: "out" | "in" }) {
  const cols = edge.pairs.map((p) => p.column).join(", ");
  const refs = edge.pairs.map((p) => p.refColumn).join(", ");
  const other = direction === "out" ? edge.parent : edge.child;
  const Icon = direction === "out" ? ArrowUpRight : ArrowDownLeft;
  const unreviewed = edge.inferred && !edge.accepted;
  return (
    <li className="flex flex-col gap-1 rounded-md border border-border-default p-2">
      <div className="flex items-center gap-1 text-xs">
        <Icon aria-hidden className="size-3.5 shrink-0 text-text-muted" />
        <span className="font-mono">{direction === "out" ? `(${cols})` : `(${refs})`}</span>
        <span className="text-text-muted">{direction === "out" ? "→" : "←"}</span>
        <RelationLink id={other} label={`${other}(${direction === "out" ? refs : cols})`} />
      </div>
      <p className="text-2xs text-text-muted">
        {unreviewed
          ? `Inferred, confidence ${confidenceLabel(edge.confidence)}`
          : edge.accepted
            ? "Inferred, accepted"
            : `${edge.fk.name}${edge.fk.onDelete && edge.fk.onDelete !== "no action" ? ` · on delete ${edge.fk.onDelete}` : ""}${edge.fk.validated ? "" : " · not validated"}`}
      </p>
      {unreviewed && direction === "out" && <InferredFkActions edge={edge} />}
    </li>
  );
}

function Columns({ relation }: { relation: Relation }) {
  return (
    <ul className="flex flex-col">
      {visibleColumns(relation, false).map(({ column, flags }) => (
        <li key={column.name} className="flex flex-col border-b border-border-default py-1 last:border-b-0">
          <div className="flex items-center gap-1 text-xs">
            <KeyIcon flags={flags} />
            <span className="font-mono">{column.name}</span>
            <span className="ml-auto truncate font-mono text-2xs text-text-muted">{column.type}</span>
            <BadgeList badges={columnBadges(flags, column.generatedKind)} />
          </div>
          {(column.default || column.generated || column.comment) && (
            <p className="pl-8 font-mono text-2xs break-words text-text-muted">
              {column.generated ? `generated: ${column.generated}` : column.default ? `default ${column.default}` : ""}
              {column.comment && <span className="block font-sans">{column.comment}</span>}
            </p>
          )}
        </li>
      ))}
    </ul>
  );
}

function Body({ graph, id }: { graph: SchemaGraph; id: string }) {
  const relation = graph.byKey.get(id);
  if (!relation) return null;
  const outgoing = graph.outgoing.get(id) ?? [];
  const incoming = (graph.incoming.get(id) ?? []).filter((e) => !e.selfRef);
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-3">
      <p className="text-xs text-text-muted">
        {KIND_META[relation.kind].label}
        {relation.stub ? " outside the selected schemas" : ` · ${rowEstimateLabel(relation.rowEstimate)}`}
        {relation.pages > 0 && ` · ${formatGrouped(relation.pages)} pages`}
      </p>
      {relation.comment && <p className="text-xs">{relation.comment}</p>}
      {relation.kind === "partitioned_table" && (
        <Section title="Partitions">
          <p className="font-mono text-xs">{relation.partitionKey}</p>
          <ul className="font-mono text-xs text-text-muted">
            {(relation.partitions ?? []).map((p) => (
              <li key={`${p.schema}.${p.name}`}>
                {p.schema}.{p.name}
              </li>
            ))}
          </ul>
        </Section>
      )}
      {relation.dependsOn && relation.dependsOn.length > 0 && (
        <Section title="Reads from">
          <ul className="flex flex-col">
            {relation.dependsOn.map((q) => {
              const key = `${q.schema}.${q.name}`;
              return (
                <li key={key}>
                  {graph.byKey.has(key) ? (
                    <RelationLink id={key} label={key} />
                  ) : (
                    <span className="font-mono text-xs">{key}</span>
                  )}
                </li>
              );
            })}
          </ul>
        </Section>
      )}
      {!relation.stub && (
        <Section title={`Columns (${relation.columns?.length ?? 0})`}>
          <Columns relation={relation} />
        </Section>
      )}
      {outgoing.length > 0 && (
        <Section title={`Foreign keys (${outgoing.length})`}>
          <ul className="flex flex-col gap-1">
            {outgoing.map((e) => (
              <FkItem key={e.id} edge={e} direction="out" />
            ))}
          </ul>
        </Section>
      )}
      {incoming.length > 0 && (
        <Section title={`Referenced by (${incoming.length})`}>
          <ul className="flex flex-col gap-1">
            {incoming.map((e) => (
              <FkItem key={e.id} edge={e} direction="in" />
            ))}
          </ul>
        </Section>
      )}
      {(relation.indexes ?? []).length > 0 && (
        <Section title="Indexes">
          <ul className="flex flex-col font-mono text-2xs">
            {(relation.indexes ?? []).map((ix) => (
              <li key={ix.name} className="truncate" title={ix.partial ? `WHERE ${ix.partial}` : undefined}>
                {ix.name} ({ix.columns.join(", ")}){ix.unique ? " unique" : ""} {ix.method}
                {ix.valid ? "" : " INVALID"}
              </li>
            ))}
          </ul>
        </Section>
      )}
      {(relation.checks ?? []).length > 0 && (
        <Section title="Checks">
          <ul className="flex flex-col font-mono text-2xs">
            {(relation.checks ?? []).map((c) => (
              <li key={c.name} className="break-words">
                {c.expr}
                {c.validated ? "" : " (not validated)"}
              </li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  );
}

/** Everything known about the selected table. Opens with Enter or a double-click; Esc closes. */
export function TableDetails({ onClose }: { onClose: () => void }) {
  const open = useCanvasStore((s) => s.detailsOpen);
  const id = useCanvasStore((s) => s.selectedId);
  const graph = useCanvasStore((s) => s.graph);
  const headingRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    if (open) headingRef.current?.focus();
  }, [open]);

  if (!open || !id || !graph?.byKey.has(id)) return null;
  const relation = graph.byKey.get(id) as Relation;

  return (
    <aside
      aria-labelledby="erd-details-title"
      data-testid="table-details"
      className="absolute top-0 right-0 bottom-0 z-20 flex w-80 flex-col border-l border-border-default bg-surface-raised shadow-md"
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onClose();
        }
      }}
    >
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-border-default px-3">
        <KindIcon kind={relation.kind} />
        <h3
          id="erd-details-title"
          ref={headingRef}
          tabIndex={-1}
          className="min-w-0 truncate font-mono text-sm font-semibold"
        >
          {id}
        </h3>
        <Button variant="ghost" size="icon-xs" className="ml-auto" aria-label="Close details" onClick={onClose}>
          <X aria-hidden />
        </Button>
      </div>
      <Body graph={graph} id={id} />
    </aside>
  );
}
