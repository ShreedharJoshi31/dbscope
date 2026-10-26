import { ScanSearch, ShieldCheck } from "lucide-react";
import { useActiveConnectionStore } from "@/entities/connection";
import { ConnectionForm } from "@/features/add-connection";
import { ThemeToggle } from "@/features/toggle-theme";

/** First run: no saved connections yet (UX rule 1). */
export function OnboardingPage() {
  const setActiveId = useActiveConnectionStore((s) => s.setActiveId);

  return (
    <div className="flex min-h-full flex-col bg-surface-subtle">
      <header className="flex h-10 shrink-0 items-center justify-between border-b border-border-default px-4">
        <span className="flex items-center gap-1 text-sm font-semibold tracking-tight">
          <ScanSearch aria-hidden className="size-4 text-accent-fg" />
          dbscope
        </span>
        <ThemeToggle />
      </header>
      <main className="flex flex-1 justify-center px-4 py-12">
        <div className="flex w-full max-w-xl flex-col gap-6">
          <div className="flex flex-col gap-1">
            <h1 className="text-xl font-semibold tracking-tight">Connect to PostgreSQL</h1>
            <p className="text-sm text-text-muted">
              Paste a connection URL or enter the details, test the connection, then save it to open the workbench.
            </p>
          </div>
          <div className="rounded-lg border border-border-default bg-surface-raised p-6 shadow-sm">
            <ConnectionForm submitLabel="Save and open" autoFocus onSaved={(profile) => setActiveId(profile.id)} />
          </div>
          <p className="flex items-start gap-2 text-xs text-text-muted">
            <ShieldCheck aria-hidden className="mt-0.5 size-3.5 shrink-0 text-status-success-fg" />
            dbscope runs only on this machine. Passwords go into a local encrypted vault and are never sent back to the
            browser.
          </p>
        </div>
      </main>
    </div>
  );
}
