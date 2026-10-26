import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { cn } from "cn";
import type { ReactNode, Ref } from "react";

export interface SegmentedOption<T extends string> {
  value: T;
  label: ReactNode;
}

interface SegmentedControlProps<T extends string> {
  options: readonly SegmentedOption<T>[];
  value: T | undefined;
  onValueChange: (value: T) => void;
  name?: string;
  required?: boolean;
  invalid?: boolean;
  "aria-labelledby"?: string;
  "aria-describedby"?: string;
  firstRadioRef?: Ref<HTMLButtonElement>;
  className?: string;
}

/** Single choice rendered as joined buttons; a radio group for assistive tech. */
export function SegmentedControl<T extends string>({
  options,
  value,
  onValueChange,
  invalid,
  firstRadioRef,
  className,
  ...groupProps
}: SegmentedControlProps<T>) {
  return (
    <RadioGroup
      {...groupProps}
      value={value ?? null}
      onValueChange={(next) => onValueChange(next as T)}
      aria-invalid={invalid || undefined}
      className={cn(
        "inline-flex h-8 w-fit items-stretch rounded-md border border-border-control bg-surface-canvas p-px",
        invalid && "border-status-danger-fg",
        className,
      )}
    >
      {options.map((option, index) => (
        <Radio.Root
          key={option.value}
          value={option.value}
          ref={index === 0 ? firstRadioRef : undefined}
          className={cn(
            "inline-flex items-center gap-1 rounded-sm px-3 text-sm text-text-muted outline-none transition-colors",
            "hover:bg-surface-hover hover:text-text-primary",
            "focus-visible:z-10 focus-visible:ring-2 focus-visible:ring-border-focus",
            "data-checked:bg-surface-selected data-checked:font-medium data-checked:text-text-primary",
          )}
        >
          {option.label}
        </Radio.Root>
      ))}
    </RadioGroup>
  );
}
