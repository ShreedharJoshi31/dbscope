import { OctagonX } from "lucide-react";
import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "@/shared/ui/button";

interface ErrorBoundaryState {
  error: Error | null;
}

export class ErrorBoundary extends Component<{ children: ReactNode }, ErrorBoundaryState> {
  override state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  override componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error("dbscope UI crashed", error, info.componentStack);
  }

  override render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <main role="alert" className="flex min-h-full items-center justify-center bg-surface-subtle p-6">
        <div className="flex max-w-md flex-col gap-3 rounded-lg border border-border-default bg-surface-raised p-6">
          <OctagonX aria-hidden className="size-5 text-status-danger-fg" />
          <h1 className="text-lg font-semibold">The dbscope UI hit an unexpected error</h1>
          <p className="break-words font-mono text-xs text-text-muted">{error.message}</p>
          <p className="text-sm text-text-muted">Your saved connections are safe. Reloading usually recovers.</p>
          <Button className="self-start" onClick={() => window.location.reload()}>
            Reload
          </Button>
        </div>
      </main>
    );
  }
}
