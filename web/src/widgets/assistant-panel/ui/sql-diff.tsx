import { cn } from "cn";
import { diffWords, hasChanges } from "@/shared/lib/diff";

/** A small word diff of the SQL after an edited assumption: removed struck through, added underlined. */
export function SqlDiff({ before, after }: { before: string; after: string }) {
  const parts = diffWords(before, after);
  if (!hasChanges(parts)) {
    return <p className="text-2xs text-text-muted">The SQL did not change.</p>;
  }
  return (
    <figure aria-label="Changes from the previous SQL" className="m-0 flex flex-col gap-1">
      <figcaption className="text-2xs text-text-muted">
        Changed from the previous answer: <span className="text-status-danger-fg line-through">removed</span>,{" "}
        <span className="text-status-success-fg underline">added</span>
      </figcaption>
      <pre
        data-testid="sql-diff"
        className="overflow-x-auto rounded-md border border-border-default bg-surface-subtle p-2 font-mono text-2xs whitespace-pre-wrap"
      >
        {parts.map((p, i) => {
          const key = `${i}${p.type}`;
          if (p.type === "same") return <span key={key}>{p.text}</span>;
          const Tag = p.type === "added" ? "ins" : "del";
          return (
            <Tag
              key={key}
              className={cn(
                "no-underline",
                p.type === "added"
                  ? "bg-status-success-subtle text-status-success-fg underline"
                  : "bg-status-danger-subtle text-status-danger-fg line-through",
              )}
            >
              <span className="sr-only">{p.type === "added" ? "[added: " : "[removed: "}</span>
              {p.text}
              <span className="sr-only">]</span>
            </Tag>
          );
        })}
      </pre>
    </figure>
  );
}
