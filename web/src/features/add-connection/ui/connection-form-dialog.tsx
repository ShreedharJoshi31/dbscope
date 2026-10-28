import { useConnectionDialog } from "@/features/add-connection/model/dialog-store";
import { ConnectionForm } from "@/features/add-connection/ui/connection-form";
import type { Profile } from "@/shared/api";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/shared/ui/dialog";

interface ConnectionFormDialogProps {
  onSaved: (profile: Profile, mode: "create" | "edit") => void;
}

/** "New connection" and "Edit connection" from the workbench. */
export function ConnectionFormDialog({ onSaved }: ConnectionFormDialogProps) {
  const { dialog, close } = useConnectionDialog();
  const editing = dialog?.mode === "edit" ? dialog.profile : undefined;

  return (
    <Dialog open={dialog !== null} onOpenChange={(open) => !open && close()}>
      <DialogContent className="max-h-[calc(100vh-4rem)] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{editing ? `Edit ${editing.name}` : "New connection"}</DialogTitle>
          <DialogDescription>
            {editing
              ? "Changes apply the next time dbscope connects."
              : "Connect dbscope to a database. Credentials stay on this machine."}
          </DialogDescription>
        </DialogHeader>
        {dialog && (
          <ConnectionForm
            key={editing?.id ?? "new"}
            profile={editing}
            submitLabel={editing ? "Save changes" : "Save connection"}
            onCancel={close}
            autoFocus={!editing}
            onSaved={(profile) => {
              close();
              onSaved(profile, dialog.mode);
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
