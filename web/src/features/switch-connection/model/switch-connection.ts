import { useActiveConnectionStore } from "@/entities/connection";
import type { Profile } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";

export function switchConnection(profile: Profile): void {
  useActiveConnectionStore.getState().setActiveId(profile.id);
  announce(`Switched to ${profile.name}.`);
}
