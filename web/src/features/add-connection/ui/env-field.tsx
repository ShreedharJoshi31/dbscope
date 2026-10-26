import { CircleAlert, Lightbulb } from "lucide-react";
import { type Ref, useId } from "react";
import { ENV_META, ENV_ORDER, isLoopbackHost } from "@/entities/connection";
import type { Env } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import { SegmentedControl } from "@/shared/ui/segmented-control";

interface EnvFieldProps {
  value: Env | undefined;
  host: string;
  error?: string | undefined;
  onChange: (env: Env) => void;
  firstRadioRef?: Ref<HTMLButtonElement>;
}

const OPTIONS = ENV_ORDER.map((env) => {
  const Icon = ENV_META[env].icon;
  return {
    value: env,
    label: (
      <>
        <Icon aria-hidden className="size-3.5" />
        {ENV_META[env].label}
      </>
    ),
  };
});

export function EnvField({ value, host, error, onChange, firstRadioRef }: EnvFieldProps) {
  const id = useId();
  const labelId = `${id}-label`;
  const descriptionId = `${id}-description`;
  const errorId = `${id}-error`;
  // Only loopback hosts get a suggestion; anything else could be production.
  const suggestLocal = !value && isLoopbackHost(host);

  return (
    <div className="flex flex-col gap-1">
      <span id={labelId} className="text-xs font-medium">
        Environment <span className="font-normal text-text-muted">(required)</span>
      </span>
      <SegmentedControl
        options={OPTIONS}
        value={value}
        onValueChange={onChange}
        required
        invalid={Boolean(error)}
        aria-labelledby={labelId}
        aria-describedby={error ? `${errorId} ${descriptionId}` : descriptionId}
        firstRadioRef={firstRadioRef}
      />
      {error && (
        <p id={errorId} className="flex items-start gap-1 text-xs text-status-danger-fg">
          <CircleAlert aria-hidden className="mt-0.5 size-3 shrink-0" />
          {error}
        </p>
      )}
      {suggestLocal && (
        <p className="flex items-center gap-2 text-xs text-text-muted">
          <Lightbulb aria-hidden className="size-3 shrink-0 text-status-info-fg" />
          This host is on your machine.
          <Button type="button" variant="link" size="xs" className="h-auto p-0" onClick={() => onChange("local")}>
            Use local
          </Button>
        </p>
      )}
      <p id={descriptionId} className="text-xs text-text-muted">
        {value ? ENV_META[value].description : "dbscope uses this to decide how cautious to be."}
      </p>
    </div>
  );
}
