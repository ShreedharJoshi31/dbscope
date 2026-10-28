import { GitCompareArrows, LoaderCircle } from "lucide-react";
import { useId, useState } from "react";
import { ENGINE_META, useActiveConnection } from "@/entities/connection";
import { showComparison, useCompareStore } from "@/features/compare-schema/model/compare";
import { useWorkbenchSchema } from "@/features/load-schema";
import { fetchProfileSchema, fetchSnapshot, useProfiles, useSnapshots } from "@/shared/api";
import { formatAge } from "@/shared/lib/relative-time";
import { Button } from "@/shared/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/ui/dialog";
import { NativeSelect } from "@/shared/ui/native-select";
import { SegmentedControl } from "@/shared/ui/segmented-control";

type Target = "snapshot" | "connection";

/** Picks what to compare this connection's schema with: one of its own snapshots, or another connection of the same engine. */
export function CompareSchemaDialog() {
  const open = useCompareStore((s) => s.open);
  const setOpen = useCompareStore((s) => s.setOpen);
  const { profile } = useActiveConnection();
  const { query } = useWorkbenchSchema();
  const snapshots = useSnapshots(profile?.id, open);
  const profiles = useProfiles();
  const [target, setTarget] = useState<Target>("snapshot");
  const [snapshot, setSnapshot] = useState("");
  const [other, setOther] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pickId = useId();
  const others = (profiles.data ?? []).filter((p) => p.id !== profile?.id && p.engine === profile?.engine);
  // The newest snapshot is usually the schema on screen; offer the older ones first.
  const older = snapshots.data?.slice(1) ?? [];
  const chosenSnapshot = snapshot || String(older[0]?.id ?? "");
  const chosenOther = other || (others[0]?.id ?? "");

  const compare = async () => {
    if (!profile || !query.data) return;
    setBusy(true);
    setError(null);
    try {
      if (target === "snapshot") {
        const info = older.find((s) => String(s.id) === chosenSnapshot);
        if (!info) return;
        showComparison(
          query.data,
          await fetchSnapshot(profile.id, info.id),
          `the schema from ${formatAge(info.takenAt)}`,
        );
      } else {
        const p = others.find((x) => x.id === chosenOther);
        if (!p) return;
        showComparison(query.data, await fetchProfileSchema(p.id), p.name);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const ready = target === "snapshot" ? Boolean(chosenSnapshot) : Boolean(chosenOther);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <GitCompareArrows aria-hidden className="size-4" />
            Compare schema
          </DialogTitle>
          <DialogDescription>
            Shows what differs in {profile?.name ?? "this connection"} from an earlier schema of it, or from another{" "}
            {profile ? ENGINE_META[profile.engine].label : ""} connection: tables, columns, keys, indexes and risks.
          </DialogDescription>
        </DialogHeader>
        <SegmentedControl
          aria-label="Compare with"
          options={[
            { value: "snapshot", label: "An earlier schema" },
            { value: "connection", label: "Another connection" },
          ]}
          value={target}
          onValueChange={setTarget}
        />
        <div className="flex flex-col gap-1">
          <label htmlFor={pickId} className="text-xs font-medium">
            {target === "snapshot" ? "Schema as read" : "Connection"}
          </label>
          {target === "snapshot" ? (
            older.length === 0 ? (
              <p className="text-xs text-text-muted">
                {snapshots.isPending
                  ? "Loading…"
                  : "No earlier schema yet: dbscope keeps one each time the schema is read and its structure has changed."}
              </p>
            ) : (
              <NativeSelect id={pickId} value={chosenSnapshot} onChange={(e) => setSnapshot(e.target.value)}>
                {older.map((s) => (
                  <option key={s.id} value={s.id}>
                    {formatAge(s.takenAt)} · {s.relations} relations · {new Date(s.takenAt).toLocaleString()}
                  </option>
                ))}
              </NativeSelect>
            )
          ) : others.length === 0 ? (
            <p className="text-xs text-text-muted">
              No other {profile ? ENGINE_META[profile.engine].label : ""} connection to compare with.
            </p>
          ) : (
            <NativeSelect id={pickId} value={chosenOther} onChange={(e) => setOther(e.target.value)}>
              {others.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name} ({p.env})
                </option>
              ))}
            </NativeSelect>
          )}
        </div>
        {error && (
          <p role="alert" className="text-xs text-status-danger-fg">
            Could not read the other schema: {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button onClick={() => void compare()} disabled={!ready || busy || !query.data}>
            {busy && <LoaderCircle aria-hidden className="size-3.5 animate-spin" />}
            Compare
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
