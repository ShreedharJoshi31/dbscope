import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { ChevronRight } from "lucide-react";
import { useId } from "react";
import type { ConnectionFormErrors, ConnectionFormValues } from "@/features/add-connection/model/form";
import type { SummaryMode } from "@/shared/api";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/shared/ui/collapsible";
import { FormField } from "@/shared/ui/form-field";
import { Input } from "@/shared/ui/input";

interface AdvancedFieldsProps {
  values: ConnectionFormValues;
  errors: ConnectionFormErrors;
  onChange: <K extends keyof ConnectionFormValues>(field: K, value: ConnectionFormValues[K]) => void;
  onBlur: (field: keyof ConnectionFormValues) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  timeoutRef: (el: HTMLInputElement | null) => void;
  maxCostRef?: (el: HTMLInputElement | null) => void;
}

function summaryModes(maxRows: number): { value: SummaryMode; title: string; text: string }[] {
  return [
    { value: "off", title: "Off", text: "The assistant sees your schema and SQL, never query results." },
    {
      value: "profile",
      title: "Profile (default)",
      text: "Adds a statistical summary of results, such as row counts and value ranges, without row values.",
    },
    {
      value: "rows",
      title: "Rows",
      text: `Also sends up to ${maxRows} result rows to your LLM provider. Pick this only if that provider may see this data.`,
    },
  ];
}

export function AdvancedFields({
  values,
  errors,
  onChange,
  onBlur,
  open,
  onOpenChange,
  timeoutRef,
  maxCostRef,
}: AdvancedFieldsProps) {
  const summaryLabelId = useId();

  return (
    <Collapsible open={open} onOpenChange={onOpenChange}>
      <CollapsibleTrigger className="group/advanced flex items-center gap-1 rounded-sm text-xs font-medium text-text-muted hover:text-text-primary">
        <ChevronRight aria-hidden className="size-3.5 transition-transform group-data-panel-open/advanced:rotate-90" />
        Advanced
        <span className="font-normal">schemas, assistant data, statement timeout, cost limit</span>
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-3 flex flex-col gap-4 border-l border-border-default pl-4">
        <FormField
          label="Schemas"
          optional
          description="Comma-separated, for example public, billing. Leave empty for all non-system schemas."
        >
          {(control) => (
            <Input
              {...control}
              value={values.schemas}
              onChange={(e) => onChange("schemas", e.target.value)}
              onBlur={() => onBlur("schemas")}
              placeholder="All non-system schemas"
              className="font-mono text-xs"
              spellCheck={false}
            />
          )}
        </FormField>

        <div className="flex flex-col gap-2">
          <span id={summaryLabelId} className="text-xs font-medium">
            What the assistant may send to your LLM provider
          </span>
          <RadioGroup
            aria-labelledby={summaryLabelId}
            value={values.summaryMode}
            onValueChange={(v) => onChange("summaryMode", v as SummaryMode)}
            className="flex flex-col gap-2"
          >
            {summaryModes(values.maxRowsToLLM).map((mode) => (
              // biome-ignore lint/a11y/noLabelWithoutControl: Radio.Root renders the control inside this label.
              <label key={mode.value} className="flex cursor-pointer items-start gap-2 text-sm">
                <Radio.Root
                  value={mode.value}
                  className="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border border-border-control bg-surface-canvas outline-none focus-visible:ring-2 focus-visible:ring-border-focus data-checked:border-accent-emphasis"
                >
                  <Radio.Indicator className="size-2 rounded-full bg-accent-emphasis" />
                </Radio.Root>
                <span className="flex flex-col">
                  <span className="font-medium">{mode.title}</span>
                  <span className="text-xs text-text-muted">{mode.text}</span>
                </span>
              </label>
            ))}
          </RadioGroup>
        </div>

        <FormField
          label="Statement timeout (ms)"
          error={errors.statementTimeoutMs}
          description="Queries running longer are cancelled on the server. Default 30000, or 10000 for prod-replica."
          className="max-w-60"
        >
          {(control) => (
            <Input
              {...control}
              ref={timeoutRef}
              inputMode="numeric"
              value={values.statementTimeoutMs}
              onChange={(e) => onChange("statementTimeoutMs", e.target.value)}
              onBlur={() => onBlur("statementTimeoutMs")}
              className="font-mono tabular-nums"
            />
          )}
        </FormField>

        <FormField
          label="Cost limit"
          optional
          error={errors.maxCost}
          description="Queries whose EXPLAIN cost is higher ask before running. Blank = environment default (prod-replica 1,000,000; staging 10,000,000; local no limit), 0 = no limit."
          className="max-w-80"
        >
          {(control) => (
            <Input
              {...control}
              ref={maxCostRef}
              inputMode="decimal"
              value={values.maxCost}
              onChange={(e) => onChange("maxCost", e.target.value)}
              onBlur={() => onBlur("maxCost")}
              placeholder="Environment default"
              className="font-mono tabular-nums"
            />
          )}
        </FormField>
      </CollapsibleContent>
    </Collapsible>
  );
}
