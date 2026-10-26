import { CircleAlert } from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import { EnvBadge } from "@/entities/connection";
import { canConfirmDelete, useDeleteConnection } from "@/features/delete-connection/model/delete-store";
import { type Profile, useDeleteProfile } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/shared/ui/alert-dialog";
import { Button } from "@/shared/ui/button";
import { Input } from "@/shared/ui/input";
import { Label } from "@/shared/ui/label";

interface DeleteConnectionDialogProps {
  onDeleted: (profile: Profile) => void;
}

export function DeleteConnectionDialog({ onDeleted }: DeleteConnectionDialogProps) {
  const { target, close } = useDeleteConnection();
  return (
    <AlertDialog open={target !== null} onOpenChange={(open) => !open && close()}>
      <AlertDialogContent className="sm:max-w-md">
        {target && <DeleteConfirmation key={target.id} profile={target} onDeleted={onDeleted} onClose={close} />}
      </AlertDialogContent>
    </AlertDialog>
  );
}

function DeleteConfirmation({
  profile,
  onDeleted,
  onClose,
}: {
  profile: Profile;
  onDeleted: (profile: Profile) => void;
  onClose: () => void;
}) {
  const inputId = useId();
  const [typed, setTyped] = useState("");
  const remove = useDeleteProfile();
  const needsTyping = profile.env === "prod-replica";
  const allowed = canConfirmDelete(profile, typed);

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!allowed || remove.isPending) return;
    remove.mutate(profile.id, {
      onSuccess: () => {
        announce(`Deleted connection ${profile.name}.`);
        onClose();
        onDeleted(profile);
      },
    });
  }

  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-4">
      <AlertDialogHeader>
        <AlertDialogTitle className="flex items-center gap-2">
          Delete {profile.name}? <EnvBadge env={profile.env} />
        </AlertDialogTitle>
        <AlertDialogDescription>
          This removes the connection and its saved password from this machine. The database itself is not changed.
        </AlertDialogDescription>
      </AlertDialogHeader>

      {needsTyping && (
        <div className="flex flex-col gap-1">
          <Label htmlFor={inputId} className="text-xs font-medium">
            Type <span className="font-mono font-semibold">{profile.name}</span> to confirm
          </Label>
          <Input
            id={inputId}
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
            autoFocus
          />
        </div>
      )}

      {remove.isError && (
        <p role="alert" className="flex items-start gap-2 text-sm text-status-danger-fg">
          <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
          {remove.error.message}
        </p>
      )}

      <AlertDialogFooter>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <Button type="submit" variant="destructive" disabled={!allowed || remove.isPending}>
          {remove.isPending ? "Deleting…" : "Delete connection"}
        </Button>
      </AlertDialogFooter>
    </form>
  );
}
