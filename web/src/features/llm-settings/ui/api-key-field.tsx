import { CircleCheck, KeyRound } from "lucide-react";
import { Button } from "@/shared/ui/button";
import { FormField } from "@/shared/ui/form-field";
import { Input } from "@/shared/ui/input";

interface ApiKeyFieldProps {
  value: string;
  onChange: (value: string) => void;
  /** A key for this provider is stored in the vault. */
  saved: boolean;
  optional: boolean;
  error?: string | undefined;
  onRemove: () => void;
  removing: boolean;
}

/** The key is write-only: the agent never returns it, and the field never shows a stored key. */
export function ApiKeyField({ value, onChange, saved, optional, error, onRemove, removing }: ApiKeyFieldProps) {
  return (
    <div className="flex flex-col gap-1.5">
      <FormField
        label="API key"
        optional={optional}
        error={error}
        description={
          saved
            ? "Type a new key only to replace the saved one."
            : optional
              ? "Local servers such as Ollama usually need none."
              : "Stored in dbscope's encrypted vault on this machine, never shown again."
        }
      >
        {(control) => (
          <Input
            {...control}
            type="password"
            autoComplete="new-password"
            spellCheck={false}
            value={value}
            placeholder={saved ? "•••••••• saved" : "Paste the key"}
            onChange={(e) => onChange(e.target.value)}
            className="font-mono"
          />
        )}
      </FormField>
      {saved && (
        <div className="flex items-center gap-2 text-xs">
          <span data-testid="api-key-saved" className="flex items-center gap-1 text-status-success-fg">
            <CircleCheck aria-hidden className="size-3.5" />
            Key saved
          </span>
          <Button variant="ghost" size="xs" onClick={onRemove} disabled={removing}>
            <KeyRound aria-hidden />
            Remove key
          </Button>
        </div>
      )}
    </div>
  );
}
