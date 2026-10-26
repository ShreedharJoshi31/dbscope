import { CircleAlert, LoaderCircle } from "lucide-react";
import { useState } from "react";
import { ErrorBoundary } from "@/app/error-boundary";
import { AppHotkeys } from "@/app/hotkeys";
import { Providers } from "@/app/providers";
import { createQueryClient } from "@/app/query-client";
import { ShortcutSheet } from "@/features/show-shortcuts";
import { UnlockSecretsDialog } from "@/features/unlock-secrets";
import { NoSessionPage } from "@/pages/no-session";
import { OnboardingPage } from "@/pages/onboarding";
import { WorkbenchPage } from "@/pages/workbench";
import { getToken, useProfiles } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import { LiveRegion } from "@/shared/ui/live-region";

function Screen() {
  const profiles = useProfiles();

  if (profiles.isPending) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-sm text-text-muted">
        <LoaderCircle aria-hidden className="size-4 animate-spin" />
        Loading connections…
      </div>
    );
  }
  if (profiles.isError) {
    if (profiles.error.code === "unauthorized") return <NoSessionPage reason="rejected" />;
    return (
      <main className="flex h-full items-center justify-center p-6">
        <div role="alert" className="flex max-w-md flex-col items-start gap-3">
          <p className="flex items-start gap-2 text-sm font-medium text-status-danger-fg">
            <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
            Could not load connections
          </p>
          <p className="text-sm text-text-muted">{profiles.error.message}</p>
          <Button variant="outline" onClick={() => profiles.refetch()}>
            Try again
          </Button>
        </div>
      </main>
    );
  }

  const hasProfiles = profiles.data.length > 0;
  return (
    <>
      <AppHotkeys workbench={hasProfiles} />
      {hasProfiles ? <WorkbenchPage /> : <OnboardingPage />}
    </>
  );
}

export function App() {
  const [queryClient] = useState(createQueryClient);

  if (!getToken()) return <NoSessionPage reason="missing" />;

  return (
    <ErrorBoundary>
      <Providers queryClient={queryClient}>
        <Screen />
        <UnlockSecretsDialog />
        <ShortcutSheet />
        <LiveRegion />
      </Providers>
    </ErrorBoundary>
  );
}
