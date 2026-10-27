import type { UseQueryResult } from "@tanstack/react-query";
import { useActiveConnection } from "@/entities/connection";
import { type SchemaSource, useWorkspaceStore } from "@/entities/schema";
import { layoutKeyFor } from "@/features/load-schema/model/layout-key";
import {
  type ApiError,
  type Model,
  type Profile,
  schemaQueryKeys,
  useProfileSchema,
  useSampleSchema,
} from "@/shared/api";

export interface WorkbenchSchema {
  source: SchemaSource;
  /** The connection the schema comes from; undefined in the sample workspace. */
  profile: Profile | undefined;
  query: UseQueryResult<Model, ApiError>;
  queryKey: readonly unknown[];
  layoutKey: string | undefined;
  /** The sample is read-only: decisions and positions stay in this browser. */
  readOnly: boolean;
}

/** The schema the explorer, canvas and status bar show: the active connection's, or the sample. */
export function useWorkbenchSchema(): WorkbenchSchema {
  const source = useWorkspaceStore((s) => s.source);
  const { profile } = useActiveConnection();
  const sample = useSampleSchema(source === "sample");
  const live = useProfileSchema(source === "profile" ? profile?.id : undefined);
  const query = source === "sample" ? sample : live;
  return {
    source,
    profile: source === "profile" ? profile : undefined,
    query,
    queryKey: source === "sample" ? schemaQueryKeys.sample : schemaQueryKeys.schema(profile?.id ?? ""),
    layoutKey: query.data ? layoutKeyFor(query.data) : undefined,
    readOnly: source === "sample",
  };
}
