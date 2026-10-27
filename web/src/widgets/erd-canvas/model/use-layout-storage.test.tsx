import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import type { CanvasLayoutInput } from "@/shared/api";
import { flushLayoutSaves } from "@/shared/lib/layout-sync";
import { signIn } from "@/test/render";
import { server } from "@/test/server";
import { SAVE_DEBOUNCE_MS } from "@/widgets/erd-canvas/model/layout-saver";
import { useLayoutStorage } from "@/widgets/erd-canvas/model/use-layout-storage";

const layout = (x: number): CanvasLayoutInput => ({
  key: "public",
  positions: { "public.users": { x, y: 0 } },
  collapsed: [],
});

describe("useLayoutStorage", () => {
  it("saves pending changes on flush and nothing after it, so an import is not overwritten", async () => {
    signIn();
    const puts: number[] = [];
    server.use(
      http.get("/api/profiles/p1/layout", () => HttpResponse.json({ error: { code: "not_found" } }, { status: 404 })),
      http.put("/api/profiles/p1/layout", async ({ request }) => {
        const body = (await request.json()) as CanvasLayoutInput;
        puts.push(body.positions["public.users"]?.x ?? -1);
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const { result, unmount } = renderHook(() => useLayoutStorage("p1", "public", false), { wrapper });
    await waitFor(() => expect(result.current.restored).toBeNull());

    result.current.schedule(layout(1));
    await flushLayoutSaves();
    expect(puts).toEqual([1]);

    // A drag (or a schema refresh adding a table) after the flush, while the import runs.
    result.current.schedule(layout(2));
    await new Promise((r) => setTimeout(r, SAVE_DEBOUNCE_MS + 200));
    unmount();
    await new Promise((r) => setTimeout(r, 50));
    expect(puts).toEqual([1]);

    // The remounted canvas saves normally again.
    const again = renderHook(() => useLayoutStorage("p1", "public", false), { wrapper });
    await waitFor(() => expect(again.result.current.restored).toBeNull());
    again.result.current.schedule(layout(3));
    again.unmount();
    await waitFor(() => expect(puts).toEqual([1, 3]));
  });
});
