import { TEST_ERROR_COPY } from "@/features/llm-settings/model/form";
import type { ApiError, LLMTestResult } from "@/shared/api";
import { StatusIcon } from "@/shared/ui/status-icon";

/** The /api/llm/test outcome with human copy per error kind. */
export function TestResultView({ result, error }: { result?: LLMTestResult | undefined; error?: ApiError | null }) {
  if (error) {
    return (
      <div role="alert" className="flex items-start gap-1.5 text-xs">
        <StatusIcon tone="danger" className="size-3.5" />
        <span>
          <span className="font-medium text-status-danger-fg">Could not run the test.</span> {error.message}
        </span>
      </div>
    );
  }
  if (!result) return null;
  if (result.ok) {
    return (
      <p role="status" data-testid="llm-test-result" className="flex items-center gap-1.5 text-xs">
        <StatusIcon tone="success" className="size-3.5" />
        <span>
          <span className="font-medium text-status-success-fg">Connected.</span> {result.model} answered in{" "}
          <span className="font-mono">{result.latencyMs} ms</span>.
        </span>
      </p>
    );
  }
  const copy = TEST_ERROR_COPY[result.error?.kind ?? "other"] ?? TEST_ERROR_COPY.other;
  return (
    <div role="alert" data-testid="llm-test-result" className="flex items-start gap-1.5 text-xs">
      <StatusIcon tone="danger" className="mt-0.5 size-3.5" />
      <span className="flex flex-col gap-0.5">
        <span className="font-medium text-status-danger-fg">{copy.title}</span>
        <span className="text-text-muted">{copy.hint}</span>
        {result.error?.message && <span className="font-mono break-words">{result.error.message}</span>}
      </span>
    </div>
  );
}
