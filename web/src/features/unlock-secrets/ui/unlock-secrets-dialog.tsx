import { CircleAlert, KeyRound } from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import { useUnlockPrompt } from "@/features/unlock-secrets/model/unlock-prompt";
import { useSecretsStatus, useUnlockSecrets } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/ui/dialog";
import { Input } from "@/shared/ui/input";
import { Label } from "@/shared/ui/label";

/** Opens whenever the passphrase vault reports locked, unless the user dismissed it. */
export function UnlockSecretsDialog() {
  const { data: status } = useSecretsStatus();
  const { dismissed, dismiss } = useUnlockPrompt();
  const open = Boolean(status?.locked) && !dismissed;

  return (
    <Dialog open={open} onOpenChange={(next) => !next && dismiss()}>
      <DialogContent className="sm:max-w-md" showCloseButton={false}>
        {open && <UnlockForm onDismiss={dismiss} />}
      </DialogContent>
    </Dialog>
  );
}

function UnlockForm({ onDismiss }: { onDismiss: () => void }) {
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
    <form onSubmit={onSubmit} className="flex flex-col gap-4">
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <KeyRound aria-hidden className="size-4 text-text-muted" />
          Unlock saved passwords
        </DialogTitle>
        <DialogDescription>
          This system has no OS keyring, so dbscope keeps connection passwords in a vault encrypted with your
          passphrase. The first time you unlock, the passphrase you enter becomes the vault passphrase. It cannot be
          recovered, so keep it somewhere safe.
        </DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-1">
        <Label htmlFor={id} className="text-xs font-medium">
          Passphrase
        </Label>
        <Input
          id={id}
          type="password"
          autoComplete="current-password"
          value={passphrase}
          onChange={(e) => setPassphrase(e.target.value)}
          aria-invalid={unlock.isError || undefined}
          aria-describedby={unlock.isError ? `${id}-error` : undefined}
        />
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
          {unlock.isPending ? "Unlocking…" : "Unlock"}
        </Button>
      </DialogFooter>
    </form>
  );
}
