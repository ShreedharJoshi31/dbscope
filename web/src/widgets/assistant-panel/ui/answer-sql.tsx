import { Copy, Play, SquarePen } from "lucide-react";
import { useActiveConnection } from "@/entities/connection";
import { openSqlTab } from "@/entities/query";
import { useQueryRunner } from "@/features/run-query";
import { announce } from "@/shared/lib/announcer";
import { copyText } from "@/shared/lib/clipboard";
import { Button } from "@/shared/ui/button";
import { SqlCode } from "@/shared/ui/sql-code";
import { SqlDiff } from "@/widgets/assistant-panel/ui/sql-diff";

interface AnswerSqlProps {
  sql: string;
  previousSql: string | null;
  busy: boolean;
}

/** The generated SQL, read-only and highlighted, with Copy, Open in editor and Re-run (UX rule 5). */
export function AnswerSql({ sql, previousSql, busy }: AnswerSqlProps) {
  const { profile } = useActiveConnection();
  const runner = useQueryRunner();
  return (
    <section data-section="sql" aria-label="Generated SQL" className="flex flex-col gap-1.5">
      <SqlCode sql={sql} label="Generated SQL" />
      <div className="flex flex-wrap gap-1">
        <Button
          variant="ghost"
          size="xs"
          onClick={() => void copyText(sql).then((ok) => announce(ok ? "Copied the SQL." : "Copy failed."))}
        >
          <Copy aria-hidden />
          Copy
        </Button>
        <Button
          variant="ghost"
          size="xs"
          onClick={() => {
            openSqlTab(sql);
            announce("Opened the SQL in a new editor tab.");
          }}
        >
          <SquarePen aria-hidden />
          Open in editor
        </Button>
        <Button
          variant="ghost"
          size="xs"
          disabled={!profile || busy}
          title="Run this SQL again through the guarded executor; the result opens in the dock"
          onClick={() => profile && runner.request({ profile, sql, kind: "query", analyze: false, tabId: null })}
        >
          <Play aria-hidden />
          Re-run
        </Button>
      </div>
      {previousSql !== null && <SqlDiff before={previousSql} after={sql} />}
    </section>
  );
}
