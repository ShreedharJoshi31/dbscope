import type { Profile, ProfileInput } from "@/shared/api";

/** The update body for a profile with new schemas. `password` is omitted, so the stored one is kept. */
export function withSchemas(profile: Profile, schemas: string[]): ProfileInput {
  const { id: _id, hasPassword: _has, capabilities: _caps, createdAt: _c, updatedAt: _u, ...rest } = profile;
  return { ...rest, schemas: [...schemas].sort() };
}
