import { Network } from "lucide-react";
import type { ReactNode } from "react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/shared/ui/tabs";

/** Centre tab strip. Phase 0 has the ERD tab only; query and migration tabs join it later. */
export function CentreTabs({ erd }: { erd: ReactNode }) {
  return (
    <Tabs defaultValue="erd" className="flex h-full min-h-0 flex-col gap-0">
      <div className="flex h-8 shrink-0 items-end border-b border-border-default bg-surface-subtle px-2">
        <TabsList variant="line" aria-label="Open tabs" className="h-8 gap-0 p-0">
          <TabsTrigger value="erd" className="h-8 flex-none rounded-none px-3 text-xs">
            <Network aria-hidden />
            ERD
          </TabsTrigger>
        </TabsList>
      </div>
      <TabsContent value="erd" className="min-h-0 flex-1 bg-surface-canvas">
        {erd}
      </TabsContent>
    </Tabs>
  );
}
