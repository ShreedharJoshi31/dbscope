import { resolveActiveProfile, useActiveConnectionStore } from "@/entities/connection/model/active-connection";
import { type Profile, useProfiles } from "@/shared/api";

/** The connection the workbench is looking at, plus the full list. */
export function useActiveConnection(): { profile: Profile | undefined; profiles: Profile[] } {
  const { data: profiles = [] } = useProfiles();
  const activeId = useActiveConnectionStore((s) => s.activeId);
  return { profile: resolveActiveProfile(profiles, activeId), profiles };
}
