import { useRunPrompt } from "@/features/run-query/model/prompt-store";
import { useQueryRunner } from "@/features/run-query/model/run-query";
import { formatCompact, formatGrouped } from "@/shared/lib/format-number";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/shared/ui/alert-dialog";

/**
 * Stops before running: an EXPLAIN cost over the profile's limit (409
 * cost_confirmation_required), or ANALYZE on a prod-replica, which executes
 * the statement (UX rule 4).
 */
export function RunConfirmDialog() {
  const { prompt, close } = useRunPrompt();
  const runner = useQueryRunner();

  const confirm = () => {
    if (!prompt) return;
    close();
    if (prompt.type === "cost") void runner.execute({ ...prompt.options, confirmCost: true, analyzeConfirmed: true });
    else runner.request({ ...prompt.options, analyzeConfirmed: true });
  };

  return (
    <AlertDialog open={prompt !== null} onOpenChange={(open) => !open && close()}>
      <AlertDialogContent className="sm:max-w-md">
        {prompt?.type === "cost" && (
          <AlertDialogHeader>
            <AlertDialogTitle>This query looks expensive</AlertDialogTitle>
            <AlertDialogDescription>
              PostgreSQL estimates a cost of{" "}
              <span className="font-mono text-text-primary">{formatGrouped(Math.round(prompt.estimatedCost))}</span>{" "}
              (about <span className="font-mono text-text-primary">{formatCompact(prompt.estimatedRows)}</span> rows).
              The limit for {prompt.options.profile.name} is{" "}
              <span className="font-mono text-text-primary">{formatGrouped(prompt.limit)}</span>. The estimate comes
              from EXPLAIN; nothing has run yet.
            </AlertDialogDescription>
          </AlertDialogHeader>
        )}
        {prompt?.type === "analyze" && (
          <AlertDialogHeader>
            <AlertDialogTitle>ANALYZE runs the query on {prompt.options.profile.name}</AlertDialogTitle>
            <AlertDialogDescription>
              EXPLAIN ANALYZE executes the statement to measure it, on this prod-replica connection. It stays read-only
              and stops at the statement timeout, but it puts the full load of the query on the replica.
            </AlertDialogDescription>
          </AlertDialogHeader>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={confirm}>
            {prompt?.type === "analyze" ? "Run with ANALYZE" : "Run anyway"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
