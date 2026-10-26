import { useAnnouncer } from "@/shared/lib/announcer";

/** Polite announcements for async results (tests, saves). Mounted once. */
export function LiveRegion() {
  const { message, id } = useAnnouncer();
  return (
    <div role="status" aria-live="polite" aria-atomic="true" className="sr-only" data-testid="live-region">
      <span key={id}>{message}</span>
    </div>
  );
}
