import { CSPProvider } from "@base-ui/react/csp-provider";
import { type QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useThemeSync } from "@/app/theme";
import { TooltipProvider } from "@/shared/ui/tooltip";

interface ProvidersProps {
  queryClient: QueryClient;
  children: ReactNode;
}

function ThemeSync() {
  useThemeSync();
  return null;
}

/**
 * The agent serves the UI with CSP `default-src 'self'`, which blocks inline
 * <style> tags; Base UI is told not to inject any.
 */
export function Providers({ queryClient, children }: ProvidersProps) {
  return (
    <CSPProvider disableStyleElements>
      <QueryClientProvider client={queryClient}>
        <TooltipProvider delay={400}>
          <ThemeSync />
          {children}
        </TooltipProvider>
      </QueryClientProvider>
    </CSPProvider>
  );
}
