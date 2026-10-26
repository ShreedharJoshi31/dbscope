import { MutationCache, QueryCache, QueryClient } from "@tanstack/react-query";
import { useUnlockPrompt } from "@/features/unlock-secrets";
import { isApiError, queryKeys } from "@/shared/api";

const MAX_NETWORK_RETRIES = 2;

export function createQueryClient(): QueryClient {
  // A locked vault surfaces as 423 on any secret-using call; refresh the status so the unlock dialog opens.
  const onError = (error: unknown) => {
    if (!isApiError(error) || error.code !== "secrets_locked") return;
    useUnlockPrompt.getState().reopen();
    void client.invalidateQueries({ queryKey: queryKeys.secretsStatus });
  };

  const client = new QueryClient({
    queryCache: new QueryCache({ onError }),
    mutationCache: new MutationCache({ onError }),
    defaultOptions: {
      queries: {
        // Only transport failures are worth retrying; API errors are answers.
        retry: (failures, error) => isApiError(error) && error.code === "network" && failures < MAX_NETWORK_RETRIES,
        refetchOnWindowFocus: false,
      },
      mutations: { retry: false },
    },
  });
  return client;
}
