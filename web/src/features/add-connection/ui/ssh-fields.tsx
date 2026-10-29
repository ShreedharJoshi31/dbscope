import { type ChangeEvent, useId } from "react";
import type { ConnectionField, ConnectionFormValues } from "@/features/add-connection/model/form";
import { Checkbox } from "@/shared/ui/checkbox";
import { FormField } from "@/shared/ui/form-field";
import { Input } from "@/shared/ui/input";

interface SshFieldsProps {
  values: ConnectionFormValues;
  errorFor: (field: ConnectionField) => string | undefined;
  onChange: <K extends ConnectionField>(field: K, value: ConnectionFormValues[K]) => void;
  onBlur: (field: ConnectionField) => void;
  register: (field: ConnectionField) => (el: HTMLElement | null) => void;
  hasSavedPassphrase: boolean;
}

/** The SSH server (bastion) a database is reached through, for one only reachable from inside its network. */
export function SshFields({ values, errorFor, onChange, onBlur, register, hasSavedPassphrase }: SshFieldsProps) {
  const hintId = useId();
  const text = (field: "sshHost" | "sshPort" | "sshUser" | "sshKeyFile") => ({
    ref: register(field),
    value: values[field],
    onChange: (e: ChangeEvent<HTMLInputElement>) => onChange(field, e.target.value),
    onBlur: () => onBlur(field),
  });

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-0.5">
        {/* biome-ignore lint/a11y/noLabelWithoutControl: Base UI's Checkbox renders the control inside the wrapping label. */}
        <label className="flex items-center gap-2 text-sm font-medium">
          <Checkbox checked={values.ssh} onCheckedChange={(on) => onChange("ssh", on)} aria-describedby={hintId} />
          Connect through an SSH server
        </label>
        <p id={hintId} className="pl-6 text-xs text-text-muted">
          For a database reachable only from a bastion host. Host and port above are as the SSH server sees them.
        </p>
      </div>
      {values.ssh && (
        <div className="grid grid-cols-6 gap-x-3 gap-y-4 border-l border-border-default pl-4">
          <FormField
            label="SSH host"
            error={errorFor("sshHost")}
            className="col-span-4"
            description="Its host key must be in ~/.ssh/known_hosts: connect once with ssh to add it."
          >
            {(control) => (
              <Input {...control} {...text("sshHost")} placeholder="bastion.example.com" spellCheck={false} />
            )}
          </FormField>
          <FormField label="SSH port" error={errorFor("sshPort")} className="col-span-2">
            {(control) => <Input {...control} {...text("sshPort")} inputMode="numeric" className="tabular-nums" />}
          </FormField>
          <FormField label="SSH user" error={errorFor("sshUser")} className="col-span-6">
            {(control) => <Input {...control} {...text("sshUser")} placeholder="ec2-user" spellCheck={false} />}
          </FormField>
          <FormField
            label="Key file"
            optional
            error={errorFor("sshKeyFile")}
            className="col-span-6"
            description="Keys in ssh-agent are tried first. Leave empty to also try ~/.ssh/id_ed25519, id_ecdsa and id_rsa."
          >
            {(control) => (
              <Input
                {...control}
                {...text("sshKeyFile")}
                placeholder="/Users/you/.ssh/id_ed25519"
                className="font-mono text-xs"
                spellCheck={false}
              />
            )}
          </FormField>
          {values.sshKeyFile.trim() && (
            <FormField
              label="Key passphrase"
              optional
              className="col-span-6"
              description={
                hasSavedPassphrase
                  ? "Leave empty to keep the saved passphrase."
                  : "Only for an encrypted key file. Stored in the encrypted vault on this machine."
              }
            >
              {(control) => (
                <Input
                  {...control}
                  type="password"
                  value={values.sshPassphrase}
                  onChange={(e) => onChange("sshPassphrase", e.target.value)}
                  placeholder={hasSavedPassphrase ? "••••••••" : undefined}
                  autoComplete="new-password"
                />
              )}
            </FormField>
          )}
        </div>
      )}
    </div>
  );
}
