import { useEffect } from "react";
import type { Profile } from "@/shared/api";

const APP_NAME = "dbscope";

export function documentTitleFor(profile: Profile | undefined): string {
  if (!profile) return APP_NAME;
  const prefix = profile.env === "prod-replica" ? "[PROD] " : "";
  return `${prefix}${profile.name} · ${APP_NAME}`;
}

export function useDocumentTitle(profile: Profile | undefined): void {
  const title = documentTitleFor(profile);
  useEffect(() => {
    document.title = title;
    return () => {
      document.title = APP_NAME;
    };
  }, [title]);
}
