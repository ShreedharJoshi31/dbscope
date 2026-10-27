import { CircleAlert, KeyRound, LoaderCircle } from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import { useUnlockPrompt } from "@/features/unlock-secrets/model/unlock-prompt";
import { useSecretsStatus, useUnlockSecrets } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/ui/dialog";
import { Input } from "@/shared/ui/input";
import { Label } from "@/shared/ui/label";

/**
 * Opens when the passphrase vault is locked and holds saved passwords, or when a request needed it,
 * unless the user dismissed it. A first-run user with nothing saved is not interrupted.
 */
export function UnlockSecretsDialog() {
  const { data: status } = useSecretsStatus();
  const { dismissed, requested, dismiss } = useUnlockPrompt();
  const open = Boolean(status?.locked) && (Boolean(status?.initialized) || requested) && !dismissed;
  const creating = status?.initialized === false;
  // The form stays mounted while the dialog animates closed; a new key on each open resets it.
  const [formKey, setFormKey] = useState(0);
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setFormKey((key) => key + 1);
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !next && dismiss()}>
      <DialogContent className="sm:max-w-md" showCloseButton={false}>
        <UnlockForm key={formKey} creating={creating} onDismiss={dismiss} />
      </DialogContent>
    </Dialog>
  );
}

function UnlockForm({ creating, onDismiss }: { creating: boolean; onDismiss: () => void }) {
  const id = useId();
  const [passphrase, setPassphrase] = useState("");
  const unlock = useUnlockSecrets();
  const wrong = unlock.error?.code === "unauthorized";

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!passphrase || unlock.isPending) return;
    unlock.mutate(passphrase, {
      onSuccess: () => {
        setPassphrase("");
        announce("Secrets vault unlocked.");
      },
    });
  }

  return (
    <form onSubmit={onSubmit} aria-busy={unlock.isPending} className="flex flex-col gap-4">
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <KeyRound aria-hidden className="size-4 text-text-muted" />
          {creating ? "Choose a passphrase for saved passwords" : "Unlock saved passwords"}
        </DialogTitle>
        <DialogDescription>
          {creating
            ? "This system has no OS keyring, so dbscope keeps connection passwords in a vault encrypted with a passphrase you choose now. It cannot be recovered, so keep it somewhere safe, then retry what you were doing."
            : "This system has no OS keyring, so dbscope keeps connection passwords in a vault encrypted with your passphrase."}
        </DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-1">
        <Label htmlFor={id} className="text-xs font-medium">
          Passphrase
        </Label>
        <Input
          id={id}
          type="password"
          autoComplete={creating ? "new-password" : "current-password"}
          value={passphrase}
          onChange={(e) => setPassphrase(e.target.value)}
          readOnly={unlock.isPending}
          aria-invalid={unlock.isError || undefined}
          aria-describedby={unlock.isError ? `${id}-error` : undefined}
        />
        {unlock.isPending && (
          <p role="status" className="text-xs text-text-muted">
            Unlocking the vault. The first unlock can take a few seconds.
          </p>
        )}
        {unlock.isError && (
          <p id={`${id}-error`} className="flex items-start gap-1 text-xs text-status-danger-fg">
            <CircleAlert aria-hidden className="mt-0.5 size-3 shrink-0" />
            {wrong ? "That passphrase does not unlock the vault." : unlock.error.message}
          </p>
        )}
      </div>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onDismiss}>
          Not now
        </Button>
        <Button type="submit" disabled={!passphrase || unlock.isPending}>
          {unlock.isPending && <LoaderCircle aria-hidden className="animate-spin" />}
          {unlock.isPending ? "Unlocking…" : creating ? "Set passphrase" : "Unlock"}
        </Button>
      </DialogFooter>
    </form>
  );
}
