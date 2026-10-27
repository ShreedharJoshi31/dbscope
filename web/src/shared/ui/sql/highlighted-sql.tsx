import "@/shared/ui/sql/sql-code.css";
import { PostgreSQL } from "@codemirror/lang-sql";
import { highlightCode } from "@lezer/highlight";
import { type ReactNode, useMemo } from "react";
import { sqlHighlighter } from "@/shared/ui/sql/highlighter";

/** Highlights SQL into React text nodes: no HTML strings, so model output stays plain text. */
function highlight(sql: string): ReactNode[] {
  const out: ReactNode[] = [];
  const tree = PostgreSQL.language.parser.parse(sql);
  highlightCode(
    sql,
    tree,
    sqlHighlighter,
    (text, classes) => {
      out.push(
        classes ? (
          <span key={out.length} className={classes}>
            {text}
          </span>
        ) : (
          text
        ),
      );
    },
    () => out.push("\n"),
  );
  return out;
}

/** Lazily loaded part of SqlCode (pulls in the SQL parser). */
export default function HighlightedSql({ sql }: { sql: string }) {
  const nodes = useMemo(() => highlight(sql), [sql]);
  return <>{nodes}</>;
}
