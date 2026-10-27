import { ListRestart } from "lucide-react";
import { useId } from "react";
import type { LlmFormErrors, LlmFormValues } from "@/features/llm-settings/model/form";
import type { LLMEffort } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import { FormField } from "@/shared/ui/form-field";
import { Input } from "@/shared/ui/input";
import { NativeSelect } from "@/shared/ui/native-select";
import { SegmentedControl } from "@/shared/ui/segmented-control";

type Change = <K extends keyof LlmFormValues>(field: K, value: LlmFormValues[K]) => void;

const EFFORTS: { value: LLMEffort; label: string }[] = [
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
];

function ModelSelect({
  label,
  value,
  models,
  onChange,
}: {
  label: string;
  value: string;
  models: readonly string[];
  onChange: (v: string) => void;
}) {
  const options = models.includes(value) || !value ? models : [value, ...models];
  return (
    <FormField label={label}>
      {(control) => (
        <NativeSelect {...control} value={value} onChange={(e) => onChange(e.target.value)} className="font-mono">
          {options.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </NativeSelect>
      )}
    </FormField>
  );
}

export function AnthropicFields({
  values,
  onChange,
  models,
}: {
  values: LlmFormValues;
  onChange: Change;
  models: readonly string[];
}) {
  const effortLabel = useId();
  return (
    <>
      <div className="grid grid-cols-2 gap-3">
        <ModelSelect label="Model" value={values.model} models={models} onChange={(v) => onChange("model", v)} />
        <ModelSelect
          label="Summary model"
          value={values.summaryModel}
          models={models}
          onChange={(v) => onChange("summaryModel", v)}
        />
      </div>
      <div className="flex flex-col gap-1">
        <span id={effortLabel} className="text-xs font-medium">
          Effort
        </span>
        <SegmentedControl
          aria-labelledby={effortLabel}
          options={EFFORTS}
          value={values.effort}
          onValueChange={(v) => onChange("effort", v)}
          size="sm"
        />
        <p className="text-xs text-text-muted">
          Higher effort thinks longer before answering: slower, sometimes better.
        </p>
      </div>
    </>
  );
}

interface OpenAiFieldsProps {
  values: LlmFormValues;
  errors: LlmFormErrors;
  onChange: Change;
  models: readonly string[] | undefined;
  onLoadModels: () => void;
  loadingModels: boolean;
  /** Why "Load models" is disabled, if it is. */
  loadBlocked: string | undefined;
}

export function OpenAiFields({
  values,
  errors,
  onChange,
  models,
  onLoadModels,
  loadingModels,
  loadBlocked,
}: OpenAiFieldsProps) {
  const listId = useId();
  return (
    <>
      <FormField
        label="Base URL"
        error={errors.baseUrl}
        description="Any OpenAI-compatible Chat Completions endpoint: Ollama, LM Studio, vLLM or OpenAI."
      >
        {(control) => (
          <Input
            {...control}
            value={values.baseUrl}
            onChange={(e) => onChange("baseUrl", e.target.value)}
            placeholder="http://localhost:11434/v1/"
            spellCheck={false}
            className="font-mono text-xs"
          />
        )}
      </FormField>
      <div className="grid grid-cols-[1fr_auto] items-end gap-2">
        <FormField label="Model" error={errors.model}>
          {(control) => (
            <Input
              {...control}
              list={listId}
              value={values.model}
              onChange={(e) => onChange("model", e.target.value)}
              placeholder="llama3.1:8b"
              spellCheck={false}
              className="font-mono text-xs"
            />
          )}
        </FormField>
        <Button
          variant="outline"
          size="sm"
          className="mb-0.5"
          onClick={onLoadModels}
          disabled={loadingModels || Boolean(loadBlocked)}
          title={loadBlocked}
        >
          <ListRestart aria-hidden />
          {loadingModels ? "Loading…" : "Load models"}
        </Button>
      </div>
      <datalist id={listId}>
        {(models ?? []).map((m) => (
          <option key={m} value={m} />
        ))}
      </datalist>
      {loadBlocked && <p className="text-xs text-text-muted">{loadBlocked}</p>}
      {models && (
        <p className="text-xs text-text-muted">{models.length} models available; pick one in the Model field.</p>
      )}
      <FormField label="Summary model" optional description="Leave empty to use the same model for summaries.">
        {(control) => (
          <Input
            {...control}
            list={listId}
            value={values.summaryModel}
            onChange={(e) => onChange("summaryModel", e.target.value)}
            spellCheck={false}
            className="font-mono text-xs"
          />
        )}
      </FormField>
    </>
  );
}
