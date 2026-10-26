import { cn } from "cn";
import { CircleAlert } from "lucide-react";
import { type ReactNode, useId } from "react";
import { Label } from "@/shared/ui/label";

export interface FieldControlProps {
  id: string;
  "aria-describedby": string | undefined;
  "aria-invalid": true | undefined;
}

interface FormFieldProps {
  label: ReactNode;
  error?: string | undefined;
  description?: ReactNode;
  optional?: boolean;
  className?: string;
  children: (control: FieldControlProps) => ReactNode;
}

/** Label, control, help text and inline error wired together by id. */
export function FormField({ label, error, description, optional, className, children }: FormFieldProps) {
  const id = useId();
  const descriptionId = description ? `${id}-description` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [errorId, descriptionId].filter(Boolean).join(" ") || undefined;

  return (
    <div className={cn("flex min-w-0 flex-col gap-1", className)}>
      <Label htmlFor={id} className="text-xs font-medium text-text-primary">
        {label}
        {optional && <span className="font-normal text-text-muted">(optional)</span>}
      </Label>
      {children({ id, "aria-describedby": describedBy, "aria-invalid": error ? true : undefined })}
      {error && (
        <p id={errorId} className="flex items-start gap-1 text-xs text-status-danger-fg">
          <CircleAlert aria-hidden className="mt-0.5 size-3 shrink-0" />
          {error}
        </p>
      )}
      {description && (
        <p id={descriptionId} className="text-xs text-text-muted">
          {description}
        </p>
      )}
    </div>
  );
}
