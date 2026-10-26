import { KeyRound } from "lucide-react";

interface NoSessionPageProps {
  /** "missing": opened without a token. "rejected": the agent no longer accepts it (it restarted). */
  reason: "missing" | "rejected";
}

export function NoSessionPage({ reason }: NoSessionPageProps) {
  return (
    <main className="flex min-h-full items-center justify-center bg-surface-subtle p-6">
      <div className="flex max-w-md flex-col gap-3 rounded-lg border border-border-default bg-surface-raised p-6 shadow-sm">
        <KeyRound aria-hidden className="size-5 text-text-muted" />
        <h1 className="text-lg font-semibold">
          Open dbscope from the URL printed by <code className="font-mono">dbscope ui</code>
        </h1>
        <p className="text-sm text-text-muted">
          {reason === "missing"
            ? "This page needs the one-time session link that the dbscope agent prints when it starts. The link keeps other websites and processes from talking to your databases."
            : "The dbscope agent did not accept this session. It has probably restarted with a new session link."}
        </p>
        <ol className="list-decimal pl-5 text-sm">
          <li>
            Run <code className="font-mono">dbscope ui</code> in a terminal (or find the terminal where it is running).
          </li>
          <li>Open the http://127.0.0.1 link it prints, including the part after #.</li>
        </ol>
      </div>
    </main>
  );
}
